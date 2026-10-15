package packages

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// The MQTT sub-topics of jarvis/nodes/{id}/ that jarvisd nudges a node on when the app asks
// for a package change (cc packages.go publishPkg).
const (
	TopicInstall   = "package-install"
	TopicUninstall = "package-uninstall"
	TopicRevert    = "package-revert"
)

// Topics are the three, for the runner's subscriptions.
var Topics = []string{TopicInstall, TopicUninstall, TopicRevert}

// opTimeout bounds one change. jarvisd gives a verified request 15 minutes.
const opTimeout = 12 * time.Minute

// API is the node's command-center client (node.CC).
type API interface {
	Do(ctx context.Context, method, path string, body, out any) error
}

// Requests answers the app's package requests as a Pi does (package_install_handler.py): the
// MQTT nudge carries only a request id; what to install comes from jarvisd's verify answer for
// a request it issued to this node, never from the nudge. The result goes back to jarvisd,
// which the app polls. A desktop applies the change without restarting, so there is no
// "restarting" step.
type Requests struct {
	NodeID  string
	Manager *Manager
	Log     *slog.Logger

	mu   sync.Mutex
	seen map[string]bool // request ids taken: a QoS-1 nudge can arrive twice
}

// take reports whether rid is new, and remembers it.
func (q *Requests) take(rid string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.seen == nil {
		q.seen = map[string]bool{}
	}
	if q.seen[rid] {
		return false
	}
	q.seen[rid] = true
	return true
}

type verifyAnswer struct {
	Confirmed     bool    `json:"confirmed"`
	CommandName   string  `json:"command_name"`
	GithubRepoURL string  `json:"github_repo_url"`
	GitTag        *string `json:"git_tag"`
}

// Handle serves one nudge on topic (one of Topics). It blocks until the result is posted.
func (q *Requests) Handle(ctx context.Context, api API, topic string, payload []byte) {
	op := topic[len("package-"):]
	var nudge struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(payload, &nudge) != nil || nudge.RequestID == "" || len(nudge.RequestID) > 64 {
		q.Log.Warn("packages: ignoring a malformed request", "topic", topic)
		return
	}
	rid := nudge.RequestID
	if !q.take(rid) {
		return
	}
	var v verifyAnswer
	// Install, uninstall and revert share one request table and its verify route.
	path := "/api/v0/nodes/" + url.PathEscape(q.NodeID) + "/package-install/" + url.PathEscape(rid) + "/verify"
	if err := api.Do(ctx, http.MethodGet, path, nil, &v); err != nil || !v.Confirmed {
		q.Log.Warn("packages: request not confirmed by jarvisd; ignoring it", "op", op, "request_id", rid, "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()
	var details map[string]any
	var err error
	switch topic {
	case TopicInstall:
		if v.GithubRepoURL == "" {
			q.post(ctx, api, op, rid, nil, "No repo URL in verify response")
			return
		}
		ref := ""
		if v.GitTag != nil {
			ref = *v.GitTag
		}
		q.Log.Info("packages: install", "request_id", rid, "command", v.CommandName, "repo", v.GithubRepoURL, "ref", ref)
		details, err = q.Manager.Install(ctx, v.GithubRepoURL, ref)
	case TopicUninstall:
		var name string
		name, err = q.Manager.Uninstall(ctx, v.CommandName)
		details = map[string]any{"package_name": name}
	case TopicRevert:
		var name, version string
		name, version, err = q.Manager.Revert(ctx, v.CommandName)
		details = map[string]any{"package_name": name, "version": version}
	default:
		return
	}
	if err != nil {
		q.Log.Error("packages: "+op+" failed", "request_id", rid, "command", v.CommandName, "err", err)
		q.post(ctx, api, op, rid, nil, err.Error())
		return
	}
	q.post(ctx, api, op, rid, details, "")
}

func (q *Requests) post(ctx context.Context, api API, op, rid string, details map[string]any, errText string) {
	body := map[string]any{"success": errText == ""}
	if errText != "" {
		body["error"] = errText
	}
	if len(details) > 0 {
		body["details"] = details
	}
	path := "/api/v0/nodes/" + url.PathEscape(q.NodeID) + "/package-" + op + "/" + url.PathEscape(rid) + "/results"
	// The change is done whatever the deadline did to it; give the post its own.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := api.Do(pctx, http.MethodPost, path, body, nil); err != nil {
		q.Log.Error("packages: result not delivered", "op", op, "request_id", rid, "err", err)
		return
	}
	q.Log.Info("packages: result delivered", "op", op, "request_id", rid, "success", errText == "")
}
