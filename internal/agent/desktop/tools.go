// Package desktop turns host capabilities into node client tools: one tool per capability,
// offered only while the capability is available, with the open policy applied here (host owns
// mechanism, this package owns what the LLM may ask for).
package desktop

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/launches"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Tool names. Function names can't contain dots, so they don't spell the capability names,
// but each maps to exactly one capability.
const (
	ToolOpen      = "desktop_open"
	ToolGetVolume = "desktop_get_volume"
	ToolSetVolume = "desktop_set_volume"
	ToolList      = "desktop_list_windows"
	ToolMinimize  = "desktop_minimize_window"
	ToolFocus     = "desktop_focus_window"
	ToolClose     = "desktop_close_window"
	ToolListApps  = "desktop_list_apps"
)

// OpenPolicy decides what desktop_open may hand to the OS: web and mail links, and installed
// applications by name (the apps catalogue). Never a path, a custom URI scheme or arguments
// from the model: an arbitrary target is program execution driven by an STT transcript.
type OpenPolicy struct {
	AllowAny bool // opt-in escape hatch (--allow-open-any); restores "anything the OS can open"
}

var openSchemes = map[string]bool{"http": true, "https": true, "mailto": true}

// targetKind classifies a desktop_open target.
type targetKind int

const (
	targetLink targetKind = iota // an allowed link: open it
	targetApp                    // a name: resolve it in the apps catalogue
	targetRaw                    // AllowAny: hand it to the OS as is
)

// Classify decides how target is handled, or refuses it.
func (p OpenPolicy) Classify(target string) (targetKind, error) {
	t := strings.TrimSpace(target)
	if t == "" {
		return 0, host.Errorf(host.ErrInvalidTarget, "empty target")
	}
	if p.AllowAny {
		return targetRaw, nil
	}
	if u, err := url.Parse(t); err == nil && u.Scheme != "" && len(u.Scheme) > 1 {
		if !openSchemes[strings.ToLower(u.Scheme)] {
			return 0, host.Errorf(host.ErrInvalidTarget, "only http(s) and mailto links, or an installed app's name, may be opened (got %q)", target)
		}
		if (u.Scheme == "http" || u.Scheme == "https") && u.Host == "" {
			return 0, host.Errorf(host.ErrInvalidTarget, "link has no host: %q", target)
		}
		return targetLink, nil
	}
	if strings.ContainsAny(t, `/\`) || strings.HasPrefix(t, "-") || strings.HasPrefix(t, "~") {
		return 0, host.Errorf(host.ErrInvalidTarget, "paths can't be opened; name an installed app instead (got %q)", target)
	}
	return targetApp, nil
}

type tool struct {
	spec       node.Spec
	capability string
	call       func(ctx context.Context, args map[string]any) (map[string]any, error)
}

// Lister returns the installed-app catalogue, computed now.
type Lister func() ([]apps.App, error)

// Tools is the desktop toolset.
type Tools struct {
	Host   *host.Host
	Policy OpenPolicy
	Apps   Lister // nil: no apps are launchable
	// Launched records each app launch, so typing into it can wait for its window (nil: not
	// recorded).
	Launched *launches.Tracker
	tools    []tool
}

// New builds the toolset over a host and an apps catalogue (nil for none).
func New(h *host.Host, policy OpenPolicy, list Lister) *Tools {
	t := &Tools{Host: h, Policy: policy, Apps: list}
	winID := node.Param{Name: "window_id", Type: "string", Required: true,
		Description: "Opaque window id exactly as returned by desktop_list_windows"}
	t.tools = []tool{
		{node.Spec{Name: ToolOpen, Description: "On the user's computer, open a web link in the default browser, or launch an installed app by name: \"open X\" or \"launch X\" on the computer. Pass the name as heard, even if it looks misspelled or unfamiliar: the computer matches near and sound-alike names itself (matched_app says which). If the result is ambiguous or not found, ask the user which of the candidates or suggestions they meant. The launch is not confirmed.",
			Params:   []node.Param{{Name: "target", Type: "string", Required: true, Description: "A full http(s) URL, e.g. https://example.com, or an app's name as the user said it, e.g. Steam"}},
			Keywords: []string{"open", "launch", "start", "app", "browser", "website", "computer", "desktop"},
			Examples: []node.Example{
				{VoiceCommand: "open youtube on my computer", Params: map[string]any{"target": "https://www.youtube.com"}, Primary: true},
				{VoiceCommand: "launch steam on my computer", Params: map[string]any{"target": "Steam"}},
			}},
			host.CapOpen, t.open},
		{node.Spec{Name: ToolListApps, Description: "List the apps installed on the user's computer, i.e. the apps desktop_open can open. ALWAYS call this to answer any question about what is installed, available or openable on the computer (\"what apps can you open?\", \"do I have Steam?\", \"which games are installed?\"). Never answer those from general knowledge: only this list is accurate for this computer. With query, it looks a name up (as heard; it may be misheard) and returns the closest apps and best_match.",
			Params:   []node.Param{{Name: "query", Type: "string", Description: "Optional: a name or word to look up, e.g. steam"}},
			Keywords: []string{"apps", "applications", "programs", "games", "installed", "available", "open", "computer", "desktop"},
			Examples: []node.Example{
				{VoiceCommand: "what apps can you open", Params: map[string]any{}, Primary: true},
				{VoiceCommand: "what apps are on my computer", Params: map[string]any{}},
				{VoiceCommand: "do I have steam installed", Params: map[string]any{"query": "steam"}},
			}},
			host.CapOpen, t.listApps},
		{node.Spec{Name: ToolGetVolume, Description: "Get the computer's master volume (0-100).",
			Keywords: []string{"volume", "computer", "desktop"},
			Examples: []node.Example{{VoiceCommand: "what's the volume on my computer", Params: map[string]any{}, Primary: true}}},
			host.CapVolumeGet, t.getVolume},
		{node.Spec{Name: ToolSetVolume, Description: "Set the computer's master volume. Out-of-range values are clamped to 0-100; the result says the level applied.",
			Params:   []node.Param{{Name: "volume", Type: "int", Required: true, Description: "Desired volume, 0-100"}},
			Keywords: []string{"volume", "louder", "quieter", "computer", "desktop"},
			Examples: []node.Example{{VoiceCommand: "set my computer volume to 30", Params: map[string]any{"volume": 30}, Primary: true}}},
			host.CapVolumeSet, t.setVolume},
		{node.Spec{Name: ToolList, Description: "List the windows open on the user's computer: id, title, app, minimized. Call it to answer what's open on screen, and before minimizing, focusing or closing a window, to get its id. Never invent ids.",
			Keywords: []string{"windows", "apps", "computer", "desktop"},
			Examples: []node.Example{{VoiceCommand: "what windows are open on my computer", Params: map[string]any{}, Primary: true}}},
			host.CapWindowsList, t.list},
		{node.Spec{Name: ToolMinimize, Description: "Minimize one window on the user's computer.", Params: []node.Param{winID},
			Keywords: []string{"minimize", "hide", "window"}},
			host.CapWindowsMinimize, t.windowAction(host.ActionMinimize)},
		{node.Spec{Name: ToolFocus, Description: "Bring one window on the user's computer to the front.", Params: []node.Param{winID},
			Keywords: []string{"focus", "switch to", "window"}},
			host.CapWindowsFocus, t.windowAction(host.ActionFocus)},
		{node.Spec{Name: ToolClose, Description: "Ask one window on the user's computer to close, as if its close button were clicked. The app may prompt to save; it is never force-killed.", Params: []node.Param{winID},
			Keywords: []string{"close", "window"}, Risky: true},
			host.CapWindowsClose, t.windowAction(host.ActionClose)},
	}
	return t
}

// Available offers each tool only while its capability is present, computed now.
func (t *Tools) Available(ctx context.Context) []node.ToolDef {
	caps, err := t.Host.Capabilities(ctx)
	if err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, c := range caps {
		have[c] = true
	}
	var out []node.ToolDef
	for _, tl := range t.tools {
		if have[tl.capability] {
			out = append(out, tl.spec.Render())
		}
	}
	return out
}

// Call runs a tool. Failures are outputs ({"success": false, "error", "error_code"}), so the
// LLM can tell the user what happened.
func (t *Tools) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	for _, tl := range t.tools {
		if tl.spec.Name != name {
			continue
		}
		out, err := tl.call(ctx, args)
		if err != nil {
			o := node.Fail("%v", err)
			o["error_code"] = code(err)
			var amb *apps.AmbiguousError
			var nf *apps.NotFoundError
			if errors.As(err, &amb) {
				o["candidates"] = amb.Candidates // ask the user which one
			} else if errors.As(err, &nf) && len(nf.Suggestions) > 0 {
				o["suggestions"] = nf.Suggestions
			}
			return o, true
		}
		out["success"] = true
		return out, true
	}
	return nil, false
}

func code(err error) string {
	switch {
	case errors.Is(err, host.ErrNotSupported):
		return "not_supported"
	case errors.Is(err, host.ErrWindowNotFound):
		return "window_not_found"
	case errors.Is(err, host.ErrInvalidTarget), errors.Is(err, errBadArgs):
		return "invalid_argument"
	case errors.As(err, new(*apps.AmbiguousError)):
		return "ambiguous"
	case errors.Is(err, apps.ErrNotFound):
		return "app_not_found"
	}
	return "failed"
}

var errBadArgs = errors.New("bad arguments")

func (t *Tools) open(ctx context.Context, args map[string]any) (map[string]any, error) {
	target, _ := args["target"].(string)
	kind, err := t.Policy.Classify(target)
	if err != nil {
		return nil, err
	}
	if kind != targetApp {
		if err := t.Host.Open(ctx, strings.TrimSpace(target)); err != nil {
			return nil, err
		}
		return map[string]any{"opened": target, "note": "requested; launch is not confirmed"}, nil
	}
	list, err := t.catalogue()
	if err != nil {
		return nil, err
	}
	p, err := apps.Find(list, target)
	if err != nil {
		return nil, err
	}
	app := p.App
	t.Launched.Record(ctx, app.Name, app.ID)
	// The path launched is the catalogue's, never the model's.
	if err := t.Host.LaunchApp(ctx, app.Path); err != nil {
		return nil, err
	}
	out := map[string]any{"launched": app.Name, "matched_app": app.Name, "note": "requested; launch is not confirmed"}
	if p.Fuzzy {
		out["fuzzy_match"], out["heard_as"] = true, p.HeardAs
	}
	return out, nil
}

func (t *Tools) catalogue() ([]apps.App, error) {
	if t.Apps == nil {
		return nil, nil
	}
	return t.Apps()
}

// maxClosest is how many near names a lookup returns.
const maxClosest = 3

// maxListed caps desktop_list_apps so a big Start Menu can't flood the model's context.
const maxListed = 300

func (t *Tools) listApps(_ context.Context, args map[string]any) (map[string]any, error) {
	list, err := t.catalogue()
	if err != nil {
		return nil, err
	}
	raw, _ := args["query"].(string)
	raw = strings.TrimSpace(raw)
	q := strings.ToLower(raw)
	names := []string{}
	for _, a := range list {
		if q == "" || strings.Contains(strings.ToLower(a.Name), q) {
			names = append(names, a.Name)
		}
	}
	out := map[string]any{"count": len(names)}
	if len(names) > maxListed {
		names = names[:maxListed]
		out["truncated"] = true
	}
	out["apps"] = names
	if q != "" {
		// A lookup: the closest names by spelling or sound, and the one app it resolves to.
		out["closest"] = apps.Closest(list, raw, maxClosest)
		if p, err := apps.Find(list, raw); err == nil {
			out["best_match"] = p.App.Name
			if p.Fuzzy {
				out["heard_as"] = p.HeardAs
			}
		}
	}
	return out, nil
}

func (t *Tools) getVolume(ctx context.Context, _ map[string]any) (map[string]any, error) {
	v, err := t.Host.GetVolume(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"volume": v}, nil
}

// intArg accepts a JSON number or a numeric string (small models send "30"). No range check:
// out-of-range clamps in host.SetVolume.
func intArg(args map[string]any, k string) (int, error) {
	switch v := args[k].(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			break
		}
		return int(math.Max(math.Min(math.Round(v), 1e6), -1e6)), nil
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(v), "%"), 64); err == nil {
			return intArg(map[string]any{k: f}, k)
		}
	}
	return 0, fmt.Errorf("%w: %s must be a number", errBadArgs, k)
}

func (t *Tools) setVolume(ctx context.Context, args map[string]any) (map[string]any, error) {
	level, err := intArg(args, "volume")
	if err != nil {
		return nil, err
	}
	applied, err := t.Host.SetVolume(ctx, level)
	if err != nil {
		return nil, err
	}
	return map[string]any{"volume": applied, "requested": level}, nil
}

func (t *Tools) list(ctx context.Context, _ map[string]any) (map[string]any, error) {
	ws, err := t.Host.ListWindows(ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"windows": ws}, nil
}

func (t *Tools) windowAction(a host.WindowAction) func(context.Context, map[string]any) (map[string]any, error) {
	return func(ctx context.Context, args map[string]any) (map[string]any, error) {
		id, _ := args["window_id"].(string)
		if id == "" {
			return nil, fmt.Errorf("%w: window_id is required", errBadArgs)
		}
		if err := t.Host.WindowDo(ctx, a, id); err != nil {
			return nil, err
		}
		return map[string]any{"window_id": id, "action": string(a)}, nil
	}
}
