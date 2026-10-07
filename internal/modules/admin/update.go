package admin

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// The update check (AD5, inventory §6.2 #9, invariants I1/I2). Opt-in: with
// updates.enabled off (the default; env JARVIS_ALLOW_UPDATES) jarvisd makes no outbound request
// at all. When on, it reads the GitHub releases of the jarvis-server repo, picks the newest
// stable release (prereleases only when this build is itself a prerelease), and reports the
// download for this platform. Honesty rule: "up to date" is only ever said after a successful
// check that could compare versions; otherwise checked is false with the reason.
// Installing is POST /api/update/apply (apply.go, AD5): signed, with snapshot and rollback.

// Admin settings.
const (
	SettingUpdatesEnabled = "updates.enabled"
	EnvAllowUpdates       = "JARVIS_ALLOW_UPDATES"
)

// Definitions are the admin module's settings.
var Definitions = []settings.Definition{
	{Key: SettingUpdatesEnabled, Category: "updates", Type: settings.Bool, Default: false, EnvFallback: EnvAllowUpdates,
		Description: "Check GitHub for new jarvisd releases. Off by default: with it off jarvisd makes no " +
			"update request at all."},
}

const (
	// UpdateRepo is where releases are published.
	UpdateRepo = update.Repo
	// updateTTL is how long a successful check is reused (GitHub allows 60 anonymous
	// requests an hour).
	updateTTL = time.Hour
	// maxNotes caps the release notes returned.
	maxNotes = 20000
)

// UpdateOptions configure the check.
type UpdateOptions struct {
	// APIBase replaces https://api.github.com (tests).
	APIBase string
	// Client makes the request; nil is a 15 s client.
	Client *http.Client
}

type releaseAsset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
	Size int64  `json:"size"`
}

// updateStatus is the GET /api/update body.
type updateStatus struct {
	UpdatesEnabled  bool          `json:"updates_enabled"`
	Checked         bool          `json:"checked"`
	Reason          *string       `json:"reason"`
	CheckedAt       *string       `json:"checked_at"`
	CurrentVersion  string        `json:"current_version"`
	LatestVersion   *string       `json:"latest_version"`
	UpdateAvailable bool          `json:"update_available"`
	UpToDate        bool          `json:"up_to_date"`
	Prerelease      bool          `json:"prerelease"`
	ReleaseURL      *string       `json:"release_url"`
	ReleaseNotes    *string       `json:"release_notes"`
	PublishedAt     *string       `json:"published_at"`
	Platform        string        `json:"platform"`
	Asset           *releaseAsset `json:"asset"`
	ChecksumsURL    *string       `json:"checksums_url"`
	InstallCommand  *string       `json:"install_command"`
	InstallHint     string        `json:"install_hint"`
	// CanApply: POST /api/update/apply can run here; else ApplyBlocked says why and
	// ApplyCommand is what to run on the server instead.
	CanApply     bool    `json:"can_apply"`
	ApplyBlocked *string `json:"apply_blocked"`
	ApplyCommand *string `json:"apply_command"`
}

// updateCache keeps the last successful check.
type updateCache struct {
	mu     sync.Mutex
	status *updateStatus
	at     time.Time
}

func strp(s string) *string { return &s }

func platform() string { return update.Platform() }

// releaseSource reads the GitHub releases (Updates overrides the API base in tests).
func (m *Module) releaseSource() update.Source {
	return update.Source{APIBase: m.Updates.APIBase, Client: m.Updates.Client, UserAgent: "jarvisd/" + m.version()}
}

func (m *Module) version() string {
	if m.Version == "" {
		return "dev"
	}
	return m.Version
}

func (m *Module) updatesEnabled(ctx context.Context) bool {
	return m.settings != nil && m.settings.Bool(ctx, SettingUpdatesEnabled, settings.Scope{})
}

// baseStatus is the answer before any check: nothing claimed.
func (m *Module) baseStatus(enabled bool) *updateStatus {
	return &updateStatus{
		UpdatesEnabled: enabled, CurrentVersion: m.version(), Platform: platform(),
		InstallHint: "Install updates from here (jarvisd checks the release signature, keeps the previous " +
			"version and rolls back if the new one doesn't come up) or run `jarvisd upgrade` on the server.",
	}
}

// updateStatusFor answers GET /api/update and POST /api/update/check. Disabled: no network.
// Enabled: the cached check when fresh (unless force), else a new one.
func (m *Module) updateStatusFor(ctx context.Context, force bool) *updateStatus {
	enabled := m.updatesEnabled(ctx)
	if !enabled {
		st := m.baseStatus(false)
		st.Reason = strp("Update checks are off. Turn on " + SettingUpdatesEnabled + " to let jarvisd ask GitHub for new releases.")
		return st
	}
	m.upd.mu.Lock()
	defer m.upd.mu.Unlock()
	if !force && m.upd.status != nil && time.Since(m.upd.at) < updateTTL {
		cp := *m.upd.status
		return &cp
	}
	st := m.checkReleases(ctx)
	if st.Checked {
		m.upd.status, m.upd.at = st, time.Now()
		cp := *st
		return &cp
	}
	return st
}

// checkReleases asks GitHub. Any failure is checked:false with the reason.
func (m *Module) checkReleases(ctx context.Context) *updateStatus {
	st := m.baseStatus(true)
	fail := func(reason string) *updateStatus {
		st.Reason = strp(reason)
		m.deps.Log.Warn("admin: update check failed", "reason", reason)
		return st
	}
	releases, err := m.releaseSource().List(ctx)
	if err != nil {
		return fail(err.Error())
	}
	current, currentOK := update.ParseVersion(m.version())
	best := update.Newest(releases, m.version())
	var bestV update.Version
	if best != nil {
		bestV, _ = update.ParseVersion(best.Tag)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if best == nil {
		st.Checked, st.CheckedAt = true, &now
		st.Reason = strp("No published release was found.")
		return st
	}
	st.Checked, st.CheckedAt = true, &now
	st.LatestVersion, st.Prerelease = strp(best.Tag), best.Prerelease
	st.ReleaseURL, st.PublishedAt = strp(best.HTMLURL), strp(best.PublishedAt)
	notes := best.Body
	if len(notes) > maxNotes {
		notes = notes[:maxNotes] + "…"
	}
	st.ReleaseNotes = strp(notes)
	suffix := "-" + platform() + ".tar.gz"
	installer := "install.sh"
	if runtime.GOOS == "windows" {
		suffix, installer = "-"+platform()+".zip", "install.ps1"
	}
	for _, a := range best.Assets {
		switch {
		case strings.HasPrefix(a.Name, "jarvisd-") && strings.HasSuffix(a.Name, suffix):
			st.Asset = &releaseAsset{Name: a.Name, URL: a.URL, Size: a.Size}
		case a.Name == "SHA256SUMS":
			st.ChecksumsURL = strp(a.URL)
		case a.Name == installer:
			cmd := "curl -fsSL " + a.URL + " | sh"
			if installer == "install.ps1" {
				cmd = "irm " + a.URL + " | iex"
			}
			st.InstallCommand = strp(cmd)
		}
	}
	switch {
	case st.Asset == nil:
		st.InstallHint = "This release has no download for " + platform() + "."
	case st.InstallCommand != nil:
		st.InstallHint = "Run the install command; it verifies the download and restarts jarvisd."
	default:
		st.InstallHint = "Install it from here, or run `jarvisd upgrade` on the server: either checks the " +
			"release signature, snapshots the database, keeps the previous version and rolls back if the new " +
			"one doesn't come up."
	}
	if !currentOK {
		// Honest: a development build can't be compared, so neither "available" nor "up to date".
		st.Reason = strp("This build (" + m.version() + ") has no release version to compare with.")
		return st
	}
	if bestV.Compare(current) > 0 {
		st.UpdateAvailable = true
	} else {
		st.UpToDate = true
	}
	return st
}

// handleUpdate is GET /api/update: the status, checking GitHub only when updates are on and
// the last check is older than an hour.
func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, m.withApply(r.Context(), m.updateStatusFor(r.Context(), false)))
}

// withApply adds whether the one-click update can run here.
func (m *Module) withApply(ctx context.Context, st *updateStatus) *updateStatus {
	reason, cmd := m.applyBlocker(ctx)
	st.CanApply = reason == ""
	if reason != "" {
		st.ApplyBlocked = strp(reason)
	}
	if cmd != "" {
		st.ApplyCommand = strp(cmd)
	}
	return st
}

// handleUpdateCheck is POST /api/update/check: check now (still nothing when updates are off).
func (m *Module) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, m.withApply(r.Context(), m.updateStatusFor(r.Context(), true)))
}

// handleUpdateSettings is PUT /api/update/settings {enabled}: turn checks on or off, then
// answer the status (turning them on does not check by itself; the next GET or check does).
func (m *Module) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if m.settings == nil {
		unavailable(w, "admin")
		return
	}
	var body map[string]any
	if !httpx.DecodeJSON(w, r, &body) {
		return
	}
	enabled, ok := body["enabled"].(bool)
	if !ok {
		httpx.ValidationError(w, httpx.FieldError{Type: "bool_type", Loc: []any{"body", "enabled"}, Msg: "Input should be a valid boolean", Input: body["enabled"]})
		return
	}
	if err := m.settings.Set(r.Context(), SettingUpdatesEnabled, enabled, settings.Scope{}); err != nil {
		m.deps.Log.Error("admin: saving the update setting failed", "err", err)
		httpx.Error(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !enabled {
		m.upd.mu.Lock()
		m.upd.status = nil
		m.upd.mu.Unlock()
	}
	m.deps.Log.Info("admin: setting changed", "service", "admin", "key", SettingUpdatesEnabled)
	st := m.baseStatus(enabled)
	if enabled {
		m.upd.mu.Lock()
		if m.upd.status != nil && time.Since(m.upd.at) < updateTTL {
			cp := *m.upd.status
			st = &cp
		} else {
			st.Reason = strp("Not checked yet.")
		}
		m.upd.mu.Unlock()
	} else {
		st.Reason = strp("Update checks are off.")
	}
	httpx.WriteJSON(w, http.StatusOK, st)
}
