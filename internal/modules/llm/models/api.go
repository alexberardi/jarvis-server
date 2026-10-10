package models

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Stack is the engine and model-manager machinery, wired together. The llm module builds one
// in Register and starts it in Start (see docs/llm/06 "Wiring").
type Stack struct {
	Store    *Store
	Binaries *engine.Binaries
	Detector *engine.Detector
	Resolver *engine.Resolver
	Manager  *Manager
}

// NewStack wires the stack over the llm module's settings service (whose definitions must
// include models.SettingDefinitions()) and registers its queue jobs.
func NewStack(deps module.Deps, set *settings.Service) *Stack {
	log := deps.Log
	str := func(key string) string { return set.String(context.Background(), key, settings.Scope{}) }
	store := &Store{DB: deps.DB}
	bins := engine.NewBinaries(deps.Config.Home, log)
	bins.BaseURL = func(k engine.Kind) string { return str(engine.BaseURLKey(k)) }
	bins.Override = func(k engine.Kind) string { return str(engine.PathKey(k)) }
	det := engine.NewDetector(func() map[engine.Flavour]string { return bins.Installed(engine.KindLlama) })
	det.Log = log
	res := &engine.Resolver{
		Memory:   engine.FileGPUMemory{Path: filepath.Join(deps.Config.Home, "engines", "gpu-placements.json")},
		Source:   engine.SettingsSource{Settings: set, Models: store},
		Binaries: bins,
		Hardware: func(ctx context.Context) engine.Hardware { return det.Hardware(ctx, false) },
		RequestFetch: func(ctx context.Context, k engine.Kind, f engine.Flavour) error {
			_, err := engine.EnqueueFetch(ctx, deps.Queue, k, f)
			return err
		},
		Log:         log,
		TemplateDir: filepath.Join(deps.Config.Home, "templates"),
	}
	hf := &HF{
		Endpoint: func() string { return str(engine.KeyHFEndpoint) },
		Token:    func() string { return str(engine.KeyHFToken) },
	}
	mgr := &Manager{Store: store, Settings: set, Queue: deps.Queue, Binaries: bins, Detector: det, Labels: res,
		HF: hf, ModelsDir: filepath.Join(deps.Config.Home, "models"), Log: log}
	bins.RegisterJobs(deps.Queue, func() {
		det.Hardware(context.Background(), true) // a new build may see more devices
		res.Notify()
	})
	mgr.RegisterJobs(deps.Queue)
	return &Stack{Store: store, Binaries: bins, Detector: det, Resolver: res, Manager: mgr}
}

// Start runs the resolver loop (engines start for configured labels) and the GPU watch, which
// detects at once (logging a driver fault at startup) and re-checks every few minutes.
func (s *Stack) Start(ctx context.Context) error {
	go func() {
		s.Detector.Hardware(ctx, false)
		engine.WatchGPU(ctx, s.Detector, s.Resolver, 0)
	}()
	return s.Resolver.Start(ctx)
}

// Mount serves the model-manager API, every route behind guard (main wires
// settings.SuperuserGuard).
func (s *Stack) Mount(mux *http.ServeMux, guard settings.Guard) {
	(&API{Manager: s.Manager, Resolver: s.Resolver}).Mount(mux, guard)
}

// API is the admin HTTP API (docs/llm/06).
type API struct {
	Manager  *Manager
	Resolver interface {
		Status(ctx context.Context) []engine.LabelStatus
		Instances() []engine.InstanceStatus
	}
}

// Mount registers the routes.
func (a *API) Mount(mux *http.ServeMux, guard settings.Guard) {
	g := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if guard == nil {
				httpx.Error(w, http.StatusForbidden, "Model management is not available")
				return
			}
			if guard(w, r) {
				h(w, r)
			}
		}
	}
	mux.HandleFunc("GET /v1/models/catalog", g(a.catalog))
	mux.HandleFunc("GET /v1/models/hf/{repo...}", g(a.hfRepo))
	mux.HandleFunc("POST /v1/models/install", g(a.install))
	mux.HandleFunc("GET /v1/models/installs", g(a.installs))
	mux.HandleFunc("GET /v1/models/installs/{id}", g(a.getInstall))
	mux.HandleFunc("POST /v1/models/installs/{id}/cancel", g(a.cancelInstall))
	mux.HandleFunc("GET /v1/models/installed", g(a.installed))
	mux.HandleFunc("POST /v1/models/installed", g(a.register))
	mux.HandleFunc("DELETE /v1/models/installed/{id}", g(a.deleteModel))
	mux.HandleFunc("GET /v1/models/labels", g(a.labels))
	mux.HandleFunc("PUT /v1/models/labels", g(a.putLabels))
	mux.HandleFunc("GET /v1/hardware", g(a.hardware))
	mux.HandleFunc("POST /v1/hardware/engines", g(a.fetchEngine))
}

func writeErr(w http.ResponseWriter, err error) {
	var re *RequestError
	switch {
	case errors.As(err, &re):
		httpx.Error(w, re.Status, re.Msg)
	case errors.Is(err, ErrNotFound):
		httpx.Error(w, http.StatusNotFound, "Not found")
	default:
		httpx.Error(w, http.StatusInternalServerError, err.Error())
	}
}

type catalogItem struct {
	Entry
	Fit       Fit    `json:"fit"`
	Installed bool   `json:"installed"`
	State     string `json:"state,omitempty"` // installed row state, if any
}

func (a *API) catalog(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hw := a.Manager.hardware(ctx)
	installed, err := a.Manager.Store.List(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	state := map[string]string{}
	for _, m := range installed {
		state[m.ID] = m.State
	}
	residents := a.Manager.Residents(ctx)
	items := []catalogItem{}
	for _, e := range Catalog() {
		// Judged for the labels of its kind: next to the other engines, not to itself or to
		// the model it would replace.
		items = append(items, catalogItem{Entry: e, Fit: a.Manager.entryFit(ctx, hw, e, residents), Installed: state[e.ID] == StateReady, State: state[e.ID]})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": items, "recommended": a.Manager.Recommend(ctx, hw), "hardware": hw, "residents": residents})
}

type hfChoice struct {
	Choice
	Fit Fit `json:"fit"`
}

func (a *API) hfRepo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	repo, err := a.Manager.HF.Repo(ctx, r.PathValue("repo"), r.URL.Query().Get("revision"))
	switch {
	case errors.Is(err, ErrRepoNotFound):
		httpx.Error(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, ErrGated):
		httpx.Error(w, http.StatusForbidden, err.Error())
		return
	case err != nil && !ValidRepo(r.PathValue("repo")):
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	case err != nil:
		httpx.Error(w, http.StatusBadGateway, err.Error())
		return
	}
	hw := a.Manager.hardware(ctx)
	ctxLen, _ := strconv.Atoi(r.URL.Query().Get("context"))
	residents := a.Manager.Residents(ctx)
	choices := []hfChoice{}
	for _, c := range Choices(repo) {
		choices = append(choices, hfChoice{Choice: c, Fit: a.Manager.fitFor(ctx, hw, c.Kind, c.Size, 0, ctxLen, residents)})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"repo": repo.ID, "revision": repo.Revision, "gated": repo.Gated, "files": choices})
}

func (a *API) install(w http.ResponseWriter, r *http.Request) {
	var req InstallRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	inst, existing, err := a.Manager.Install(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	status := http.StatusAccepted
	if existing {
		status = http.StatusOK
	}
	out := map[string]any{"install": inst, "existing": existing}
	if warn := a.Manager.FitWarning(r.Context(), inst.ModelID, req.Assign); warn != "" {
		out["warning"] = warn
	}
	httpx.WriteJSON(w, status, out)
}

func (a *API) installs(w http.ResponseWriter, r *http.Request) {
	list, err := a.Manager.Store.ListInstalls(r.Context(), 50)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"installs": list})
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "Not found")
		return 0, false
	}
	return id, true
}

func (a *API) getInstall(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	inst, err := a.Manager.Store.GetInstall(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, inst)
}

func (a *API) cancelInstall(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	inst, err := a.Manager.Cancel(r.Context(), id)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, inst)
}

func (a *API) installed(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	list, err := a.Manager.Store.List(ctx)
	if err != nil {
		writeErr(w, err)
		return
	}
	type item struct {
		Model
		Labels []string `json:"labels"`
	}
	out := []item{}
	var total int64
	for _, m := range list {
		l := a.Manager.labelsUsing(ctx, m.ID)
		if l == nil {
			l = []string{}
		}
		out = append(out, item{Model: m, Labels: l})
		if !m.External {
			total += m.BytesDone
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"models": out, "disk_bytes": total, "dir": a.Manager.ModelsDir})
}

func (a *API) register(w http.ResponseWriter, r *http.Request) {
	var req RegisterRequest
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	m, err := a.Manager.Register(r.Context(), req)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, m)
}

func (a *API) deleteModel(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "true"
	if err := a.Manager.Delete(r.Context(), r.PathValue("id"), force); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) labels(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	hw := a.Manager.hardware(ctx)
	labels := a.Resolver.Status(ctx)
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"labels":    labels,
		"gpu_fault": engine.FaultFor(hw, labels),
		"voice":     a.Manager.VoiceStatus(ctx),
		"engines":   a.Resolver.Instances(),
		"proposal":  engine.Propose(hw),
		"recommend": a.Manager.Recommend(ctx, hw),
		"warnings":  Overcommitted(hw, a.Manager.Residents(ctx)),
	})
}

func (a *API) putLabels(w http.ResponseWriter, r *http.Request) {
	var body map[string]map[string]any
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	if err := a.Manager.UpdateLabels(r.Context(), body); err != nil {
		writeErr(w, err)
		return
	}
	a.labels(w, r)
}

func (a *API) hardware(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	refresh := r.URL.Query().Get("refresh") == "true" || r.URL.Query().Get("refresh") == "1"
	hw := engine.Hardware{Flavour: engine.FlavourCPU}
	if a.Manager.Detector != nil {
		hw = a.Manager.Detector.Hardware(ctx, refresh)
	}
	builds := map[string]any{}
	for _, k := range engine.Kinds {
		builds[string(k)] = map[string]any{
			"build":    engine.Releases[k].Build,
			"flavours": engine.FlavoursFor(k, a.Manager.Binaries.Platform),
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"hardware":  hw,
		"gpu_fault": engine.FaultFor(hw, a.Resolver.Status(ctx)),
		"proposal":  engine.Propose(hw),
		"builds":    builds,
		"installed": a.Manager.Binaries.List(),
		"engines":   a.Resolver.Instances(),
		"voice":     a.Manager.VoiceStatus(ctx),
	})
}

func (a *API) fetchEngine(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Kind    string `json:"kind"`
		Flavour string `json:"flavour"`
	}
	if !httpx.DecodeJSON(w, r, &req) {
		return
	}
	k := engine.Kind(req.Kind)
	if k == "" {
		k = engine.KindLlama
	}
	f, err := engine.ParseFlavour(req.Flavour)
	if err == nil && f == "" {
		f = a.Manager.hardware(r.Context()).Flavour
	}
	if err == nil {
		_, err = engine.AssetsFor(k, a.Manager.Binaries.Platform, f)
	}
	if err != nil {
		httpx.Error(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	if p, ok := a.Manager.Binaries.Path(k, f); ok {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"kind": k, "flavour": f, "installed": true, "path": p})
		return
	}
	id, err := engine.EnqueueFetch(r.Context(), a.Manager.Queue, k, f)
	if err != nil {
		writeErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"kind": k, "flavour": f, "installed": false, "job_id": id})
}
