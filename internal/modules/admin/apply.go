package admin

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// The restart button (AD8) and the one-click signed update (AD5). Both end with jarvisd
// exiting for its supervisor to start it again; unsupervised they answer 409 with the
// command to run instead.

// Restarter ends serve so the supervisor restarts jarvisd (service.Restarter).
type Restarter interface {
	Kind() service.Kind
	Request() error
}

// UpgradeConfig is what the one-click update needs from cmd/jarvisd.
type UpgradeConfig struct {
	// Exe is the running executable, symlinks resolved (the path the service runs).
	Exe string
	// Helper is set under the systemd system unit, whose privileged ExecStartPre swaps a
	// staged release in: the service account itself can't write the binary.
	Helper bool
	// Source reads releases (JARVIS_UPDATE_API in the CI upgrade job).
	Source update.Source
	// Keys replace the embedded trusted keys (tests).
	Keys []update.PublicKey
	// SkipVersionCheck skips running the new binary (tests).
	SkipVersionCheck bool
}

// restartDelay lets the 202 reach the browser before serve shuts down.
var restartDelay = 300 * time.Millisecond

func (m *Module) supervisor() service.Kind {
	if m.Restarter == nil {
		return service.None
	}
	return m.Restarter.Kind()
}

// handleRestart is POST /api/system/restart: 202 and a supervised restart, or 409 with the
// command when nothing would start jarvisd again.
func (m *Module) handleRestart(w http.ResponseWriter, r *http.Request) {
	kind := m.supervisor()
	if !kind.Supervised() {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"detail": "jarvisd isn't running under a service manager, so it can't restart itself. " +
				"Restart it yourself, or install it as a service (jarvisd service install).",
			"supervisor": kind,
			"command":    service.RestartCommand(kind, false),
		})
		return
	}
	m.deps.Log.Info("admin: restart requested", "supervisor", kind)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"restarting": true, "status": "restarting", "supervisor": kind})
	go func() {
		time.Sleep(restartDelay)
		if err := m.Restarter.Request(); err != nil {
			m.deps.Log.Error("admin: restart failed", "err", err)
		}
	}()
}

// applyJob is the one in-flight (or last) update run in this process.
type applyJob struct {
	State     string  `json:"state"` // running | restarting | failed
	Step      string  `json:"step"`
	Done      int64   `json:"bytes_done"`
	Total     int64   `json:"bytes_total"`
	From      string  `json:"from_version"`
	To        *string `json:"to_version"`
	Error     *string `json:"error"`
	StartedAt string  `json:"started_at"`
}

type applyState struct {
	mu  sync.Mutex
	job *applyJob
}

func (m *Module) paths() update.Paths {
	return update.Paths{Home: m.deps.Config.Home, Exe: m.Upgrade.Exe}
}

// applyBlocker says why the one-click update can't run here ("" when it can) and the command
// that does it instead.
func (m *Module) applyBlocker(ctx context.Context) (reason, command string) {
	kind := m.supervisor()
	switch {
	case !m.updatesEnabled(ctx):
		return "Update checks are off. Turn on " + SettingUpdatesEnabled + " first.", ""
	case m.Upgrade.Exe == "" || m.deps.Config.Home == "":
		return "This jarvisd can't update itself (no executable path).", ""
	case !kind.Supervised():
		return "jarvisd isn't running under a service manager, so nothing would start the new version. " +
			"Stop jarvisd, then run the command.", "jarvisd upgrade"
	case !m.Upgrade.Helper && !update.CanWrite(m.Upgrade.Exe):
		cmd := "sudo jarvisd upgrade"
		if runtime.GOOS == "windows" {
			cmd = "jarvisd upgrade (from an elevated PowerShell)"
		}
		return "jarvisd can't replace its own binary at " + m.Upgrade.Exe + " (it needs administrator rights). " +
			"Run the command on the server.", cmd
	}
	return "", ""
}

// handleApply is POST /api/update/apply {version?}: 202 and a background run (download,
// verify, snapshot, swap, restart), polled with GET /api/update/apply. 409 with the reason
// (and the command to run instead) when it can't run here or another is in progress.
func (m *Module) handleApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Version string `json:"version"`
	}
	if r.Body != nil && r.Body != http.NoBody && r.ContentLength != 0 && !httpx.DecodeJSON(w, r, &body) {
		return
	}
	conflict := func(detail, command string) {
		out := map[string]any{"detail": detail, "supervisor": m.supervisor()}
		if command != "" {
			out["command"] = command
		}
		httpx.WriteJSON(w, http.StatusConflict, out)
	}
	if reason, cmd := m.applyBlocker(r.Context()); reason != "" {
		conflict(reason, cmd)
		return
	}
	if mk, err := update.ReadMarker(m.paths()); err != nil || mk != nil {
		if err != nil {
			conflict("The upgrade state is unreadable: "+err.Error(), "")
		} else {
			conflict("An upgrade to "+mk.To+" is already in progress ("+mk.State+").", "")
		}
		return
	}
	m.apply.mu.Lock()
	if j := m.apply.job; j != nil && (j.State == "running" || j.State == "restarting") {
		m.apply.mu.Unlock()
		conflict("An update is already running.", "")
		return
	}
	job := &applyJob{State: "running", Step: update.StepChecking, From: m.version(), StartedAt: time.Now().UTC().Format(time.RFC3339)}
	m.apply.job = job
	snapshot := *job
	m.apply.mu.Unlock()
	m.deps.Log.Info("admin: update requested", "from", m.version(), "version", body.Version)
	go m.runApply(body.Version)
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"job": snapshot})
}

func (m *Module) setJob(f func(j *applyJob)) {
	m.apply.mu.Lock()
	defer m.apply.mu.Unlock()
	if m.apply.job != nil {
		f(m.apply.job)
	}
}

func (m *Module) runApply(target string) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	fail := func(err error) {
		msg := err.Error()
		if errors.Is(err, update.ErrUpToDate) {
			msg = "Already up to date: " + msg
		}
		m.deps.Log.Error("admin: update failed", "err", err)
		m.setJob(func(j *applyJob) { j.State, j.Error = "failed", &msg })
	}
	p := m.paths()
	src := m.Upgrade.Source
	if src.APIBase == "" {
		src.APIBase = m.Updates.APIBase
	}
	if src.UserAgent == "" {
		src.UserAgent = "jarvisd/" + m.version()
	}
	mk, err := update.Stage(ctx, update.StageOptions{
		Paths: p, Current: m.version(), Target: target, Source: src, Keys: m.Upgrade.Keys, By: "admin",
		SkipVersionCheck: m.Upgrade.SkipVersionCheck,
		Progress: func(pr update.Progress) {
			m.setJob(func(j *applyJob) { j.Step, j.Done, j.Total = pr.Step, pr.Done, pr.Total })
		},
	})
	if err != nil {
		fail(err)
		return
	}
	to := mk.To
	m.setJob(func(j *applyJob) { j.To, j.Step = &to, update.StepInstalling })
	if update.CanWrite(p.Exe) {
		if _, err := update.Swap(ctx, p, update.SwapOptions{Keys: m.Upgrade.Keys}); err != nil {
			_ = update.Abort(p, err.Error())
			fail(err)
			return
		}
	} // else the systemd pre-start (root) re-verifies and swaps it in on the restart
	m.setJob(func(j *applyJob) { j.State, j.Step = "restarting", "restarting" })
	m.deps.Log.Info("admin: update installed; restarting", "from", mk.From, "to", mk.To)
	time.Sleep(restartDelay)
	if err := m.Restarter.Request(); err != nil {
		fail(err)
	}
}

// handleApplyStatus is GET /api/update/apply: this process's run (if any), the upgrade in
// progress across restarts (the marker), and the last finished upgrade's outcome.
func (m *Module) handleApplyStatus(w http.ResponseWriter, r *http.Request) {
	m.apply.mu.Lock()
	var job *applyJob
	if m.apply.job != nil {
		cp := *m.apply.job
		job = &cp
	}
	m.apply.mu.Unlock()
	out := map[string]any{"job": job, "pending": nil, "last": nil, "current_version": m.version()}
	if m.deps.Config.Home != "" {
		p := m.paths()
		if mk, err := update.ReadMarker(p); err == nil && mk != nil {
			out["pending"] = map[string]any{"state": mk.State, "from_version": mk.From, "to_version": mk.To, "attempts": mk.Attempts}
		}
		if res, err := update.ReadResult(p); err == nil && res != nil {
			out["last"] = res
		}
	}
	reason, cmd := m.applyBlocker(r.Context())
	out["can_apply"] = reason == ""
	if reason != "" {
		out["blocked_reason"] = reason
	}
	if cmd != "" {
		out["command"] = cmd
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}
