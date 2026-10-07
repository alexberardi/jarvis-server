package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// InstallJobType is the queue job that downloads a model (and its projector and engine).
const InstallJobType = "llm.models.install"

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

	mu      sync.Mutex
	running map[int64]context.CancelCauseFunc
}

func (m *Manager) slice() time.Duration {
	if m.SliceDuration > 0 {
		return m.SliceDuration
	}
	return 8 * time.Minute
}

// RegisterJobs registers the install job on the queue.
func (m *Manager) RegisterJobs(q *queue.Queue) {
	q.Register(InstallJobType, queue.Handler{
		Concurrency: 1,
		MaxAttempts: installMaxAttempts,
		Lease:       m.slice() + 2*time.Minute,
		Backoff: func(attempt int) time.Duration {
			return min(5*time.Second<<min(attempt-1, 6), 5*time.Minute)
		},
		Run: m.runInstall,
	})
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

func repoDir(repo string) string { return strings.ReplaceAll(repo, "/", "--") }

// modelFromFiles builds a model row for files of a repo.
func (m *Manager) modelRow(id, kind, display, catalogID, repo, rev string, files []File) Model {
	dir := filepath.Join(m.ModelsDir, repoDir(repo))
	var size int64
	for _, f := range files {
		size += f.Size
	}
	return Model{ID: id, Kind: kind, Display: display, CatalogID: catalogID, Repo: repo, Revision: rev, Files: files,
		Path: filepath.Join(dir, filepath.FromSlash(files[0].Name)), Size: size, State: StateDownloading}
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
	jobID, err := m.Queue.Enqueue(ctx, InstallJobType, payload, queue.Options{DedupKey: fmt.Sprintf("llm.install:%d", id)})
	if err != nil {
		return Install{}, false, err
	}
	inst, err = m.Store.GetInstall(ctx, id)
	if err != nil {
		return Install{}, false, err
	}
	inst.JobID = jobID
	if err := m.Store.UpdateInstall(ctx, inst); err != nil {
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
		id, qerr := m.Queue.Enqueue(ctx, InstallJobType, payload, queue.Options{})
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
			dest := filepath.Join(root, filepath.FromSlash(f.Name))
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
	inst.State, inst.Error = InstallFailed, err.Error()
	_ = m.Store.UpdateInstall(ctx, *inst)
	for _, id := range []string{inst.MMProjID, inst.ModelID} {
		if mod, gerr := m.Store.Get(ctx, id); gerr == nil && mod.State != StateReady {
			_ = m.Store.SetModelState(ctx, id, StateFailed, mod.BytesDone, err.Error())
		}
	}
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
			p := filepath.Join(root, filepath.FromSlash(f.Name))
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
		if e, ok := CatalogEntry("whisper-large-v3-turbo"); ok && EntryFit(hw, e).Verdict == "fits" {
			out[engine.LabelSTT] = e.ID
		}
	}
	return out
}
