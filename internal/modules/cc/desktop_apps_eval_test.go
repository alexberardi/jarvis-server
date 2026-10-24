package cc

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/launches"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
)

// TestDesktopAppsLive plays the user's real desktop voice failures (2026-10-14, DESIGN §14)
// against a real model through the real turn pipeline, with the desktop agent's own tools
// (open, list apps, type) over fakes: a real desktop's 62-app catalogue, windows that appear
// when an app is launched, a keyboard that records. A smart-home tool competes for "open X".
//
//	JARVIS_DESKTOP_EVAL_LLM=http://127.0.0.1:39555 \
//	  go test -run TestDesktopAppsLive -v -timeout 60m ./internal/modules/cc/
//
// The engine is a llama-server running the household's model with jarvisd's pinned template.
func TestDesktopAppsLive(t *testing.T) {
	base := os.Getenv("JARVIS_DESKTOP_EVAL_LLM")
	if base == "" {
		t.Skip("JARVIS_DESKTOP_EVAL_LLM not set")
	}
	only := os.Getenv("JARVIS_DESKTOP_EVAL_ONLY")
	for _, c := range []struct {
		name string
		wake [2]string // a scripted wake exchange first ("": none)
		say  string
		// want: the app launched, and the app typed into ("" for none).
		launch, typeInto string
	}{
		{name: "omerite_note", say: "Can you open Omerite and drop down a note to update Steam?", launch: "Omawrite", typeInto: "omawrite"},
		{name: "neovim_wake", say: "NeoVim, can you open it?", launch: "Neovim"},
		{name: "neovim_followup", wake: [2]string{"Can you open the oven?",
			"I can't control the oven directly, but I can help you find the right device in your Home Assistant if you'd like. Would you like me to look that up?"},
			say: "NeoVim, can you open it?", launch: "Neovim"},
		{name: "neovim_type", say: "Open neovim and type hello world.", launch: "Neovim", typeInto: "Neovim"},
		{name: "oven", say: "Can you open the oven?"}, // a real oven: nothing launched
	} {
		if only != "" && !strings.Contains(c.name, only) {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			d := newEvalDesktop()
			ve := newVoiceEnv(t, prompts.Qwen3_5_9B)
			ve.eng.strict.Store(true) // jarvisd's pinned template (ID12)
			ve.eng.ctxLen = 32768
			// As the agent starts a voice conversation: its tools, and its app names for whisper.
			nc, _ := json.Marshal(map[string]any{"timezone": "UTC", "stt_vocabulary": d.vocabulary()})
			ve.do("POST", "/api/v0/conversation/start", `{"conversation_id":"c1","node_context":`+string(nc)+
				`,"client_tools":`+d.clientTools(t)+`}`, ve.node.h()).want(200)
			extra := map[string]any{"turn_source": "wake", "wake_confidence": 0.9}
			if c.wake[0] != "" {
				scriptWake(t, ve, "c1", c.wake)
				extra = map[string]any{"turn_source": "follow_up", "follow_up_iteration": 1}
			}
			ve.eng.mu.Lock()
			ve.eng.respond = realEngine(t, base)
			ve.eng.mu.Unlock()
			start := time.Now()
			b := ve.turn("/api/v0/voice/command", "c1", c.say, extra).want(200).json()
			var trail []string
			for round := 0; b["stop_reason"] == "tool_calls" && round < 5; round++ {
				var results []map[string]any
				calls, _ := b["tool_calls"].([]any)
				for _, x := range calls {
					call := x.(map[string]any)
					fn := call["function"].(map[string]any)
					name, _ := fn["name"].(string)
					args := node.DecodeArgs(fn["arguments"])
					out := d.call(name, args)
					a, _ := json.Marshal(args)
					trail = append(trail, name+string(a))
					results = append(results, map[string]any{"tool_call_id": call["id"], "output": out})
				}
				b = ve.turn("/api/v0/voice/command/continue", "c1", "", map[string]any{"tool_results": results}).want(200).json()
			}
			t.Logf("%.0fs %q -> %s | stop=%v reply=%q", time.Since(start).Seconds(), c.say, strings.Join(trail, " -> "),
				b["stop_reason"], b["assistant_message"])
			if b["stop_reason"] == "not_for_me" {
				t.Errorf("silently dropped as not for me")
			}
			if c.launch != "" && (len(d.launched) == 0 || d.launched[len(d.launched)-1] != c.launch) {
				t.Errorf("launched %v, want %s", d.launched, c.launch)
			}
			if c.launch == "" && len(d.launched) > 0 {
				t.Errorf("launched %v", d.launched)
			}
			if c.typeInto != "" && (len(d.typedInto) == 0 || !strings.EqualFold(d.typedInto[len(d.typedInto)-1], c.typeInto)) {
				t.Errorf("typed into %v, want %s", d.typedInto, c.typeInto)
			}
		})
	}
}

// evalDesktopApps is a real Linux desktop's catalogue (the user's, 2026-10-13).
const evalDesktopApps = "Aether|Avahi SSH Server Browser|Avahi VNC Server Browser|Avahi Zeroconf Browser|Basecamp|btop++|" +
	"Chromium|cliamp|Cyberpunk 2077|Deskflow|Discord|Disk Usage|Disks|Docker|Document Viewer|Fcitx 5 Configuration|Files|" +
	"Firefox|Foot|Foot Client|Foot Server|Google Contacts|Google Maps|Google Messages|Google Photos|Hardware Locality lstopo|" +
	"HEY|Image Viewer|Kdenlive|LibreOffice|LibreOffice Base|LibreOffice Calc|LibreOffice Draw|LibreOffice Impress|" +
	"LibreOffice Math|LibreOffice Writer|Limine-snapper-restore|LocalSend|Manage Printing|Media Player|Moonlight|Neovim|" +
	"OBS Studio|Obsidian|Omacalc|Omacut|Omawrite|Pinta|Print Settings|Qt V4L2 test Utility|Qt V4L2 video capture utility|" +
	"Red Dead Redemption|Steam|Sunshine|Tensaku|The Witcher 3: Wild Hunt — Remastered|uuctl|WhatsApp|X|Xournal++|YouTube|Zoom"

// evalDesktop is the agent's desktop and dictation tools over fakes: launching an app opens
// its window (a terminal's, for Neovim), typing records which window had focus.
type evalDesktop struct {
	ts        node.Multi
	dt        *dictate.Tools
	win       *evalWindows
	launched  []string
	typedInto []string
	smart     []string
}

func newEvalDesktop() *evalDesktop {
	d := &evalDesktop{win: &evalWindows{active: "term", ws: []dictate.Window{
		{ID: "term", Apps: []string{"Alacritty"}, Title: "~", Visible: true},
		{ID: "ff", Apps: []string{"firefox"}, Title: "Docs", Recency: 1, Visible: true},
	}}}
	var list []apps.App
	for _, n := range strings.Split(evalDesktopApps, "|") {
		list = append(list, apps.App{Name: n, Path: "/apps/" + n + ".desktop"})
	}
	b := &evalBackend{d: d}
	dt := &dictate.Tools{KB: &evalKB{d: d}, Win: d.win, Apps: func() ([]apps.App, error) { return list, nil },
		Sleep: func(context.Context, time.Duration) error { return nil }}
	dk := desktop.New(host.New(b), desktop.OpenPolicy{}, func() ([]apps.App, error) { return list, nil })
	tr := &launches.Tracker{Windows: func(ctx context.Context) ([]string, error) {
		var ids []string
		for _, w := range d.win.ws {
			ids = append(ids, w.ID)
		}
		return ids, nil
	}}
	dk.Launched, dt.Launched = tr, tr
	d.ts, d.dt = node.Multi{dk, dt}, dt
	return d
}

func (d *evalDesktop) vocabulary() string { return d.dt.Vocabulary(context.Background()) }

// evalSmartHome is the competing smart-home tool ("the oven").
var evalSmartHome = node.Spec{Name: "control_device", Description: "Turn a smart-home device (lights, switches, " +
	"oven, thermostat, locks) on or off, or open/close it.",
	Params: []node.Param{{Name: "device", Type: "string", Required: true, Description: "The device's name"},
		{Name: "action", Type: "string", Required: true, Description: "on, off, open or close"}}}

func (d *evalDesktop) clientTools(t *testing.T) string {
	var tools []any
	for _, def := range d.ts.Available(context.Background()) {
		tools = append(tools, def.ClientTool)
	}
	tools = append(tools, evalSmartHome.Render().ClientTool)
	raw, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (d *evalDesktop) call(name string, args map[string]any) map[string]any {
	if name == evalSmartHome.Name {
		dev, _ := args["device"].(string)
		d.smart = append(d.smart, dev)
		return map[string]any{"success": false, "error": "no device named " + dev}
	}
	out, ok := d.ts.Call(context.Background(), name, args, node.Request{})
	if !ok {
		return map[string]any{"success": false, "error": "unknown tool " + name}
	}
	return out
}

type evalBackend struct{ d *evalDesktop }

func (b *evalBackend) Platform() string { return "linux" }
func (b *evalBackend) Capabilities(context.Context) []string {
	return []string{"open"}
}
func (b *evalBackend) GetVolume(context.Context) (int, error)             { return 50, nil }
func (b *evalBackend) ApplyVolume(context.Context, int) error             { return nil }
func (b *evalBackend) ListWindows(context.Context) ([]host.Window, error) { return nil, nil }
func (b *evalBackend) MinimizeWindow(context.Context, string) error       { return nil }
func (b *evalBackend) FocusWindow(context.Context, string) error          { return nil }
func (b *evalBackend) CloseWindow(context.Context, string) error          { return nil }
func (b *evalBackend) ArrangeWindow(context.Context, string, host.WindowAction, string) error {
	return nil
}
func (b *evalBackend) Open(context.Context, string) error { return nil }
func (b *evalBackend) LaunchApp(_ context.Context, p string) error {
	name := strings.TrimSuffix(strings.TrimPrefix(p, "/apps/"), ".desktop")
	b.d.launched = append(b.d.launched, name)
	w := dictate.Window{ID: "w-" + name, Apps: []string{strings.ToLower(name)}, Title: "untitled", Visible: true}
	if name == "Neovim" {
		w.Apps = []string{"Alacritty"} // a terminal app's window is the terminal's
		w.Title = "nvim"
	}
	b.d.win.ws = append(b.d.win.ws, w)
	return nil
}

type evalWindows struct {
	ws     []dictate.Window
	active string
}

func (w *evalWindows) Available(context.Context) (bool, string)       { return true, "" }
func (w *evalWindows) List(context.Context) ([]dictate.Window, error) { return w.ws, nil }
func (w *evalWindows) Focus(_ context.Context, id string) error       { w.active = id; return nil }
func (w *evalWindows) Active(context.Context) (string, error)         { return w.active, nil }
func (w *evalWindows) Same(id, active string) bool                    { return id == active }

type evalKB struct{ d *evalDesktop }

func (k *evalKB) Name() string                             { return "eval" }
func (k *evalKB) Available(context.Context) (bool, string) { return true, "" }
func (k *evalKB) Press(context.Context, dictate.Key) error { return nil }
func (k *evalKB) Type(context.Context, string) error {
	into := k.d.win.active
	for _, w := range k.d.win.ws {
		if w.ID == into {
			into = strings.TrimPrefix(w.ID, "w-")
		}
	}
	k.d.typedInto = append(k.d.typedInto, into)
	return nil
}
