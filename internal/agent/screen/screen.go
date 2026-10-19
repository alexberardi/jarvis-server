// Package screen is "read the screen" (docs/agent/DESIGN.md §12): on request, a screenshot of
// the focused window (or the screen) goes to the model as an image attached to the tool result.
//
// Privacy: a capture lives in memory only. Nothing is written to disk (macOS's screencapture
// needs a file: a 0600 file in a private 0700 directory, removed before Capture returns), and
// nothing about the image is logged. command-center shows it to the model for that turn only.
package screen

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // grim and screencapture output
	"log/slog"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/image/draw"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// ToolRead is the tool's name.
const ToolRead = "desktop_read_screen"

// AttachmentsKey is the tool-output key command-center lifts images from (docs/cc/chat-images.md
// §10): a list of {"mime", "data" (base64), "label"}. Any client tool may use it.
const AttachmentsKey = "image_attachments"

// Scopes.
const (
	ScopeWindow = "window"
	ScopeScreen = "screen"
)

// Limits: the longest side a capture is scaled down to (vision models tile images; ~1568 px
// keeps a screen readable at about 1.4k image tokens), and the encoded size cap command-center
// also enforces (chatMaxImageBytes).
const (
	MaxSide  = 1568
	MaxBytes = 2 << 20
)

// ErrUnavailable: this desktop can't be captured (no supported compositor, no permission).
var ErrUnavailable = errors.New("screen capture unavailable")

// Shot is one capture before encoding: the image, what it shows and where it is. Geometry's
// LogicalW is 0 when the capturer doesn't know; ImageW/ImageH are set by EncodeShot.
type Shot struct {
	Image    image.Image
	Title    string // window title (window scope), or "" (screen)
	App      string
	Geometry computeruse.Geometry
}

// physicalScale fills g.Scale from the image when the compositor didn't say: a capture is in
// physical pixels, so it is the image's width over the logical width (to 0.01).
func physicalScale(g computeruse.Geometry, img image.Image) computeruse.Geometry {
	if g.Scale <= 0 && g.LogicalW > 0 && img != nil {
		g.Scale = math.Round(float64(img.Bounds().Dx())/float64(g.LogicalW)*100) / 100
	}
	return g
}

// Capturer is one platform's capture.
type Capturer interface {
	// Available says whether Capture can work now, and why not when it can't. Computed per call
	// (a macOS grant can change; a session can come and go).
	Available(ctx context.Context) (bool, string)
	// Capture takes the focused window (scope window), the screen (scope screen), or the
	// window the user named when window is set (whatever scope says). A named window that
	// can't be captured is a *WindowError, never a fallback to another window or the screen.
	Capture(ctx context.Context, scope, window string) (Shot, error)
}

// Exec is the slice of the OS the shell-out capturers use, injected for tests.
type Exec interface {
	LookPath(name string) bool
	Getenv(key string) string
	// Output runs a command and returns its stdout (binary safe). A non-zero exit is an error
	// carrying stderr.
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// OSExec is the real Exec.
type OSExec struct{}

func (OSExec) LookPath(name string) bool { _, err := exec.LookPath(name); return err == nil }

func (OSExec) Getenv(key string) string { return os.Getenv(key) }

func (OSExec) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("%s: %s", name, msg)
	}
	return out.Bytes(), nil
}

// Tools is the screen toolset: one tool, offered only while capture works.
type Tools struct {
	C Capturer
	// Hide, when set, takes our own overlay (the island) off the screen for a capture and
	// returns what brings it back.
	Hide func() (restore func())
	// Log gets one Info line per call: scope, the window asked for, what was captured (app and
	// a shortened title). Never anything about the image. Nil: no log.
	Log *slog.Logger
}

func spec() node.Spec {
	return node.Spec{Name: ToolRead,
		Description: "Look at the user's computer screen: takes a screenshot of a window (or the whole screen) and shows it " +
			"to you with this result. Call it whenever the user refers to something on their screen and you need to see it: " +
			"\"what does this error say\", \"summarise this window\", \"read the firefox window\", \"what's in my terminal\", " +
			"\"what's on my screen\". When the user names an app or window (\"the firefox window\", \"the terminal\", \"slack\"), " +
			"put that name in window: it may not be the focused one. Without a name you get the focused window. Answer from " +
			"what you see; quote short text exactly. If it fails, tell the user what the error says. Only when asked: never " +
			"call it unprompted. The screenshot is not kept after this turn.",
		Params: []node.Param{
			{Name: "scope", Type: "string", Enum: []string{ScopeWindow, ScopeScreen},
				Description: "window (default): one window. screen: the whole screen, only when the user says screen or names no window and one window isn't enough"},
			{Name: "window", Type: "string",
				Description: "the app or window the user named, in their words: \"firefox\", \"terminal\", \"slack\", or words from its title. Leave it out when they mean whatever is in front (\"this error\", \"this window\")"},
		},
		Keywords: []string{"screen", "screenshot", "window", "error", "read", "see", "look", "summarise", "computer"},
		Examples: []node.Example{
			{VoiceCommand: "what does this error say", Params: map[string]any{"scope": ScopeWindow}, Primary: true},
			{VoiceCommand: "read the firefox window", Params: map[string]any{"scope": ScopeWindow, "window": "firefox"}},
			{VoiceCommand: "what does it say in my terminal", Params: map[string]any{"scope": ScopeWindow, "window": "terminal"}},
			{VoiceCommand: "summarise this window", Params: map[string]any{"scope": ScopeWindow}},
			{VoiceCommand: "what's on my screen", Params: map[string]any{"scope": ScopeScreen}},
		},
	}
}

// Available offers the tool only while capture works.
func (t *Tools) Available(ctx context.Context) []node.ToolDef {
	if t.C == nil {
		return nil
	}
	if ok, _ := t.C.Available(ctx); !ok {
		return nil
	}
	return []node.ToolDef{spec().Render()}
}

// Call captures, scales and attaches. Failures are outputs, so the model can say what happened.
func (t *Tools) Call(ctx context.Context, name string, args map[string]any, _ node.Request) (map[string]any, bool) {
	if name != ToolRead {
		return nil, false
	}
	if t.C == nil {
		return failure(ErrUnavailable.Error(), "not_supported"), true
	}
	if ok, why := t.C.Available(ctx); !ok {
		return failure(why, "not_supported"), true
	}
	scope, _ := args["scope"].(string)
	scope = strings.ToLower(strings.TrimSpace(scope))
	if scope != ScopeScreen {
		scope = ScopeWindow
	}
	window, _ := args["window"].(string)
	window = strings.TrimSpace(window)
	if normalizeQuery(window) == "" {
		window = ""
	} else {
		scope = ScopeWindow // a named window is the more specific ask
	}
	log := t.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	restore := func() {}
	if t.Hide != nil {
		restore = t.Hide()
	}
	shot, err := t.C.Capture(ctx, scope, window)
	restore()
	if err != nil {
		var we *WindowError
		if errors.As(err, &we) {
			log.Info("read screen", "scope", scope, "window", window, "error_code", we.Code)
			return failure(we.Msg, we.Code), true
		}
		log.Info("read screen", "scope", scope, "window", window, "error_code", "capture_failed", "err", err)
		return failure(err.Error(), "capture_failed"), true
	}
	log.Info("read screen", "scope", scope, "window", window, "app", shot.App, "title", node.Truncate(shot.Title, 40))
	data, err := EncodeShot(&shot)
	w, h := shot.Geometry.ImageW, shot.Geometry.ImageH
	if err != nil {
		return failure(err.Error(), "capture_failed"), true
	}
	label := "screenshot of the screen"
	if scope == ScopeWindow && shot.Title != "" {
		label = "screenshot of the window " + quote(shot.Title)
	} else if scope == ScopeWindow && shot.App != "" {
		label = "screenshot of the " + shot.App + " window"
	}
	out := map[string]any{"success": true, "scope": scope, "width": w, "height": h,
		AttachmentsKey: []any{map[string]any{"mime": "image/jpeg", "data": base64.StdEncoding.EncodeToString(data), "label": label}}}
	if shot.Title != "" {
		out["window_title"] = shot.Title
	}
	if shot.App != "" {
		out["app"] = shot.App
	}
	if g := shot.Geometry; g.LogicalW > 0 && g.LogicalH > 0 {
		// Where the image is on the desktop: image pixel (x, y) is logical global pixel
		// origin + (x*logical_w/width, y*logical_h/height).
		out["capture"] = map[string]any{"origin_x": g.OriginX, "origin_y": g.OriginY,
			"logical_w": g.LogicalW, "logical_h": g.LogicalH, "scale": g.Scale, "monitor": g.Monitor}
	}
	return out, true
}

func failure(msg, code string) map[string]any {
	o := node.Fail("%s", msg)
	o["error_code"] = code
	return o
}

func quote(s string) string {
	if r := []rune(s); len(r) > 80 {
		s = string(r[:80]) + "…"
	}
	return "\"" + s + "\""
}

// Encode scales img so its longest side is at most MaxSide and encodes it as JPEG under
// MaxBytes, lowering the quality if it has to.
func Encode(img image.Image) ([]byte, int, int, error) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, 0, 0, errors.New("empty capture")
	}
	if long := max(w, h); long > MaxSide {
		w, h = max(1, w*MaxSide/long), max(1, h*MaxSide/long)
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
		img = dst
	}
	for _, q := range []int{85, 70, 55} {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
			return nil, 0, 0, err
		}
		if buf.Len() <= MaxBytes {
			return buf.Bytes(), w, h, nil
		}
	}
	return nil, 0, 0, errors.New("capture too large to send")
}

// EncodeShot encodes shot.Image like Encode and records the size the model sees in
// shot.Geometry (ImageW, ImageH), so a point on that image maps back to the screen.
func EncodeShot(shot *Shot) ([]byte, error) {
	data, w, h, err := Encode(shot.Image)
	if err != nil {
		return nil, err
	}
	shot.Geometry.ImageW, shot.Geometry.ImageH = w, h
	return data, nil
}

// decode reads a PNG/JPEG capture.
func decode(b []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("unreadable capture: %w", err)
	}
	return img, nil
}
