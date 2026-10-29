package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/jpeg"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// Computer control (blueprint P3 pieces 6 and 7): the computer-task executor reads a page as a
// list of interactive elements with refs and acts on one element by its ref, in a task window
// opened behind the user's; it screenshots that window when elements aren't enough, and saves
// downloads (statements). These are not model tools: the executor (computer_observe, computer_act)
// calls them. Snapshot names and values are untrusted page content; whoever renders them for a
// model wraps them with Wrap.

// Snapshot limits (the extension applies the same).
const (
	DefaultSnapshotMax = 300
	MaxSnapshotMax     = 1000
	snapshotNameMax    = 120
	actValueMax        = 2000 // the extension's FILL_MAX
)

// Act operations and the keys the keys op sends.
var (
	ActOps  = []string{"press", "set_value", "select", "check", "focus", "scroll_into_view", "keys"}
	ActKeys = []string{"Enter", "Escape", "Tab", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight"}
)

var refPattern = regexp.MustCompile(`^e[1-9][0-9]{0,5}$`)

// ActResult is where the tab ended up after an act.
type ActResult struct {
	OK         bool   `json:"ok"`
	URLAfter   string `json:"url_after,omitempty"`
	TitleAfter string `json:"title_after,omitempty"`
}

// Snapshot lists the interactive elements of tab ("KEY:T", or a bare id for the default
// browser), at most max (0: DefaultSnapshotMax).
func (t *Tools) Snapshot(ctx context.Context, tab string, max int) (computeruse.Snapshot, error) {
	if max <= 0 {
		max = DefaultSnapshotMax
	}
	max = min(max, MaxSnapshotMax)
	s, _, err := t.snapshot(ctx, map[string]any{"tab_id": tab}, map[string]any{"max": max}, max)
	return s, err
}

// snapshot runs the extension's snapshot command (extra: max, and for voice viewport and wait)
// and returns it decoded and cleaned, with the raw result (its tab). Computer tasks and voice
// targeting share it, so both get the same limits and secret-field rules.
func (t *Tools) snapshot(ctx context.Context, args, extra map[string]any, max int) (computeruse.Snapshot, map[string]any, error) {
	res, err := t.call(ctx, "snapshot", args, extra)
	if err != nil {
		return computeruse.Snapshot{}, nil, err
	}
	var s computeruse.Snapshot
	if err := decode(res, &s); err != nil || s.Gen <= 0 {
		return computeruse.Snapshot{}, nil, fmt.Errorf("the extension's snapshot is malformed: %v", cmpErr(err, "no gen"))
	}
	if len(s.Elements) > max {
		s.Elements = s.Elements[:max]
	}
	for i := range s.Elements {
		e := &s.Elements[i]
		e.Name = clipRunes(e.Name, snapshotNameMax)
		e.Value = clipRunes(e.Value, snapshotNameMax)
		e.Context = clipRunes(e.Context, computeruse.ContextMax)
		if !computeruse.ValidPosition(e.Position) {
			e.Position = ""
		}
		if len(e.Target) > computeruse.TargetMax {
			e.Target = "" // a cut URL would point somewhere else
		}
		if secretField(*e) {
			e.Value = "" // never, even from an extension that sent it
		}
	}
	return s, res, nil
}

// secretField is snapCollect's secret(): a password, hidden, card or one-time-code field,
// whose value a snapshot never carries.
func secretField(e computeruse.SnapElement) bool {
	if e.Element().Secure || strings.EqualFold(e.Type, "hidden") {
		return true
	}
	for _, a := range strings.Fields(strings.ToLower(e.Autocomplete)) {
		if strings.HasPrefix(a, "cc-") || a == "one-time-code" {
			return true
		}
	}
	return false
}

// Act does op on the element ref ("e12") of snapshot gen in tab. value is set_value's text,
// select's option label or check's state ("false" unchecks); keys are the keys op's.
func (t *Tools) Act(ctx context.Context, tab string, gen int, ref, op, value string, keys []string) (ActResult, error) {
	switch {
	case gen <= 0:
		return ActResult{}, fmt.Errorf("%w: gen must be a snapshot's gen", errBadArgs)
	case !refPattern.MatchString(ref):
		return ActResult{}, fmt.Errorf("%w: ref must be a snapshot ref such as e12", errBadArgs)
	case !slices.Contains(ActOps, op):
		return ActResult{}, fmt.Errorf("%w: op must be one of %s", errBadArgs, strings.Join(ActOps, ", "))
	case op == "keys" && len(keys) == 0:
		return ActResult{}, fmt.Errorf("%w: keys is required", errBadArgs)
	case len([]rune(value)) > actValueMax:
		return ActResult{}, fmt.Errorf("%w: value is longer than %d characters", errBadArgs, actValueMax)
	}
	for _, k := range keys {
		if !slices.Contains(ActKeys, k) {
			return ActResult{}, fmt.Errorf("%w: keys may only be %s", errBadArgs, strings.Join(ActKeys, ", "))
		}
	}
	extra := map[string]any{"gen": gen, "ref": ref, "op": op}
	if value != "" {
		extra["value"] = value
	}
	if len(keys) > 0 {
		extra["keys"] = keys
	}
	res, err := t.call(ctx, "act", map[string]any{"tab_id": tab}, extra)
	if err != nil {
		return ActResult{}, err
	}
	var r ActResult
	if err := decode(res, &r); err != nil {
		return ActResult{}, fmt.Errorf("the extension's act result is malformed: %v", err)
	}
	return r, nil
}

// OpenBackground opens url in a new browser window that doesn't take focus, in the default
// browser, and returns its tab and window ("KEY:N" each).
func (t *Tools) OpenBackground(ctx context.Context, rawURL string) (tab, window string, err error) {
	u, err := webURL(rawURL)
	if err != nil {
		return "", "", err
	}
	// Not t.call: that drops window ids, which the executor needs (screenshots are per window).
	res, info, err := t.Hub.Call(ctx, 0, "open", map[string]any{"url": u, "background": true})
	var ce *CommandError
	if errors.As(err, &ce) && ce.Code == "unknown_command" {
		return "", "", outdated(info, "open a task window")
	}
	if err != nil {
		return "", "", err
	}
	w, wok := res["window_id"].(float64)
	tb, _ := res["tab"].(map[string]any)
	tid, tok := tb["tab_id"].(float64)
	if !wok || !tok {
		// An extension before 0.3.0 ignores background and opens a foreground tab.
		return "", "", outdated(info, "open a task window")
	}
	return fmt.Sprintf("%s:%d", info.Key, int64(tid)), fmt.Sprintf("%s:%d", info.Key, int64(w)), nil
}

// Screenshot captures the visible page of window ("KEY:N", from OpenBackground): a JPEG of the
// page viewport in physical pixels, and its geometry (origin 0,0 of the viewport, its CSS size,
// the device pixel ratio as Scale). The extension refuses a minimised window (window_minimized).
func (t *Tools) Screenshot(ctx context.Context, window string) (screen.Shot, computeruse.Geometry, error) {
	key, id, err := tabArg(map[string]any{"tab_id": window})
	if err != nil || id == nil {
		return screen.Shot{}, computeruse.Geometry{}, fmt.Errorf("%w: window must be a task window such as \"firefox:9\"", errBadArgs)
	}
	b, err := t.Hub.Lookup(key)
	if err != nil {
		return screen.Shot{}, computeruse.Geometry{}, err
	}
	// Not t.call: the command names a window, not a tab.
	res, info, err := t.Hub.Call(ctx, b.ID, "screenshot", map[string]any{"window_id": id})
	var ce *CommandError
	if errors.As(err, &ce) && ce.Code == "unknown_command" {
		return screen.Shot{}, computeruse.Geometry{}, outdated(info, "screenshot a task window")
	}
	if err != nil {
		return screen.Shot{}, computeruse.Geometry{}, err
	}
	var r struct {
		Data   string  `json:"data"`
		Width  int     `json:"width"`
		Height int     `json:"height"`
		DPR    float64 `json:"device_pixel_ratio"`
	}
	err = decode(res, &r)
	if err == nil && (r.Width <= 0 || r.Height <= 0 || r.DPR <= 0) {
		err = errors.New("no viewport size")
	}
	if err != nil {
		return screen.Shot{}, computeruse.Geometry{}, fmt.Errorf("the extension's screenshot is malformed: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil {
		return screen.Shot{}, computeruse.Geometry{}, fmt.Errorf("the extension's screenshot is malformed: %v", err)
	}
	img, err := jpeg.Decode(bytes.NewReader(raw))
	if err != nil {
		return screen.Shot{}, computeruse.Geometry{}, fmt.Errorf("the extension's screenshot is malformed: %v", err)
	}
	g := computeruse.Geometry{LogicalW: r.Width, LogicalH: r.Height, Scale: r.DPR,
		ImageW: img.Bounds().Dx(), ImageH: img.Bounds().Dy()}
	return screen.Shot{Image: img, App: info.Browser, Geometry: g}, g, nil
}

// downloadTimeout outwaits the extension: 10 s for a press to start a download, 60 s for it to
// complete.
var downloadTimeout = 75 * time.Second

// DownloadResult is a completed download: where the browser saved it.
type DownloadResult struct {
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	MIME     string `json:"mime,omitempty"`
	Filename string `json:"filename"`
}

// Download saves a file through the browser of tab: the one pressing ref (of the tab's last
// snapshot) starts, or url's. Exactly one of ref and url. It returns once the file is complete.
func (t *Tools) Download(ctx context.Context, tab, ref, rawURL string) (DownloadResult, error) {
	extra := map[string]any{}
	switch {
	case (ref == "") == (rawURL == ""):
		return DownloadResult{}, fmt.Errorf("%w: give either a ref or a url", errBadArgs)
	case ref != "" && !refPattern.MatchString(ref):
		return DownloadResult{}, fmt.Errorf("%w: ref must be a snapshot ref such as e12", errBadArgs)
	case ref != "":
		extra["ref"] = ref
	default:
		u, err := webURL(rawURL)
		if err != nil {
			return DownloadResult{}, err
		}
		extra["url"] = u
	}
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()
	res, err := t.call(ctx, "download", map[string]any{"tab_id": tab}, extra)
	if err != nil {
		return DownloadResult{}, err
	}
	var r DownloadResult
	if err := decode(res, &r); err != nil || r.Path == "" {
		return DownloadResult{}, fmt.Errorf("the extension's download result is malformed: %v", cmpErr(err, "no path"))
	}
	return r, nil
}

func decode(m map[string]any, v any) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func cmpErr(err error, dflt string) string {
	if err != nil {
		return err.Error()
	}
	return dflt
}

func clipRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
