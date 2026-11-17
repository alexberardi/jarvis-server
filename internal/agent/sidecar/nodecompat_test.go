package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
)

// compatHost is the Go agent as nodecompat's modules reach it: node.info, cc.request,
// devices.list and audio.speak, each call recorded.
type compatHost struct {
	mu    sync.Mutex
	calls []string
}

func (h *compatHost) Answer(_ context.Context, method string, raw json.RawMessage) (any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	switch method {
	case "node.info":
		h.calls = append(h.calls, method)
		return map[string]any{"node_id": "n1", "room": "office", "household_id": "hh1",
			"command_center_url": "http://cc.local:7700", "home_context": map[string]any{"location": "Springfield, IL"}}, nil
	case "cc.request":
		h.calls = append(h.calls, p["method"].(string)+" "+p["path"].(string))
		switch p["path"] {
		case "/api/v0/node/llm/chat":
			return map[string]any{"status": 200, "body": map[string]any{"content": "hello back"}}, nil
		case "/api/v0/households/hh1/rooms":
			return map[string]any{"status": 200, "body": []any{map[string]any{"id": "r1", "name": "Office"}}}, nil
		case "/api/v0/memories/inject":
			return map[string]any{"status": 200, "body": map[string]any{"injected": 1}}, nil
		}
		return map[string]any{"status": 404, "body": nil}, nil
	case "devices.list":
		h.calls = append(h.calls, method)
		return []any{map[string]any{"entity_id": "light.lamp", "protocol": "nolan", "domain": "light", "name": "Lamp"}}, nil
	case "audio.speak":
		h.calls = append(h.calls, method+" "+p["text"].(string))
		return true, nil
	}
	return nil, nil
}

// The user (2026-11-16): packages written for the Python node must work as they did there.
// Every node module the 29 published packages import answers as on the node, over the Go agent:
// secrets and auth flags in its store, Config and service discovery from node.info (never the
// node key), command-center calls signed by the agent (cc.request), the direct devices from its
// registry, the node's domain handlers, utils and constants, and another package's command by
// the node's commands.custom_commands path.
func TestNodeModulesForPackages(t *testing.T) {
	py := python(t)
	root, err := filepath.Abs(filepath.Join("testdata", "packages", "nodeparity"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]any{"packages": []any{map[string]any{"name": "nodeparity", "root": root,
		"commands": []string{"commands/check/command.py"}}}})
	listing := filepath.Join(t.TempDir(), "commands.json")
	if err := os.WriteFile(listing, b, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	seven, nine := int64(7), int64(9)
	_ = st.SetSecret("EMAIL_ADDRESS", "a@x", "user", "string", &seven)
	_ = st.SetSecret("EMAIL_ADDRESS", "b@x", "user", "string", &nine)
	h := &compatHost{}
	sc, err := StartWith(context.Background(), py, listing, slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Storage: st, Host: h})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sc.Close() })
	if f := sc.Failed(); len(f) != 0 {
		t.Fatalf("failed = %v", f)
	}
	out, ok := sc.Call(context.Background(), "nodeparity_check", map[string]any{}, node.Request{})
	if !ok || out["success"] != true {
		t.Fatalf("check = %v", out)
	}
	node.TakeSignals(out)
	got, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]json.RawMessage
	_ = json.Unmarshal(got, &r)
	want := map[string]string{
		"secret":            `"http://ha.local"`,
		"user_secret_users": `[7,9]`,
		"auth":              `[true,"401",false]`,
		"domain_action":     `"turn_on"`,
		"config":            `["n1","hh1","",{"location":"Springfield, IL"}]`,
		"cc_url":            `"http://cc.local:7700"`,
		"rooms":             `[{"id":"r1","name":"Office"}]`,
		"llm":               `"hello back"`,
		"suppressions":      `{"descriptors":[],"source_keys":[]}`,
		"memories":          `{"injected":1}`,
		"direct":            `[true,false]`,
		"direct_control":    `"No adapter for protocol: nolan"`,
		"families":          `[]`,
		"secret_after_init": `"http://ha.local"`,
		"custom_command":    `"helper"`,
	}
	for k, v := range want {
		if string(r[k]) != v {
			t.Errorf("%s = %s, want %s", k, r[k], v)
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range []string{"node.info", "GET /api/v0/households/hh1/rooms", "POST /api/v0/node/llm/chat",
		"GET /api/v0/proposals/suppressions?command=get_news&user_id=7", "POST /api/v0/memories/inject",
		"devices.list", "audio.speak hello"} {
		if !slices.Contains(h.calls, c) {
			t.Errorf("the agent never got %q: %v", c, h.calls)
		}
	}
}
