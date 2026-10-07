package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The update check (AD5, inventory §6.2 #9, invariants I1/I2). Opt-in: with
// updates.enabled off (the default; env JARVIS_ALLOW_UPDATES) jarvisd makes no outbound request
// at all. When on, it reads the GitHub releases of the jarvis-server repo, picks the newest
// stable release (prereleases only when this build is itself a prerelease), and reports the
// download for this platform. Honesty rule: "up to date" is only ever said after a successful
// check that could compare versions; otherwise checked is false with the reason.
// Installing is a later step (AD5 signed self-update, after installer I1).

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
	UpdateRepo = "alexberardi/jarvis-server"
	// defaultGitHubAPI is the GitHub REST base (UpdateOptions.APIBase overrides it in tests).
	defaultGitHubAPI = "https://api.github.com"
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
}

// updateCache keeps the last successful check.
type updateCache struct {
	mu     sync.Mutex
	status *updateStatus
	at     time.Time
}

type ghRelease struct {
	TagName     string `json:"tag_name"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	Draft       bool   `json:"draft"`
	Prerelease  bool   `json:"prerelease"`
	PublishedAt string `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func strp(s string) *string { return &s }

func platform() string { return runtime.GOOS + "-" + runtime.GOARCH }

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
		InstallHint: "Updates are installed by replacing the jarvisd binary and restarting it; " +
			"one-click updates arrive with the installer.",
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
	base := strings.TrimRight(m.Updates.APIBase, "/")
	if base == "" {
		base = defaultGitHubAPI
	}
	client := m.Updates.Client
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/repos/"+UpdateRepo+"/releases?per_page=30", nil)
	if err != nil {
		return fail("Couldn't build the update request: " + err.Error())
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "jarvisd/"+m.version())
	resp, err := client.Do(req)
	if err != nil {
		return fail("Couldn't reach GitHub to check for updates: " + err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Sprintf("GitHub answered HTTP %d to the update check", resp.StatusCode))
	}
	var releases []ghRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&releases); err != nil {
		return fail("GitHub's release list couldn't be read: " + err.Error())
	}
	current, currentOK := parseSemver(m.version())
	allowPre := currentOK && current.pre != ""
	var best *ghRelease
	var bestV semver
	for i := range releases {
		r := &releases[i]
		v, ok := parseSemver(r.TagName)
		if r.Draft || !ok || (r.Prerelease && !allowPre) || (v.pre != "" && !allowPre) {
			continue
		}
		if best == nil || v.compare(bestV) > 0 {
			best, bestV = r, v
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if best == nil {
		st.Checked, st.CheckedAt = true, &now
		st.Reason = strp("No published release was found.")
		return st
	}
	st.Checked, st.CheckedAt = true, &now
	st.LatestVersion, st.Prerelease = strp(best.TagName), best.Prerelease
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
		st.InstallHint = "Download " + st.Asset.Name + ", check it against SHA256SUMS, replace the jarvisd " +
			"binary and restart jarvisd. One-click updates arrive with the installer."
	}
	if !currentOK {
		// Honest: a development build can't be compared, so neither "available" nor "up to date".
		st.Reason = strp("This build (" + m.version() + ") has no release version to compare with.")
		return st
	}
	if bestV.compare(current) > 0 {
		st.UpdateAvailable = true
	} else {
		st.UpToDate = true
	}
	return st
}

// handleUpdate is GET /api/update: the status, checking GitHub only when updates are on and
// the last check is older than an hour.
func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, m.updateStatusFor(r.Context(), false))
}

// handleUpdateCheck is POST /api/update/check: check now (still nothing when updates are off).
func (m *Module) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, m.updateStatusFor(r.Context(), true))
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

// --- semver (vMAJOR.MINOR.PATCH[-PRERELEASE][+BUILD]) ---

type semver struct {
	major, minor, patch int
	pre                 string
}

// parseSemver reads a release tag; ok is false for anything else ("dev", a commit hash).
func parseSemver(s string) (semver, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	s, _, _ = strings.Cut(s, "+")
	core, pre, _ := strings.Cut(s, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return semver{}, false
	}
	var n [3]int
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 || (len(p) > 1 && p[0] == '0') || strings.HasPrefix(p, "+") {
			return semver{}, false
		}
		n[i] = x
	}
	return semver{n[0], n[1], n[2], pre}, true
}

// compare orders by precedence (semver §11): a prerelease sorts before its release.
func (a semver) compare(b semver) int {
	for _, d := range []int{a.major - b.major, a.minor - b.minor, a.patch - b.patch} {
		if d != 0 {
			return sign(d)
		}
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	ap, bp := strings.Split(a.pre, "."), strings.Split(b.pre, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		x, xErr := strconv.Atoi(ap[i])
		y, yErr := strconv.Atoi(bp[i])
		switch {
		case xErr == nil && yErr == nil:
			if x != y {
				return sign(x - y)
			}
		case xErr == nil:
			return -1 // numeric identifiers sort before alphanumeric
		case yErr == nil:
			return 1
		default:
			if c := strings.Compare(ap[i], bp[i]); c != 0 {
				return c
			}
		}
	}
	return sign(len(ap) - len(bp))
}

func sign(d int) int {
	switch {
	case d > 0:
		return 1
	case d < 0:
		return -1
	}
	return 0
}
