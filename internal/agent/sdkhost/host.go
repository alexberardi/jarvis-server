// Package sdkhost is the Go side of the SDK backends the Pi's built-in commands reach through
// the sidecar (docs/agent/go-pi-node.md §5.2): it answers the child's llm.*, cc.*, timer.*,
// audio.*, alerts.*, inbox.* and signals.* requests. Command-center calls go out as this
// node, with the key the agent holds; the child never sees it.
package sdkhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/music"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// Host answers the backends. A nil part answers its methods with an error (the command gets
// the SDK's safe default).
type Host struct {
	Timers *timers.Service
	Alerts *Alerts
	Audio  *Audio
	// Say queues text for the speaker and returns at once (audio.speak, a timer's expiry): a
	// command may speak mid-turn, while the voice pipeline is waiting for it. False: not queued.
	Say func(text string) bool
	// Music, when set, takes the speaker over for a music command's "play" (BeforeCall).
	Music *music.Ducker
	Log   *slog.Logger

	cc atomic.Pointer[node.CC]
}

// BeforeCall is the sidecar's takeover hook (sidecar.Takeover): before a music command's
// "play" runs, the other players stop (N:utils/command_execution_service.py
// _maybe_take_over_music). It never fails the call.
func (h *Host) BeforeCall(ctx context.Context, command, declared string, args map[string]any) {
	if h.Music != nil {
		h.Music.BeforeCall(ctx, music.TakeoverBinary(command, declared), args)
	}
}

// SetCC is the node's command-center client, once the agent is connected.
func (h *Host) SetCC(cc *node.CC) { h.cc.Store(cc) }

func (h *Host) log() *slog.Logger {
	if h.Log == nil {
		return slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return h.Log
}

// errNoCC: not connected to command-center yet.
var errNoCC = errors.New("not connected to command-center")

// llmTimeout bounds a command's LLM call, which holds up its voice turn.
const llmTimeout = 60 * time.Second

type params struct {
	Prompt  string          `json:"prompt"`
	Path    string          `json:"path"`
	JSON    json.RawMessage `json:"json"`
	Seconds *float64        `json:"seconds"`
	Label   *string         `json:"label"`
	TimerID string          `json:"timer_id"`
	Percent *float64        `json:"percent"`
	Delta   *float64        `json:"delta"`
	Muted   bool            `json:"muted"`
	Text    string          `json:"text"`
	IDs     []string        `json:"ids"`
	Payload json.RawMessage `json:"payload"`

	// inbox.post
	CommandName            string          `json:"command_name"`
	Title                  string          `json:"title"`
	Summary                string          `json:"summary"`
	Body                   string          `json:"body"`
	Category               string          `json:"category"`
	Metadata               json.RawMessage `json:"metadata"`
	UserID                 json.RawMessage `json:"user_id"`
	CreatePushNotification bool            `json:"create_push_notification"`
	TargetType             string          `json:"target_type"`
	// The files the sidecar wrote or the package named, uploaded before the item.
	Attachments []struct {
		Path     string `json:"path"`
		Mime     string `json:"mime"`
		Filename string `json:"filename"`
	} `json:"attachments"`
}

// rejection is the inbox tag for jarvisd refusing an attachment or an item that references
// them (N:services/inbox_backend.py): 413 too large, 415 a type it won't show, 422 bad ids.
func rejection(err error) string {
	var he *node.HTTPError
	if errors.As(err, &he) {
		switch he.Status {
		case http.StatusRequestEntityTooLarge:
			return "attachment_too_large"
		case http.StatusUnsupportedMediaType:
			return "attachment_type"
		case http.StatusUnprocessableEntity:
			return "invalid"
		}
	}
	return "http_error"
}

// Answer serves one request (sidecar.Host).
func (h *Host) Answer(ctx context.Context, method string, raw json.RawMessage) (any, error) {
	var p params
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
	}
	family, _, _ := strings.Cut(method, ".")
	switch family {
	case "llm", "cc", "inbox", "signals":
		return h.commandCenter(ctx, method, p)
	case "timer":
		if h.Timers == nil {
			return nil, errors.New("no timers on this node")
		}
		return h.timer(method, p)
	case "alerts":
		if h.Alerts == nil {
			return nil, errors.New("no alert queue on this node")
		}
		return h.alerts(method, p)
	case "audio":
		return h.audio(ctx, method, p)
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

func (h *Host) timer(method string, p params) (any, error) {
	t := h.Timers
	switch method {
	case "timer.set":
		if p.Seconds == nil {
			return nil, errors.New("timer.set without seconds")
		}
		return t.Set(int(*p.Seconds), p.Label)
	case "timer.cancel":
		return t.Cancel(p.TimerID), nil
	case "timer.list_active":
		return t.Active(), nil
	case "timer.get":
		if g := t.Get(p.TimerID); g != nil {
			return g, nil
		}
		return nil, nil
	case "timer.find_by_label":
		if p.Label == nil {
			return nil, nil
		}
		if id := t.FindByLabel(*p.Label); id != "" {
			return id, nil
		}
		return nil, nil
	case "timer.clear_all":
		return t.ClearAll(), nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

func wire(list []Alert) []map[string]any {
	out := []map[string]any{}
	for _, a := range list {
		out = append(out, a.wire())
	}
	return out
}

func (h *Host) alerts(method string, p params) (any, error) {
	q := h.Alerts
	switch method {
	case "alerts.pending":
		return wire(q.Pending()), nil
	case "alerts.flush":
		return wire(q.Flush()), nil
	case "alerts.remove":
		return q.Remove(p.IDs), nil
	case "alerts.count":
		return q.Count(), nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

func (h *Host) audio(ctx context.Context, method string, p params) (any, error) {
	if method == "audio.speak" {
		if h.Say == nil || strings.TrimSpace(p.Text) == "" {
			return false, nil
		}
		return h.Say(p.Text), nil
	}
	a := h.Audio
	if a == nil {
		return nil, errors.New("no volume control on this node")
	}
	switch method {
	case "audio.set_volume_percent":
		if p.Percent == nil {
			return nil, errors.New("set_volume_percent without a percent")
		}
		return a.SetVolume(ctx, int(*p.Percent)), nil
	case "audio.adjust_volume_percent":
		if p.Delta == nil {
			return nil, errors.New("adjust_volume_percent without a delta")
		}
		pct, err := a.Adjust(ctx, int(*p.Delta))
		if err != nil {
			return nil, err
		}
		return pct, nil
	case "audio.set_muted":
		return a.SetMuted(ctx, p.Muted), nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}

// nodePath is a cc.post path made absolute and clean; only the node plugin API
// (/api/v0/node/...) is reachable, as the commands use it.
func nodePath(p string) (string, error) {
	clean := path.Clean("/" + strings.TrimLeft(p, "/"))
	if !strings.HasPrefix(clean, "/api/v0/node/") || strings.ContainsAny(clean, "?#") {
		return "", fmt.Errorf("cc.post is limited to /api/v0/node/ paths, not %q", p)
	}
	return clean, nil
}

func (h *Host) commandCenter(ctx context.Context, method string, p params) (any, error) {
	cc := h.cc.Load()
	switch method {
	case "llm.chat_text":
		if cc == nil {
			return nil, errNoCC
		}
		// The node client's 15 s limit is short for a model's reply.
		slow := *cc
		if cc.HTTP != nil {
			hc := *cc.HTTP
			hc.Timeout = llmTimeout
			slow.HTTP = &hc
		}
		// As N:JarvisCommandCenterClient.chat_text: the prompt as one system message.
		var resp struct {
			Content *string `json:"content"`
		}
		body := map[string]any{"messages": []map[string]string{{"role": "system", "content": p.Prompt}},
			"model": "live", "temperature": 0}
		if err := slow.Do(ctx, http.MethodPost, "/api/v0/node/llm/chat", body, &resp); err != nil {
			return nil, err
		}
		if resp.Content == nil {
			return nil, nil
		}
		return *resp.Content, nil
	case "cc.post":
		pth, err := nodePath(p.Path)
		if err != nil {
			return nil, err
		}
		if cc == nil {
			return nil, errNoCC
		}
		var out any
		body := p.JSON
		if len(body) == 0 {
			body = json.RawMessage("{}")
		}
		if err := cc.Do(ctx, http.MethodPost, pth, body, &out); err != nil {
			return nil, err
		}
		return out, nil
	case "inbox.post":
		// The tags of N:services/inbox_backend.py.
		if strings.TrimSpace(p.Title) == "" {
			return "invalid", nil
		}
		if cc == nil {
			return "no_cc_url", nil
		}
		body := map[string]any{"title": p.Title, "summary": p.Summary, "body": p.Body, "category": p.Category,
			"metadata": p.Metadata, "user_id": p.UserID, "create_push_notification": p.CreatePushNotification,
			"target_type": p.TargetType}
		if len(p.Metadata) == 0 {
			body["metadata"] = nil
		}
		if len(p.UserID) == 0 {
			body["user_id"] = nil
		}
		if len(p.Attachments) > 0 {
			ids := []string{}
			for _, a := range p.Attachments {
				data, err := os.ReadFile(a.Path)
				if err != nil {
					h.log().Warn("sdkhost: inbox attachment unreadable", "command", p.CommandName, "file", a.Filename, "err", err)
					return "invalid", nil
				}
				id, err := cc.UploadInboxAttachment(ctx, a.Mime, data)
				if err != nil {
					h.log().Warn("sdkhost: inbox attachment not uploaded", "command", p.CommandName, "file", a.Filename, "err", err)
					return rejection(err), nil
				}
				ids = append(ids, id)
			}
			body["attachment_ids"] = ids
		}
		var resp struct {
			Sent bool `json:"sent"`
		}
		if err := cc.Do(ctx, http.MethodPost, "/api/v0/node/inbox-item", body, &resp); err != nil {
			h.log().Warn("sdkhost: inbox post failed", "command", p.CommandName, "err", err)
			if len(p.Attachments) > 0 {
				return rejection(err), nil
			}
			return "http_error", nil
		}
		if !resp.Sent {
			h.log().Warn("sdkhost: inbox post accepted but not delivered", "command", p.CommandName)
			return "http_error", nil
		}
		return "ok", nil
	case "signals.emit":
		if cc == nil {
			return "no_cc_url", nil
		}
		if len(p.Payload) == 0 {
			return "invalid", nil
		}
		if err := cc.Do(ctx, http.MethodPost, "/api/v0/signals", p.Payload, nil); err != nil {
			h.log().Warn("sdkhost: signal emit failed", "err", err)
			return "http_error", nil
		}
		return "ok", nil
	}
	return nil, fmt.Errorf("unknown method %q", method)
}
