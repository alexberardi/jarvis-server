package models

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/platform/engines"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// InstallJobType is the queue job that downloads a model (and its projector and engine).
const InstallJobType = "llm.models.install"

// InstallSmallJobType is a second download lane for small installs (voice models,
// embeddings, small engines), so they don't wait behind a multi-gigabyte LLM. Each lane runs
// one install at a time, so at most two downloads share the link.
const InstallSmallJobType = "llm.models.install.small"

// defaultSmallInstallBytes is the largest install (model + projector + engine still to fetch)
// that goes to the small lane.
const defaultSmallInstallBytes = 2 << 30

// installMaxAttempts bounds retries of transient failures within one install slice.
const installMaxAttempts = 6

// Labels is the part of the resolver the manager uses.
type Labels interface {
	Status(ctx context.Context) []engine.LabelStatus
	Instances() []engine.InstanceStatus
	Notify()
}

// Manager installs, lists, deletes and assigns models.
type Manager struct {
	Store     *Store
	Settings  *settings.Service
	Queue     *queue.Queue
	Binaries  *engine.Binaries
	Detector  *engine.Detector
	Labels    Labels
	HF        *HF
	ModelsDir string // <home>/models
	Client    *http.Client
	Log       *slog.Logger
	// SliceDuration bounds one install job run. A longer download continues in a follow-up
	// job, so the job lease stays short and a crash resumes within a lease (default 8m;
	// the lease is SliceDuration + 2m).
	SliceDuration time.Duration
	// ProgressEvery throttles progress writes (default 1s).
	ProgressEvery time.Duration
	// SmallInstallBytes is the size limit of the small install lane (default 2 GiB).
	SmallInstallBytes int64

	mu      sync.Mutex
	running map[int64]context.CancelCauseFunc
}

func (m *Manager) slice() time.Duration {
	if m.SliceDuration > 0 {
		return m.SliceDuration
	}
	return 8 * time.Minute
}

// RegisterJobs registers the install jobs (both lanes) on the queue.
func (m *Manager) RegisterJobs(q *queue.Queue) {
	for _, t := range []string{InstallJobType, InstallSmallJobType} {
		q.Register(t, queue.Handler{
			Concurrency: 1,
			MaxAttempts: installMaxAttempts,
			Lease:       m.slice() + 2*time.Minute,
			Backoff: func(attempt int) time.Duration {
				return min(5*time.Second<<min(attempt-1, 6), 5*time.Minute)
			},
			Run: m.runInstall,
		})
	}
}

// installLane picks the job type for an install with total bytes still to download.
func (m *Manager) installLane(total int64) string {
	limit := m.SmallInstallBytes
	if limit <= 0 {
		limit = defaultSmallInstallBytes
	}
	if total <= limit {
		return InstallSmallJobType
	}
	return InstallJobType
}

func (m *Manager) hardware(ctx context.Context) engine.Hardware {
	if m.Detector == nil {
		return engine.Hardware{Flavour: engine.FlavourCPU}
	}
	return m.Detector.Hardware(ctx, false)
}

// InstallRequest asks for a model: a catalog entry, or a Hugging Face repo + file.
type InstallRequest struct {
	CatalogID string `json:"catalog_id,omitempty"`

	Repo     string `json:"repo,omitempty"`
	File     string `json:"file,omitempty"`
	Revision string `json:"revision,omitempty"`
	Kind     string `json:"kind,omitempty"`    // default: guessed from the file
	ID       string `json:"id,omitempty"`      // default: derived from the file name
	Display  string `json:"display,omitempty"` // default: the file name
	// MMProjFile adds a vision projector from the same repo (repo installs).
	MMProjFile     string `json:"mmproj_file,omitempty"`
	ContextDefault int    `json:"context_default,omitempty"`

	// WithMMProj: catalog installs fetch the entry's projector unless this is false.
	WithMMProj *bool `json:"with_mmproj,omitempty"`
	// Assign points these labels at the model once it is installed.
	Assign []string `json:"assign,omitempty"`
	// GPUBackend picks the engine build to fetch with it ("" or "auto" = the first assigned
	// label's setting, else detection).
	GPUBackend string `json:"gpu_backend,omitempty"`
}

// RequestError is a client mistake (HTTP 4xx).
type RequestError struct {
	Status int
	Msg    string
}

func (e *RequestError) Error() string { return e.Msg }

func badRequest(format string, a ...any) error {
	return &RequestError{Status: http.StatusUnprocessableEntity, Msg: fmt.Sprintf(format, a...)}
}

// modelFromFiles builds a model row for files of a repo.
func (m *Manager) modelRow(id, kind, display, catalogID, repo, rev string, files []File) Model {
	dir := filepath.Join(m.ModelsDir, repoDir(repo))
	var size int64
	for _, f := range files {
		size += f.Size
	}
	return Model{ID: id, Kind: kind, Display: display, CatalogID: catalogID, Repo: repo, Revision: rev, Files: files,
		Path: filepath.Join(dir, filepath.FromSlash(fileRel(files[0].Name))), Size: size, State: StateDownloading}
}

// entryRow builds a model row for a catalog entry: from Hugging Face, or from a direct URL
// (in-binary voice models), possibly an archive extracted into a directory.
func (m *Manager) entryRow(e Entry) Model {
	if e.URL == "" {
		return m.modelRow(e.ID, e.Kind, e.Display, e.ID, e.Repo, e.Revision, []File{{e.File, e.Size, e.SHA256}})
	}
	dir := filepath.Join(m.ModelsDir, e.ID)
	mod := Model{ID: e.ID, Kind: e.Kind, Display: e.Display, CatalogID: e.ID, SourceURL: e.URL, Archive: e.Archive,
		Files: []File{{e.File, e.Size, e.SHA256}}, Path: filepath.Join(dir, e.File), Size: e.Size, State: StateDownloading}
	if e.Archive != "" {
		mod.Path = dir
	}
	return mod
}

// modelRoot is the directory a downloaded model's files live under.
func (m *Manager) modelRoot(mod Model) string {
	if mod.SourceURL != "" {
		return filepath.Join(m.ModelsDir, mod.ID)
	}
	if m.usesLegacyLayout(mod) {
		return filepath.Join(m.ModelsDir, legacyRepoDir(mod.Repo))
	}
	return filepath.Join(m.ModelsDir, repoDir(mod.Repo))
}

// plan turns a request into model rows (the model and optional projector).
func (m *Manager) plan(ctx context.Context, req InstallRequest) (Model, *Model, error) {
	if req.CatalogID != "" {
		e, ok := CatalogEntry(req.CatalogID)
		if !ok {
			return Model{}, nil, &RequestError{Status: http.StatusNotFound, Msg: "unknown catalog id " + req.CatalogID}
		}
		mod := m.entryRow(e)
		mod.ContextDefault, mod.PromptProvider = e.ContextDefault, e.PromptProvider
		if req.ContextDefault > 0 {
			mod.ContextDefault = req.ContextDefault
		}
		var proj *Model
		if e.MMProj != "" && (req.WithMMProj == nil || *req.WithMMProj) {
			p, ok := CatalogEntry(e.MMProj)
			if !ok {
				return Model{}, nil, fmt.Errorf("catalog: %s names missing projector %s", e.ID, e.MMProj)
			}
			pm := m.entryRow(p)
			proj = &pm
			mod.MMProjID = p.ID
		}
		return mod, proj, nil
	}
	if req.Repo == "" || req.File == "" {
		return Model{}, nil, badRequest("give catalog_id, or repo and file")
	}
	if !ValidRepo(req.Repo) {
		return Model{}, nil, badRequest("invalid repo %q (want owner/name)", req.Repo)
	}
	r, err := m.HF.Repo(ctx, req.Repo, req.Revision)
	if errors.Is(err, ErrRepoNotFound) {
		return Model{}, nil, &RequestError{Status: http.StatusNotFound, Msg: err.Error()}
	}
	if errors.Is(err, ErrGated) {
		return Model{}, nil, &RequestError{Status: http.StatusForbidden, Msg: err.Error()}
	}
	if err != nil {
		return Model{}, nil, &RequestError{Status: http.StatusBadGateway, Msg: err.Error()}
	}
	mk := func(file, kind, id, display string) (Model, error) {
		shards, err := shardSet(r, file)
		if err != nil {
			return Model{}, badRequest("%v", err)
		}
		if kind == "" {
			kind = engine.ModelLLM
			for _, c := range Choices(r) {
				if c.File == shards[0].Name {
					kind = c.Kind
				}
			}
		}
		if !ValidKind(kind) {
			return Model{}, badRequest("invalid kind %q", kind)
		}
		if id == "" {
			stem := Slug(path.Base(shardRe.ReplaceAllString(shards[0].Name, "$1")))
			id = stem
			if strings.HasPrefix(stem, "mmproj") || strings.HasPrefix(stem, "model") {
				id = Slug(strings.TrimSuffix(strings.TrimSuffix(path.Base(req.Repo), "-GGUF"), "-gguf")) + "-" + stem
			}
			if ex, err := m.Store.Get(ctx, id); err == nil && (ex.Repo != req.Repo || ex.Files[0].Name != shards[0].Name) {
				id = Slug(strings.SplitN(req.Repo, "/", 2)[0]) + "-" + id
			}
		}
		if !ValidID(id) {
			return Model{}, badRequest("invalid id %q", id)
		}
		if display == "" {
			display = path.Base(shards[0].Name)
		}
		files := make([]File, len(shards))
		for i, s := range shards {
			files[i] = File{Name: s.Name, Size: s.Size, SHA256: s.SHA256}
		}
		return m.modelRow(id, kind, display, "", req.Repo, r.Revision, files), nil
	}
	mod, err := mk(req.File, req.Kind, req.ID, req.Display)
	if err != nil {
		return Model{}, nil, err
	}
	mod.ContextDefault = req.ContextDefault
	var proj *Model
	if req.MMProjFile != "" {
		p, err := mk(req.MMProjFile, engine.ModelMMProj, "", "")
		if err != nil {
			return Model{}, nil, err
		}
		proj = &p
		mod.MMProjID = p.ID
	}
	return mod, proj, nil
}

// flavourFor picks the engine build an install fetches, or "" with a note when the platform
// has none for that kind.
func (m *Manager) flavourFor(ctx context.Context, kind engine.Kind, req InstallRequest) (engine.Flavour, string, error) {
	f, err := engine.ParseFlavour(req.GPUBackend)
	if err != nil {
		return "", "", badRequest("%v", err)
	}
	if f == "" && len(req.Assign) > 0 && m.Settings != nil {
		if d, ok := engine.LabelDefFor(req.Assign[0]); ok {
			f, _ = engine.ParseFlavour(m.Settings.String(ctx, d.Prefix+".gpu_backend", settings.Scope{}))
		}
	}
	if f == "" {
		f = m.hardware(ctx).Flavour
	}
	if f == "" {
		f = engine.FlavourCPU
	}
	plat := m.Binaries.Platform
	if _, err := engine.AssetsFor(kind, plat, f); err != nil {
		if _, err2 := engine.AssetsFor(kind, plat, engine.FlavourCPU); err2 == nil && req.GPUBackend == "" {
			return engine.FlavourCPU, fmt.Sprintf("no %s %s build for %s; fetching the CPU build", f, kind, plat), nil
		}
		return "", fmt.Sprintf("no downloadable %s build for %s: install one and set %s", kind, plat, engine.PathKey(kind)), nil
	}
	return f, "", nil
}

// Install validates a request, records the model rows and queues the install. A model that
// is already installing returns that install (existing = true).
func (m *Manager) Install(ctx context.Context, req InstallRequest) (inst Install, existing bool, err error) {
	for _, l := range req.Assign {
		if _, ok := lookupLabel(l); !ok {
			return Install{}, false, badRequest("unknown label %q", l)
		}
	}
	mod, proj, err := m.plan(ctx, req)
	if err != nil {
		return Install{}, false, err
	}
	for _, l := range req.Assign {
		d, _ := lookupLabel(l)
		if d.ModelKind != mod.Kind {
			return Install{}, false, badRequest("label %s takes %s models, not %s", l, d.ModelKind, mod.Kind)
		}
	}
	// An explicit engine choice applies to the labels being assigned too; otherwise they keep
	// their own (auto-detected) backend and would fetch and run a different build.
	if f, _ := engine.ParseFlavour(req.GPUBackend); f != "" && m.Settings != nil {
		for _, l := range req.Assign {
			if d, _ := lookupLabel(l); !d.Voice {
				if err := m.Settings.Set(ctx, d.Prefix+".gpu_backend", string(f), settings.Scope{}); err != nil {
					return Install{}, false, err
				}
			}
		}
	}
	if a, ok, err := m.Store.ActiveInstall(ctx, mod.ID); err != nil {
		return Install{}, false, err
	} else if ok {
		return a, true, nil
	}
	var total int64
	for _, row := range []*Model{&mod, proj} {
		if row == nil {
			continue
		}
		if ex, err := m.Store.Get(ctx, row.ID); err == nil && ex.State == StateReady && pathExists(ex.Path) {
			*row = ex
			if row == &mod && mod.MMProjID == "" && proj != nil {
				mod.MMProjID = proj.ID
				if err := m.Store.Upsert(ctx, mod); err != nil {
					return Install{}, false, err
				}
			}
			continue
		} else if err != nil && !errors.Is(err, ErrNotFound) {
			return Install{}, false, err
		} else if err == nil && ex.External {
			return Install{}, false, &RequestError{Status: http.StatusConflict, Msg: "id " + row.ID + " is a registered local file"}
		}
		total += row.Size
		if err := m.Store.Upsert(ctx, *row); err != nil {
			return Install{}, false, err
		}
	}
	ek, hasEngine := EngineKind(mod.Kind)
	var fl engine.Flavour
	var note string
	if hasEngine {
		fl, note, err = m.flavourFor(ctx, ek, req)
		if err != nil {
			return Install{}, false, err
		}
	}
	if fl != "" {
		if _, ok := m.Binaries.Path(ek, fl); !ok {
			total += engine.AssetsSize(ek, m.Binaries.Platform, fl)
		}
	}
	inst = Install{ModelID: mod.ID, EngineKind: string(ek), EngineFlavour: string(fl), Assign: req.Assign,
		BytesTotal: total, Note: note}
	if inst.Assign == nil {
		inst.Assign = []string{}
	}
	if proj != nil {
		inst.MMProjID = proj.ID
	}
	id, err := m.Store.CreateInstall(ctx, inst)
	if err != nil {
		return Install{}, false, err
	}
	payload, _ := json.Marshal(map[string]int64{"install_id": id})
	jobID, err := m.Queue.Enqueue(ctx, m.installLane(total), payload, queue.Options{DedupKey: fmt.Sprintf("llm.install:%d", id)})
	if err != nil {
		return Install{}, false, err
	}
	// Only the job id: the worker may already have picked the job up, and a whole-row write
	// here would put a running (or finished) install back to "queued".
	if err := m.Store.SetInstallJob(ctx, id, jobID); err != nil {
		return Install{}, false, err
	}
	inst, err = m.Store.GetInstall(ctx, id)
	if err != nil {
		return Install{}, false, err
	}
	return inst, false, nil
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// extractModel unpacks a directory-shaped model into root, atomically: a half-extracted
// directory never appears at root.
func extractModel(archive, root string) error {
	tmp := root + ".extract"
	os.RemoveAll(tmp)
	if err := engine.Extract(archive, tmp); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.RemoveAll(root); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, root)
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

var errUserCancel = errors.New("cancelled by user")

// runInstall is one slice of an install job.
func (m *Manager) runInstall(ctx context.Context, job queue.Job) ([]byte, error) {
	var p struct {
		InstallID int64 `json:"install_id"`
	}
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, queue.Permanent(err)
	}
	inst, err := m.Store.GetInstall(ctx, p.InstallID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if inst.State != InstallQueued && inst.State != InstallRunning {
		return nil, nil
	}
	sctx, cancel := context.WithCancelCause(ctx)
	sctx, stopSlice := context.WithTimeout(sctx, m.slice())
	defer stopSlice()
	m.mu.Lock()
	if m.running == nil {
		m.running = map[int64]context.CancelCauseFunc{}
	}
	m.running[inst.ID] = cancel
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.running, inst.ID)
		m.mu.Unlock()
		cancel(nil)
	}()

	inst.State, inst.Error, inst.JobID = InstallRunning, "", job.ID
	if err := m.Store.UpdateInstall(ctx, inst); err != nil {
		return nil, err
	}
	err = m.doInstall(sctx, &inst)
	switch {
	case err == nil:
		return nil, m.finish(ctx, &inst)
	case errors.Is(context.Cause(sctx), errUserCancel):
		m.cleanupCancelled(context.WithoutCancel(ctx), inst)
		return nil, nil
	case ctx.Err() != nil:
		// Shutdown or lease: the queue runs this job again and the downloads resume.
		return nil, ctx.Err()
	case errors.Is(sctx.Err(), context.DeadlineExceeded):
		// Slice over: continue in a fresh job (and a fresh lease).
		payload, _ := json.Marshal(map[string]int64{"install_id": inst.ID})
		lane := job.Type
		if lane == "" {
			lane = InstallJobType
		}
		id, qerr := m.Queue.Enqueue(ctx, lane, payload, queue.Options{})
		if qerr != nil {
			return nil, qerr
		}
		inst.JobID = id
		return nil, m.Store.UpdateInstall(ctx, inst)
	}
	permanent := false
	var he *engine.HTTPError
	if errors.As(err, &he) && he.Permanent() {
		permanent = true
	}
	if permanent || job.Attempt >= installMaxAttempts {
		m.fail(ctx, &inst, err)
		return nil, queue.Permanent(err)
	}
	inst.Error = fmt.Sprintf("attempt %d: %v (retrying)", job.Attempt, err)
	_ = m.Store.UpdateInstall(ctx, inst)
	return nil, err
}

// doInstall fetches the engine, then the projector, then the model, skipping what's done.
func (m *Manager) doInstall(ctx context.Context, inst *Install) error {
	every := m.ProgressEvery
	if every <= 0 {
		every = time.Second
	}
	var mu sync.Mutex
	var last time.Time
	var base int64
	report := func(cur int64, force bool) {
		mu.Lock()
		defer mu.Unlock()
		inst.BytesDone = base + cur
		if !force && time.Since(last) < every {
			return
		}
		last = time.Now()
		_ = m.Store.UpdateInstall(context.WithoutCancel(ctx), *inst)
	}
	setPhase := func(p string) {
		inst.Phase = p
		report(0, true)
	}

	ek, fl := engine.Kind(inst.EngineKind), engine.Flavour(inst.EngineFlavour)
	if fl != "" {
		if _, ok := m.Binaries.Path(ek, fl); !ok {
			setPhase("engine")
			if _, err := m.Binaries.Fetch(ctx, ek, fl, func(done, _ int64) { report(done, false) }); err != nil {
				return err
			}
			base += engine.AssetsSize(ek, m.Binaries.Platform, fl)
		}
	}
	for _, ph := range []struct{ phase, id string }{{"mmproj", inst.MMProjID}, {"model", inst.ModelID}} {
		if ph.id == "" {
			continue
		}
		mod, err := m.Store.Get(ctx, ph.id)
		if err != nil {
			return fmt.Errorf("model %s: %w", ph.id, err)
		}
		if mod.State == StateReady && pathExists(mod.Path) {
			continue
		}
		setPhase(ph.phase)
		root := m.modelRoot(mod)
		partial := filepath.Join(m.ModelsDir, ".partial", mod.ID)
		var modDone int64
		for _, f := range mod.Files {
			dest := m.filePath(mod, f.Name)
			url := m.HF.FileURL(mod.Repo, mod.Revision, f.Name)
			var header http.Header = m.HF.AuthHeader()
			if mod.SourceURL != "" {
				url, header = mod.SourceURL, nil
			}
			if mod.Archive != "" {
				// The archive is downloaded beside the partials, then extracted into root.
				dest = filepath.Join(partial, f.Name)
			}
			if st, err := os.Stat(dest); err == nil && (f.Size == 0 || st.Size() == f.Size) {
				modDone += f.Size
				continue
			}
			start := modDone
			err := engine.Download{
				URL: url, Dest: dest,
				Part: filepath.Join(partial, filepath.FromSlash(f.Name)+".part"),
				Size: f.Size, SHA256: f.SHA256, Header: header, Client: m.Client,
				Progress: func(n int64) {
					report(start+n, false)
					_ = m.Store.SetModelState(context.WithoutCancel(ctx), mod.ID, StateDownloading, start+n, "")
				},
			}.Run(ctx)
			if err != nil {
				return fmt.Errorf("%s: %w", f.Name, err)
			}
			modDone += f.Size
		}
		if mod.Archive != "" {
			if err := extractModel(filepath.Join(partial, mod.Files[0].Name), root); err != nil {
				return err
			}
		}
		if err := m.Store.SetModelState(ctx, mod.ID, StateReady, mod.Size, ""); err != nil {
			return err
		}
		os.RemoveAll(filepath.Join(m.ModelsDir, ".partial", mod.ID))
		base += mod.Size
		report(0, true)
	}
	return nil
}

func (m *Manager) finish(ctx context.Context, inst *Install) error {
	inst.State, inst.Phase, inst.BytesDone, inst.Error = InstallDone, "done", inst.BytesTotal, ""
	for _, l := range inst.Assign {
		if err := m.Assign(ctx, l, inst.ModelID); err != nil {
			inst.Note = strings.TrimSpace(inst.Note + " assign " + l + ": " + err.Error())
		}
	}
	if err := m.Store.UpdateInstall(ctx, *inst); err != nil {
		return err
	}
	if m.Labels != nil {
		m.Labels.Notify()
	}
	m.log().Info("model installed", "model", inst.ModelID, "mmproj", inst.MMProjID, "engine", inst.EngineFlavour)
	return nil
}

func (m *Manager) fail(ctx context.Context, inst *Install, err error) {
	// Model rows first, the install last: pollers watch the install, and must not see it
	// failed while its model still reads "downloading".
	for _, id := range []string{inst.MMProjID, inst.ModelID} {
		if mod, gerr := m.Store.Get(ctx, id); gerr == nil && mod.State != StateReady {
			_ = m.Store.SetModelState(ctx, id, StateFailed, mod.BytesDone, err.Error())
		}
	}
	inst.State, inst.Error = InstallFailed, err.Error()
	_ = m.Store.UpdateInstall(ctx, *inst)
	m.log().Warn("model install failed", "model", inst.ModelID, "err", err)
}

func (m *Manager) cleanupCancelled(ctx context.Context, inst Install) {
	for _, id := range []string{inst.MMProjID, inst.ModelID} {
		if id == "" {
			continue
		}
		if mod, err := m.Store.Get(ctx, id); err == nil && mod.State != StateReady {
			_ = m.Store.Delete(ctx, id)
			os.RemoveAll(filepath.Join(m.ModelsDir, ".partial", id))
		}
	}
}

// Cancel stops an install: a queued job is dropped, a running one is interrupted. Partial
// downloads of models that never finished are deleted.
func (m *Manager) Cancel(ctx context.Context, id int64) (Install, error) {
	inst, err := m.Store.GetInstall(ctx, id)
	if err != nil {
		return inst, err
	}
	changed, err := m.Store.SetInstallState(ctx, id, InstallCancelled, "")
	if err != nil {
		return inst, err
	}
	if !changed {
		return inst, &RequestError{Status: http.StatusConflict, Msg: "install is already " + inst.State}
	}
	m.mu.Lock()
	cancel := m.running[id]
	m.mu.Unlock()
	if cancel != nil {
		cancel(errUserCancel)
	} else {
		if inst.JobID != 0 {
			_, _ = m.Queue.Cancel(ctx, inst.JobID)
		}
		m.cleanupCancelled(ctx, inst)
	}
	return m.Store.GetInstall(ctx, id)
}

// labelsUsing lists the labels whose model or mmproj setting names id.
func (m *Manager) labelsUsing(ctx context.Context, id string) []string {
	var out []string
	for _, d := range allLabels() {
		for _, f := range []string{"model", "mmproj"} {
			if _, ok := m.Settings.Definition(d.Prefix + "." + f); !ok {
				continue
			}
			if strings.TrimSpace(m.Settings.String(ctx, d.Prefix+"."+f, settings.Scope{})) == id {
				out = append(out, d.Name)
			}
		}
	}
	return slices.Compact(out)
}

// Delete removes an installed model and its files (a registered file stays on disk). A
// model a label uses is refused unless force, which also clears those labels.
func (m *Manager) Delete(ctx context.Context, id string, force bool) error {
	mod, err := m.Store.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, active, err := m.Store.ActiveInstall(ctx, id); err != nil {
		return err
	} else if active {
		return &RequestError{Status: http.StatusConflict, Msg: "model is installing; cancel the install first"}
	}
	if used := m.labelsUsing(ctx, id); len(used) > 0 {
		if !force {
			return &RequestError{Status: http.StatusConflict, Msg: "model is assigned to " + strings.Join(used, ", ") + " (use force=true)"}
		}
		for _, l := range used {
			d, _ := lookupLabel(l)
			for _, f := range []string{"model", "mmproj"} {
				key := d.Prefix + "." + f
				if _, ok := m.Settings.Definition(key); ok && m.Settings.String(ctx, key, settings.Scope{}) == id {
					if err := m.Settings.Set(ctx, key, "", settings.Scope{}); err != nil {
						return err
					}
				}
			}
		}
	}
	if !mod.External && mod.Archive != "" {
		os.RemoveAll(m.modelRoot(mod))
	} else if !mod.External && (mod.Repo != "" || mod.SourceURL != "") {
		root := m.modelRoot(mod)
		for _, f := range mod.Files {
			p := m.filePath(mod, f.Name)
			if strings.HasPrefix(p, root+string(filepath.Separator)) {
				os.Remove(p)
			}
		}
		removeEmptyDirs(root)
	}
	os.RemoveAll(filepath.Join(m.ModelsDir, ".partial", id))
	if err := m.Store.Delete(ctx, id); err != nil {
		return err
	}
	if m.Labels != nil {
		m.Labels.Notify()
	}
	return nil
}

func removeEmptyDirs(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			removeEmptyDirs(filepath.Join(root, e.Name()))
		}
	}
	os.Remove(root) // only succeeds when empty
}

// RegisterRequest records a model file already on disk (05 §4: register in place, no copy).
type RegisterRequest struct {
	Path           string `json:"path"`
	Kind           string `json:"kind"`
	ID             string `json:"id,omitempty"`
	Display        string `json:"display,omitempty"`
	MMProjID       string `json:"mmproj_id,omitempty"`
	ContextDefault int    `json:"context_default,omitempty"`
	// CatalogID says the file is that catalog entry's (a copy downloaded elsewhere): its size
	// and sha256 must match, and the model then gets the entry's prompt provider, pinned chat
	// template and fold flag like a catalog install.
	CatalogID string `json:"catalog_id,omitempty"`
}

// fileSHA256 hashes a file, stopping when ctx ends.
func fileSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 4<<20)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		h.Write(buf[:n])
		if errors.Is(err, io.EOF) {
			return hex.EncodeToString(h.Sum(nil)), nil
		}
		if err != nil {
			return "", err
		}
	}
}

// Register adds an existing local file as a ready model.
func (m *Manager) Register(ctx context.Context, req RegisterRequest) (Model, error) {
	if !filepath.IsAbs(req.Path) {
		return Model{}, badRequest("path must be absolute")
	}
	st, err := os.Stat(req.Path)
	if err != nil || st.IsDir() {
		return Model{}, badRequest("no file at %s", req.Path)
	}
	var entry Entry
	if req.CatalogID != "" {
		var ok bool
		if entry, ok = CatalogEntry(req.CatalogID); !ok {
			return Model{}, badRequest("unknown catalog entry %q", req.CatalogID)
		}
		if entry.Archive != "" {
			return Model{}, badRequest("catalog entry %s is an archive; install it instead", entry.ID)
		}
		if req.Kind == "" {
			req.Kind = entry.Kind
		}
		if req.Kind != entry.Kind {
			return Model{}, badRequest("catalog entry %s is a %s model, not %s", entry.ID, entry.Kind, req.Kind)
		}
		if st.Size() != entry.Size {
			return Model{}, badRequest("%s is %d bytes; catalog entry %s is %d", req.Path, st.Size(), entry.ID, entry.Size)
		}
		sum, err := fileSHA256(ctx, req.Path)
		if err != nil {
			return Model{}, err
		}
		if !strings.EqualFold(sum, entry.SHA256) {
			return Model{}, badRequest("%s has sha256 %s; catalog entry %s pins %s", req.Path, sum, entry.ID, entry.SHA256)
		}
		if req.Display == "" {
			req.Display = entry.Display
		}
		if req.ContextDefault == 0 {
			req.ContextDefault = entry.ContextDefault
		}
	}
	if req.Kind == "" {
		req.Kind = engine.ModelLLM
	}
	if !ValidKind(req.Kind) {
		return Model{}, badRequest("invalid kind %q", req.Kind)
	}
	if req.ID == "" {
		req.ID = Slug(filepath.Base(req.Path))
	}
	if !ValidID(req.ID) {
		return Model{}, badRequest("invalid id %q", req.ID)
	}
	if _, err := m.Store.Get(ctx, req.ID); err == nil {
		return Model{}, &RequestError{Status: http.StatusConflict, Msg: "id " + req.ID + " exists"}
	}
	if req.MMProjID != "" {
		if p, err := m.Store.Get(ctx, req.MMProjID); err != nil || p.Kind != engine.ModelMMProj {
			return Model{}, badRequest("mmproj_id %s is not an installed projector", req.MMProjID)
		}
	}
	if req.Display == "" {
		req.Display = filepath.Base(req.Path)
	}
	mod := Model{ID: req.ID, Kind: req.Kind, Display: req.Display, Files: []File{{Name: filepath.Base(req.Path), Size: st.Size()}},
		Path: req.Path, Size: st.Size(), MMProjID: req.MMProjID, ContextDefault: req.ContextDefault, State: StateReady,
		BytesDone: st.Size(), External: true}
	if entry.ID != "" {
		mod.CatalogID, mod.Repo, mod.Revision, mod.PromptProvider = entry.ID, entry.Repo, entry.Revision, entry.PromptProvider
	}
	if err := m.Store.Upsert(ctx, mod); err != nil {
		return Model{}, err
	}
	return m.Store.Get(ctx, req.ID)
}

// Assign points a label at an installed model (engine labels also get engine=local; voice
// labels just record the model). When the label's devices are
// unset, the detected placement is written, so settings show the effective assignment.
func (m *Manager) Assign(ctx context.Context, label, modelID string) error {
	d, ok := lookupLabel(label)
	if !ok {
		return badRequest("unknown label %q", label)
	}
	mod, err := m.Store.Get(ctx, modelID)
	if err != nil {
		return badRequest("model %s is not installed", modelID)
	}
	if mod.Kind != d.ModelKind {
		return badRequest("label %s takes %s models, not %s", label, d.ModelKind, mod.Kind)
	}
	sc := settings.Scope{}
	if err := m.Settings.Set(ctx, d.Prefix+".model", modelID, sc); err != nil {
		return err
	}
	if d.Voice {
		return nil // in-binary: nothing to run
	}
	if err := m.Settings.Set(ctx, d.Prefix+".engine", engine.ModeLocal, sc); err != nil {
		return err
	}
	if d.ModelKind != engine.ModelEmbedding && m.Settings.String(ctx, d.Prefix+".gpu_devices", sc) == "" {
		f, _ := engine.ParseFlavour(m.Settings.String(ctx, d.Prefix+".gpu_backend", sc))
		hw := m.hardware(ctx)
		if f == "" || f == hw.Flavour {
			if p := engine.Propose(hw)[label]; p.Devices != "" {
				if err := m.Settings.Set(ctx, d.Prefix+".gpu_devices", p.Devices, sc); err != nil {
					return err
				}
			}
		}
	}
	if m.Labels != nil {
		m.Labels.Notify()
	}
	return nil
}

// UpdateLabels validates and writes label settings: {label: {field: value}}. Field names are
// the per-label keys without the prefix (model, mmproj, context, engine, gpu_backend, …).
func (m *Manager) UpdateLabels(ctx context.Context, upd map[string]map[string]any) error {
	type write struct {
		key string
		val any
	}
	var writes []write
	for label, fields := range upd {
		d, ok := lookupLabel(label)
		if !ok {
			return badRequest("unknown label %q", label)
		}
		for f, v := range fields {
			key := d.Prefix + "." + f
			def, ok := m.Settings.Definition(key)
			if !ok {
				return badRequest("label %s has no setting %q", label, f)
			}
			if err := m.validateField(ctx, d, f, def, v); err != nil {
				return err
			}
			writes = append(writes, write{key, v})
		}
	}
	for _, w := range writes {
		if err := m.Settings.Set(ctx, w.key, w.val, settings.Scope{}); err != nil {
			return err
		}
	}
	if m.Labels != nil {
		m.Labels.Notify()
	}
	return nil
}

func (m *Manager) validateField(ctx context.Context, d labelRef, f string, def settings.Definition, v any) error {
	switch def.Type {
	case settings.Int:
		n, ok := v.(float64)
		if !ok || n != float64(int64(n)) {
			return badRequest("%s.%s must be an integer", d.Name, f)
		}
		if n < 0 {
			return badRequest("%s.%s must not be negative", d.Name, f)
		}
		return nil
	case settings.Bool:
		if _, ok := v.(bool); !ok {
			return badRequest("%s.%s must be true or false", d.Name, f)
		}
		return nil
	}
	s, ok := v.(string)
	if !ok {
		return badRequest("%s.%s must be a string", d.Name, f)
	}
	if len(def.Options) > 0 && !slices.Contains(def.Options, any(s)) {
		return badRequest("%s.%s must be one of %v", d.Name, f, def.Options)
	}
	switch f {
	case "model", "mmproj":
		if s == "" || (f == "mmproj" && strings.EqualFold(s, "none")) {
			return nil
		}
		if filepath.IsAbs(s) {
			if !fileExists(s) {
				return badRequest("%s.%s: no file at %s", d.Name, f, s)
			}
			return nil
		}
		mod, err := m.Store.Get(ctx, s)
		if err != nil {
			return badRequest("%s.%s: model %s is not installed", d.Name, f, s)
		}
		want := d.ModelKind
		if f == "mmproj" {
			want = engine.ModelMMProj
		}
		if mod.Kind != want {
			return badRequest("%s.%s: %s is a %s model, not %s", d.Name, f, s, mod.Kind, want)
		}
	case "gpu_backend":
		if _, err := engine.ParseFlavour(s); err != nil {
			return badRequest("%s.%s: %v", d.Name, f, err)
		}
	case "extra_args":
		if _, err := engine.SplitArgs(s); err != nil {
			return badRequest("%s.%s: %v", d.Name, f, err)
		}
	case "split_mode":
		if s != "" && s != "none" && s != "layer" && s != "row" {
			return badRequest("%s.%s must be none, layer or row", d.Name, f)
		}
	}
	return nil
}

func (m *Manager) log() *slog.Logger {
	if m.Log == nil {
		return slog.Default()
	}
	return m.Log
}

// Recommend suggests a catalog entry per label for the detected hardware: the largest LLM
// that fits one card (else one that fits tightly), MiniLM for embeddings, and whisper large-v3-turbo on a GPU (small.en on
// CPU). On a CPU-only box the LLM pick is the smallest model, and a remote endpoint is worth
// considering.
func Recommend(hw engine.Hardware) map[string]string {
	out := map[string]string{engine.LabelEmbeddings: "all-minilm-l6-v2"}
	for _, v := range VoiceLabels {
		out[v.Name] = v.DefaultModel
	}
	var best Entry
	for _, verdict := range []string{"fits", "tight"} {
		for _, e := range Catalog() {
			if e.Kind == engine.ModelLLM && EntryFit(hw, e).Verdict == verdict && e.Size > best.Size {
				best = e
			}
		}
		if best.ID != "" {
			break
		}
	}
	if best.ID == "" {
		best, _ = CatalogEntry("qwen3-4b")
	}
	out[engine.LabelLive], out[engine.LabelBackground] = best.ID, best.ID
	out[engine.LabelSTT] = "whisper-small.en"
	if hw.Flavour != engine.FlavourCPU && hw.Flavour != "" {
		// STT shares a card with the live model: large-v3-turbo only when it fits next to it.
		live := []Resident{{Labels: []string{engine.LabelLive}, Model: best.ID, NeededMB: EntryFit(hw, best).NeededMB,
			Devices: deviceList(engine.Propose(hw)[engine.LabelLive].Devices)}}
		if e, ok := CatalogEntry("whisper-large-v3-turbo"); ok {
			if FitAlongside(hw, e.Kind, e.Size, 0, 0, live).Verdict == "fits" {
				out[engine.LabelSTT] = e.ID
			}
		}
	}
	return out
}

// deviceList parses a gpu_devices setting ("0,1"); empty or invalid is nil (the default card).
func deviceList(s string) []int {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil
		}
		out = append(out, n)
	}
	return out
}

// Residents lists the GPU memory assigned local engines need, from the current label
// settings. Labels sharing one engine (same model, context and devices, LD1) count once.
// Labels on the CPU (gpu_layers 0 or gpu_backend cpu), remote, shared or off need none.
func (m *Manager) Residents(ctx context.Context) []Resident {
	if m.Labels == nil {
		return nil
	}
	var out []Resident
	idx := map[string]int{}
	for _, ls := range m.Labels.Status(ctx) {
		c := ls.Config
		if c.Engine != engine.ModeLocal || c.Model == "" || c.GPULayers == 0 || c.GPUBackend == string(engine.FlavourCPU) {
			continue
		}
		need, ok := m.residentNeed(ctx, c)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%s|%s|%d|%s", c.Kind, c.Model, c.Context, c.GPUDevices)
		if i, ok := idx[key]; ok {
			out[i].Labels = append(out[i].Labels, c.Label)
			continue
		}
		idx[key] = len(out)
		out = append(out, Resident{Labels: []string{c.Label}, Model: c.Model, NeededMB: need, Devices: deviceList(c.GPUDevices)})
	}
	return append(out, m.otherPrograms(ctx, out)...)
}

// OtherPrograms labels GPU memory jarvisd's engines don't account for: the desktop, a game
// streamer, an emulator. It shows up in fit verdicts and overcommit warnings like an engine.
const OtherPrograms = "other programs"

// otherPrograms estimates, per card, the memory in use at detection time that isn't ours:
// used minus what our engines running then were estimated to need.
func (m *Manager) otherPrograms(ctx context.Context, residents []Resident) []Resident {
	hw := m.hardware(ctx)
	devs := hw.Discrete(hw.Flavour)
	if len(devs) == 0 || hw.Flavour == engine.FlavourMetal {
		return nil // unified memory reports no per-process use
	}
	running := map[string]bool{}
	for _, in := range m.Labels.Instances() {
		// The process's launch time, not the state's: an engine still loading at detection
		// (or that went unhealthy and back since) already held its memory then, and counting
		// it as other programs too would double it (A10 F8).
		launched := in.Started
		if launched.IsZero() {
			launched = in.Since
		}
		switch in.State {
		case engines.Starting, engines.Healthy, engines.Unhealthy, engines.Draining:
			if !launched.After(hw.DetectedAt) {
				for _, l := range in.Labels {
					running[l] = true
				}
			}
		}
	}
	var out []Resident
	for _, d := range devs {
		if d.FreeMB <= 0 || d.FreeMB > d.TotalMB {
			continue
		}
		used := d.TotalMB - d.FreeMB
		for _, r := range residents {
			if !slices.ContainsFunc(r.Labels, func(l string) bool { return running[l] }) {
				continue
			}
			on := r.Devices
			if len(on) == 0 {
				on = []int{devs[0].Index}
			}
			if slices.Contains(on, d.Index) {
				used -= r.NeededMB / int64(len(on))
			}
		}
		if used > 256 { // below this it's noise in our own estimates
			out = append(out, Resident{Labels: []string{OtherPrograms}, NeededMB: used, Devices: []int{d.Index}})
		}
	}
	return out
}

// residentNeed estimates a label's engine in MB: its installed model (plus projector) at the
// label's context, or an external file by its size.
func (m *Manager) residentNeed(ctx context.Context, c engine.LabelConfig) (int64, bool) {
	var weights, kv int64
	n := c.Context
	if mod, err := m.Store.Get(ctx, c.Model); err == nil {
		weights = mod.Size
		proj := c.MMProj
		if proj == "" {
			proj = mod.MMProjID
		}
		if proj != "" && !strings.EqualFold(proj, "none") {
			if p, err := m.Store.Get(ctx, proj); err == nil {
				weights += p.Size
			}
		}
		if e, ok := CatalogEntry(mod.CatalogID); ok {
			kv = e.KVBytesPerTok
			if n <= 0 {
				n = e.ContextDefault
			}
		}
		if n <= 0 {
			n = mod.ContextDefault
		}
	} else if fi, err := os.Stat(c.Model); err == nil {
		weights = fi.Size()
	} else {
		return 0, false
	}
	need, _ := estimateNeed(c.ModelKind, weights, kv, max(n, 0))
	if n <= 0 && c.ModelKind == engine.ModelLLM {
		need, _ = estimateNeed(c.ModelKind, weights, kv, 8192)
	}
	return need / mb, true
}

// without drops labels from the residents (they are being reassigned); a resident left with
// no labels goes.
func without(rs []Resident, labels ...string) []Resident {
	var out []Resident
	for _, r := range rs {
		r.Labels = slices.DeleteFunc(slices.Clone(r.Labels), func(l string) bool { return slices.Contains(labels, l) })
		if len(r.Labels) > 0 {
			out = append(out, r)
		}
	}
	return out
}

// fitFor judges a model of kind for the labels that take that kind: next to the residents
// without those labels (the model would replace what they run, so it is not counted against
// itself or its predecessor), and as "cpu" when every such label runs on the CPU (the
// embeddings label by default), where VRAM doesn't matter (A10 F8).
func (m *Manager) fitFor(ctx context.Context, hw engine.Hardware, kind string, weights, kvPerTok int64, n int, residents []Resident) Fit {
	var labels []string
	cpuOnly := m.Settings != nil
	for _, d := range engine.LabelDefs {
		if d.ModelKind != kind {
			continue
		}
		labels = append(labels, d.Name)
		if cpuOnly && m.Settings.Int(ctx, d.Prefix+".gpu_layers", settings.Scope{}) != 0 &&
			m.Settings.String(ctx, d.Prefix+".gpu_backend", settings.Scope{}) != string(engine.FlavourCPU) {
			cpuOnly = false
		}
	}
	f := FitAlongside(hw, kind, weights, kvPerTok, n, without(residents, labels...))
	if cpuOnly && len(labels) > 0 && f.Verdict != "in_binary" {
		f = Fit{Verdict: "cpu", NeededMB: f.NeededMB, Context: f.Context, KVEstimate: f.KVEstimate}
	}
	return f
}

// entryFit is fitFor for a catalog entry (with its projector) at its default context.
func (m *Manager) entryFit(ctx context.Context, hw engine.Hardware, e Entry, residents []Resident) Fit {
	w := e.Size
	if e.MMProj != "" {
		if p, ok := CatalogEntry(e.MMProj); ok {
			w += p.Size
		}
	}
	return m.fitFor(ctx, hw, e.Kind, w, e.KVBytesPerTok, e.ContextDefault, residents)
}

// FitWarning says when a model being installed for engine labels won't fit on its card next
// to the engines the other labels already run; "" when it fits or nothing is assigned. The
// install still goes ahead: the estimate is a guide, and the user decides.
func (m *Manager) FitWarning(ctx context.Context, modelID string, assign []string) string {
	var engineLabels []string
	for _, l := range assign {
		if d, ok := lookupLabel(l); ok && !d.Voice {
			engineLabels = append(engineLabels, l)
		}
	}
	if len(engineLabels) == 0 || m.Store == nil {
		return ""
	}
	mod, err := m.Store.Get(ctx, modelID)
	if err != nil {
		return ""
	}
	var kv int64
	n := mod.ContextDefault
	if e, ok := CatalogEntry(mod.CatalogID); ok {
		kv = e.KVBytesPerTok
	}
	w := mod.Size
	if mod.MMProjID != "" {
		if p, err := m.Store.Get(ctx, mod.MMProjID); err == nil {
			w += p.Size
		}
	}
	hw := m.hardware(ctx)
	f := FitAlongside(hw, mod.Kind, w, kv, n, without(m.Residents(ctx), engineLabels...))
	if f.Verdict != "too_big" || len(f.Alongside) == 0 {
		return ""
	}
	return fmt.Sprintf("%s needs about %d MB but %s has only %d MB left next to %s; it may fail to start. "+
		"Move a label to another card, lower a context, or pick a smaller model.",
		mod.ID, f.NeededMB, f.Device, f.DeviceMB*95/100-f.CommittedMB, strings.Join(f.Alongside, ", "))
}
