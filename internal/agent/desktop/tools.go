// Package desktop turns host capabilities into node client tools, offered only while their
// capability is available, with the open policy applied here (host owns mechanism, this package
// owns what the LLM may ask for). Window verbs are one tool, desktop_window, whose action list is
// computed per call from the capabilities (docs/agent/DESKTOP-COMMANDS.md Q3).
package desktop

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/launches"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/winmatch"
)

// Tool names. Function names can't contain dots, so they don't spell the capability names,
// but each maps to exactly one capability.
const (
	ToolOpen      = "desktop_open"
	ToolGetVolume = "desktop_get_volume"
	ToolSetVolume = "desktop_set_volume"
	ToolList      = "desktop_list_windows"
	ToolWindow    = "desktop_window"
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
	// render, when set, builds the spec from the capabilities present now (false: not offered),
	// in place of spec + capability.
	render func(have map[string]bool) (node.Spec, bool)
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
	Log      *slog.Logger // nil: no logs
	tools    []tool
}

// New builds the toolset over a host and an apps catalogue (nil for none).
func New(h *host.Host, policy OpenPolicy, list Lister) *Tools {
	t := &Tools{Host: h, Policy: policy, Apps: list}
	winID := node.Param{Name: "window_id", Type: "string", Required: true,
		Description: "The id from desktop_list_windows, or the app or window name as the user said it ('TextEdit', 'the terminal')"}
	t.tools = []tool{
		{node.Spec{Name: ToolOpen, Description: "Open or launch an app ('open Steam', 'launch slack') or a link on the computer. For 'open X and type Y', open X, then desktop_type_text.",
			Params:   []node.Param{{Name: "target", Type: "string", Required: true, Description: "The app's name as said, or a URL"}},
			Keywords: []string{"open", "launch", "start", "app", "browser", "website", "computer", "desktop"},
			Examples: []node.Example{
				{VoiceCommand: "open youtube on my computer", Params: map[string]any{"target": "https://www.youtube.com"}, Primary: true},
				{VoiceCommand: "launch steam on my computer", Params: map[string]any{"target": "Steam"}},
			}},
			host.CapOpen, t.open, nil},
		{node.Spec{Name: ToolListApps, Description: "The apps installed on the computer. Use for 'what apps can you open' or 'do I have X', not to open one.",
			Params:   []node.Param{{Name: "query", Type: "string", Description: "Optional name to look up"}},
			Keywords: []string{"apps", "applications", "programs", "games", "installed", "available", "open", "computer", "desktop"},
			Examples: []node.Example{
				{VoiceCommand: "what apps can you open", Params: map[string]any{}, Primary: true},
				{VoiceCommand: "what apps are on my computer", Params: map[string]any{}},
				{VoiceCommand: "do I have steam installed", Params: map[string]any{"query": "steam"}},
			}},
			host.CapOpen, t.listApps, nil},
		{node.Spec{Name: ToolGetVolume, Description: "The computer's volume.",
			Keywords: []string{"volume", "computer", "desktop"},
			Examples: []node.Example{{VoiceCommand: "what's the volume on my computer", Params: map[string]any{}, Primary: true}}},
			host.CapVolumeGet, t.getVolume, nil},
		{node.Spec{Name: ToolSetVolume, Description: "Set the computer's volume.",
			Params:   []node.Param{{Name: "volume", Type: "int", Required: true, Description: "0-100"}},
			Keywords: []string{"volume", "louder", "quieter", "computer", "desktop"},
			Examples: []node.Example{{VoiceCommand: "set my computer volume to 30", Params: map[string]any{"volume": 30}, Primary: true}}},
			host.CapVolumeSet, t.setVolume, nil},
		{node.Spec{Name: ToolList, Description: "List the windows open on the user's computer: id, title, app, minimized, focused (newest first where known). Call it to answer what's open or what's in front.",
			Keywords: []string{"windows", "apps", "computer", "desktop"},
			Examples: []node.Example{{VoiceCommand: "what windows are open on my computer", Params: map[string]any{}, Primary: true}}},
			host.CapWindowsList, t.list, nil},
		{spec: node.Spec{Name: ToolWindow}, render: windowSpec, call: t.window},
		{node.Spec{Name: ToolClose, Description: "Ask one window on the user's computer to close, as if its close button were clicked. The app may prompt to save; it is never force-killed.", Params: []node.Param{winID},
			Keywords: []string{"close", "window"}, Risky: true},
			host.CapWindowsClose, t.windowAction(host.ActionClose), nil},
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
		if tl.render != nil {
			if spec, ok := tl.render(have); ok {
				out = append(out, spec.Render())
			}
		} else if have[tl.capability] {
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
			var we *windowError
			if errors.As(err, &we) {
				if len(we.match.Candidates) > 0 {
					o["candidates"] = we.match.Candidates // ask the user which one
				}
				if we.windows != nil {
					o["windows"] = we.windows // retry with an id from these, in the same turn
				}
			} else if errors.As(err, &amb) {
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
	var we *windowError
	switch {
	case errors.As(err, &we):
		return we.match.Code
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

// slowOutsideLaunch: desktop_open spending longer than this outside the launch itself is
// logged (the catalogue lookup and the launch record should take milliseconds).
var slowOutsideLaunch = time.Second

func (t *Tools) log() *slog.Logger {
	if t.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return t.Log
}

func (t *Tools) open(ctx context.Context, args map[string]any) (map[string]any, error) {
	start := time.Now()
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
	launchStart := time.Now()
	err = t.Host.LaunchApp(ctx, app.Path)
	if other := time.Since(start) - time.Since(launchStart); other > slowOutsideLaunch {
		t.log().Warn("desktop_open: slow outside the launch", "app", app.Name, "took", other.Round(time.Millisecond))
	}
	if err != nil {
		return nil, err
	}
	out := map[string]any{"launched": app.Name, "matched_app": app.Name, "note": "requested; launch is not confirmed"}
	if p.Fuzzy {
		out["fuzzy_match"], out["heard_as"] = true, p.HeardAs
	}
	return out, nil
}

// FindApp is the installed app a computer task names, near names included: only an app by
// name, never a link, a path or a URI, whatever --allow-open-any says (computer.Apps).
func (t *Tools) FindApp(name string) (apps.App, error) {
	kind, err := OpenPolicy{}.Classify(name)
	if err != nil {
		return apps.App{}, err
	}
	if kind != targetApp {
		return apps.App{}, host.Errorf(host.ErrInvalidTarget, "only an installed app's name can be opened here (got %q)", name)
	}
	list, err := t.catalogue()
	if err != nil {
		return apps.App{}, err
	}
	p, err := apps.Find(list, name)
	return p.App, err
}

// LaunchApp launches an app FindApp returned, by its catalogue path.
func (t *Tools) LaunchApp(ctx context.Context, app apps.App) error {
	t.Launched.Record(ctx, app.Name, app.ID)
	return t.Host.LaunchApp(ctx, app.Path)
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
		out := map[string]any{}
		if w, ok, err := t.resolveWindow(ctx, id); err != nil {
			return nil, err
		} else if ok && w.ID != id {
			id, out["matched_window"] = w.ID, windowName(w)
		}
		if err := t.Host.WindowDo(ctx, a, id); err != nil {
			return nil, err
		}
		out["window_id"], out["action"] = id, string(a)
		return out, nil
	}
}

// windowError is a window_id that names no single window, with the open windows so the model
// can retry with a real id in the same turn.
type windowError struct {
	match   *winmatch.Error
	windows []host.Window
}

func (e *windowError) Error() string { return e.match.Msg }
func (e *windowError) Unwrap() error { return e.match }

// resolveWindow: the model often passes a name or a guessed id ("text-edit-window",
// "terminal-1") instead of one from desktop_list_windows. An exact id is used as is; anything
// else is matched against the window list the way dictation names windows. ok is false when
// the list can't be read (the id is then passed through for the host to judge).
func (t *Tools) resolveWindow(ctx context.Context, id string) (host.Window, bool, error) {
	ws, err := t.Host.ListWindows(ctx)
	if err != nil {
		return host.Window{}, false, nil
	}
	cands := make([]winmatch.Candidate, len(ws))
	for i, w := range ws {
		if w.ID == id {
			return w, true, nil
		}
		cands[i] = winmatch.Candidate{Apps: []string{w.App}, Title: w.Title, Recency: i, Visible: !w.Minimized}
	}
	m, err := winmatch.PickUniqueMatch(cands, windowQuery(id), -1)
	if err != nil {
		var we *winmatch.Error
		if errors.As(err, &we) {
			return host.Window{}, false, &windowError{match: we, windows: ws}
		}
		return host.Window{}, false, err
	}
	return ws[m.Index], true, nil
}

// windowQuery turns a guessed id into words: "text-edit-window" → "text edit window",
// "terminal-1" → "terminal".
func windowQuery(id string) string {
	words := strings.FieldsFunc(id, func(r rune) bool { return r == '-' || r == '_' || unicode.IsSpace(r) })
	if n := len(words); n > 1 {
		if _, err := strconv.Atoi(words[n-1]); err == nil {
			words = words[:n-1]
		}
	}
	return strings.Join(words, " ")
}

// windowName is how a reply names the window acted on: "TextEdit — Untitled".
func windowName(w host.Window) string {
	if w.Title == "" {
		return w.App
	}
	return w.App + " — " + w.Title
}
