package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// The verified desktop-agent download (install plan 01 §3.1 step 1, item 3.1):
// GET /api/addons/agent/{os}/{arch}. jarvisd fetches the agent build of the release that
// matches its own version, checks it exactly as the updater checks jarvisd itself (the
// minisign signature over SHA256SUMS with the keys compiled into this binary, the trusted
// comment naming the tag, then the file's SHA-256 from that list: update.FetchSigned), keeps
// it under <home>/updates/agent/<tag>/<file>/ and streams it to the browser. So what a member's
// browser saves is a file the server has verified, whatever network sits between the server
// and GitHub.
//
// It fetches only when someone asks for the file, so it is not gated on updates.enabled (that
// setting is the background update check). A dev build has no release to match and answers
// 409 with the release page.

// agentPlatforms are the agent builds a release carries (release.yml).
var agentPlatforms = map[string]bool{
	"linux-amd64": true, "linux-arm64": true, "darwin-arm64": true, "windows-amd64": true,
}

// agentFetchTimeout bounds one fetch from GitHub. It runs detached from the request, so a
// browser that gives up doesn't waste a half-done download for the next one.
const agentFetchTimeout = 30 * time.Minute

// agentDownloads serialises fetches, so two clicks on the same button download once.
type agentDownloads struct {
	mu sync.Mutex
}

// agentAsset is one file a release offers for a platform.
type agentAsset struct {
	name        string
	contentType string
}

// agentAssetFor names the release file for os/arch: the .deb on Linux unless format=archive,
// else the agent archive. The dmg (macOS, item 2.2) and the Windows installer (2.3) take over
// from the archives when the release carries them.
func agentAssetFor(tag, goos, arch, format string) (agentAsset, error) {
	platform := goos + "-" + arch
	if !agentPlatforms[platform] {
		return agentAsset{}, fmt.Errorf("There is no Jarvis Automation agent for %s yet", platform)
	}
	switch format {
	case "", "archive", "deb":
	default:
		return agentAsset{}, fmt.Errorf("Unknown format %q (deb or archive)", format)
	}
	if goos == "linux" && format != "archive" {
		// build-debs.sh: jarvis-automation-agent_<tag without v>_<arch>.deb
		return agentAsset{"jarvis-automation-agent_" + strings.TrimPrefix(tag, "v") + "_" + arch + ".deb",
			"application/vnd.debian.binary-package"}, nil
	}
	if format == "deb" {
		return agentAsset{}, errors.New("The .deb is for Linux only")
	}
	name := update.ProgramArchiveName(update.AgentProgram, tag, platform)
	ct := "application/gzip"
	if strings.HasSuffix(name, ".zip") {
		ct = "application/zip"
	}
	return agentAsset{name, ct}, nil
}

func releasePage(tag string) string {
	if tag == "" {
		return "https://github.com/" + update.Repo + "/releases"
	}
	return "https://github.com/" + update.Repo + "/releases/tag/" + tag
}

// agentSource is where agent builds come from: the upgrade source (a flat release directory
// in CI), else GitHub (or the test API base).
func (m *Module) agentSource() update.Source {
	src := m.Upgrade.Source
	if src.APIBase == "" {
		src.APIBase = m.Updates.APIBase
	}
	if src.UserAgent == "" {
		src.UserAgent = "jarvisd/" + m.version()
	}
	return src
}

func (m *Module) handleAgentDownload(w http.ResponseWriter, r *http.Request) {
	tag := m.version()
	if _, ok := update.ParseVersion(tag); !ok {
		httpx.WriteJSON(w, http.StatusConflict, map[string]any{
			"detail":      "This server is a development build (" + tag + "), so there is no matching agent release to download. Get the agent from the release page.",
			"release_url": releasePage(""),
		})
		return
	}
	asset, err := agentAssetFor(tag, r.PathValue("os"), r.PathValue("arch"), r.URL.Query().Get("format"))
	if err != nil {
		httpx.WriteJSON(w, http.StatusNotFound, map[string]any{"detail": err.Error(), "release_url": releasePage(tag)})
		return
	}
	if m.deps.Config.Home == "" {
		httpx.Error(w, http.StatusServiceUnavailable, "jarvisd has no home directory to keep downloads in")
		return
	}
	path, err := m.verifiedAgent(r.Context(), tag, asset.name)
	if err != nil {
		m.deps.Log.Error("admin: agent download refused", "file", asset.name, "err", err)
		httpx.WriteJSON(w, http.StatusBadGateway, map[string]any{
			"detail":      "Couldn't get a verified copy of the agent from the release: " + err.Error(),
			"release_url": releasePage(tag),
		})
		return
	}
	f, err := os.Open(path)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The verified download couldn't be read")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "The verified download couldn't be read")
		return
	}
	h := w.Header()
	h.Set("Content-Type", asset.contentType)
	h.Set("Content-Disposition", `attachment; filename="`+asset.name+`"`)
	h.Set("Cache-Control", "private, no-store")
	h.Set("X-Jarvis-Release", tag)
	http.ServeContent(w, r, "", fi.ModTime(), f)
}

// verifiedAgent returns the cached file for tag/name after checking it again, fetching and
// verifying it first when the cache is missing or doesn't verify. Only one fetch runs at a time.
func (m *Module) verifiedAgent(ctx context.Context, tag, name string) (string, error) {
	keys := m.Upgrade.Keys
	if keys == nil {
		var err error
		if keys, err = update.TrustedKeys(); err != nil {
			return "", err
		}
	}
	root := filepath.Join(m.deps.Config.Home, "updates", "agent")
	dir := filepath.Join(root, tag, name)
	cached := &update.Fetched{Dir: dir, Tag: tag, Name: name, Archive: filepath.Join(dir, name)}

	m.agentDL.mu.Lock()
	defer m.agentDL.mu.Unlock()
	// Every request re-checks the cached copy from disk: a file altered after it was fetched is
	// fetched again, never served.
	if _, err := os.Stat(cached.Archive); err == nil {
		err := cached.Verify(keys)
		if err == nil {
			return cached.Archive, nil
		}
		m.deps.Log.Warn("admin: cached agent download no longer verifies; fetching it again", "file", name, "err", err)
	}
	if err := os.MkdirAll(filepath.Join(root, tag), 0o700); err != nil {
		return "", err
	}
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentFetchTimeout)
	defer cancel()
	src := m.agentSource()
	rel, err := src.ByTag(fctx, tag)
	if err != nil {
		return "", err
	}
	f, err := update.FetchSigned(fctx, src, rel, name, keys, dir, nil)
	if err != nil {
		return "", err
	}
	m.deps.Log.Info("admin: agent download verified and cached", "file", name, "release", tag)
	pruneAgentCache(root, tag)
	return f.Archive, nil
}

// pruneAgentCache drops the copies kept for other releases: an agent always matches its
// server's version, so after an upgrade the old ones are never served again.
func pruneAgentCache(root, keep string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != keep {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}
