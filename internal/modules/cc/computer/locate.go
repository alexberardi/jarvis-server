package computer

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png" // screenshots arrive as PNG or JPEG
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/image/draw"

	"github.com/alexberardi/jarvis-server/internal/computeruse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// SettingLocateClicks has a click the model repeats, aimed from a screenshot, placed by a
// question of its own ("on": where is this? on the screenshot, then on a close-up of that spot). The Qwen3.5 9B
// answers that within a few pixels, and misses by tens of pixels when the same click is one
// line of a whole round's answer (2026-11-15: it clicked 15 to 70 px above a Save button, forty
// times). It costs two short model calls a click.
const SettingLocateClicks = "computer.locate_clicks"

// The close-up: a square of the screenshot around the first answer, shown enlarged.
const (
	locateCloseUp  = 800 // the close-up's side in pixels, as the model sees it
	locateMinSide  = 320 // the square's side on the screenshot, at least
	locateMaxToken = 96
)

// locatable are the kinds a click's place is asked for: a drag has two ends and its own path.
var locatable = map[string]bool{"click": true, "double_click": true, "triple_click": true, "move": true}

const locateQuestion = `This is %s of an app. Jarvis is about to do this step with the mouse: %q. Where exactly should the pointer go? %s Answer with JSON only: {"x": <number>, "y": <number>}, the centre of the control, or {"absent": true} if it is not in the image.`

// locate is where on the newest screenshot a's click belongs (image pixels), asked on its own;
// ok is false when the click isn't one to ask about, the setting is off, or the model can't
// place it (the round's own coordinates then stand).
func (l *Loop) locate(ctx context.Context, r *round, a computeruse.Action) (x, y int, ok bool) {
	if a.X == nil || a.Ref != "" || a.Intent == "" || !locatable[a.Kind] || len(r.shots) == 0 || l.Settings == nil {
		return 0, 0, false
	}
	if l.Settings.String(ctx, SettingLocateClicks, settings.Scope{HouseholdID: r.task.HouseholdID}) != "on" {
		return 0, 0, false
	}
	// Only a click the model is making again: its own aim is good on wide controls (a text
	// field), where the close-up does worse, and a miss on a small one shows as a repeat.
	if !clickedBefore(r.run.History, r.run.Rounds, a.Intent) {
		return 0, 0, false
	}
	shot := r.shots[len(r.shots)-1]
	fx, fy, found := l.locateIn(ctx, r, a.Intent, "a screenshot", shot.MIME, shot.Data, shot.Width, shot.Height)
	if !found {
		return 0, 0, false
	}
	x, y = fx, fy
	// The close-up settles a small control: the first answer can sit on its edge.
	img, _, err := image.Decode(bytes.NewReader(shot.Data))
	if err != nil {
		return x, y, true
	}
	b := img.Bounds()
	side := min(max(locateMinSide, min(b.Dx(), b.Dy())/3), b.Dx(), b.Dy())
	// The screenshot's own pixels may differ from Width x Height (a downscaled capture).
	cx, cy := fx*b.Dx()/max(shot.Width, 1), fy*b.Dy()/max(shot.Height, 1)
	x0 := clampInt(cx-side/2, 0, b.Dx()-side)
	y0 := clampInt(cy-side/2, 0, b.Dy()-side)
	crop := image.NewRGBA(image.Rect(0, 0, locateCloseUp, locateCloseUp))
	draw.CatmullRom.Scale(crop, crop.Bounds(), img, image.Rect(b.Min.X+x0, b.Min.Y+y0, b.Min.X+x0+side, b.Min.Y+y0+side), draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, crop, &jpeg.Options{Quality: 90}); err != nil {
		return x, y, true
	}
	if px, py, found := l.locateIn(ctx, r, a.Intent, "part of a screenshot", "image/jpeg", buf.Bytes(), locateCloseUp, locateCloseUp); found {
		// Back to the screenshot: the close-up's pixels, then the capture's scale.
		ix, iy := x0+px*side/locateCloseUp, y0+py*side/locateCloseUp
		x, y = ix*shot.Width/max(b.Dx(), 1), iy*shot.Height/max(b.Dy(), 1)
	}
	return clampInt(x, 0, shot.Width-1), clampInt(y, 0, shot.Height-1), true
}

// locateIn asks where intent's control is in one image (w x h as the model is told) and
// answers in that image's pixels.
func (l *Loop) locateIn(ctx context.Context, r *round, intent, what, mime string, data []byte, w, h int) (int, int, bool) {
	hint := fmt.Sprintf("Coordinates are pixels of the image (%dx%d).", w, h)
	if r.pick.Adapter.Name == "norm1000" {
		hint = "Coordinates are 0 to 999 across and down the image."
	}
	temp, think, mt := 0.0, 0, locateMaxToken
	resp, err := l.LLM.Chat(ctx, llm.ChatRequest{
		Label: r.pick.Label, Priority: llm.PriorityBackground, Temperature: &temp, MaxTokens: &mt, ReasoningBudget: &think,
		Messages: []llm.Message{{Role: "user", Content: &llm.Content{Parts: []llm.Part{
			{Type: "text", Text: fmt.Sprintf(locateQuestion, what, oneLine(intent), hint)},
			{Type: "image_url", ImageURL: &llm.ImageURL{URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)}},
		}}}},
	})
	if err != nil {
		return 0, 0, false
	}
	px, py, ok := parsePoint(resp.Content)
	if !ok || r.pick.Adapter.ToImageF == nil {
		return 0, 0, false
	}
	x, y := r.pick.Adapter.ToImageF(px, py, w, h)
	return x, y, true
}

// A point as the models write one: {"x": 212, "y": 955}, or Qwen's own {"point_2d": [212, 955]}.
var (
	pointXYRE = regexp.MustCompile(`"x"\s*:\s*(-?[0-9.]+)\s*,\s*"y"\s*:\s*(-?[0-9.]+)`)
	point2dRE = regexp.MustCompile(`"point_2d"\s*:\s*\[\s*(-?[0-9.]+)\s*,\s*(-?[0-9.]+)\s*\]`)
)

// parsePoint reads the point out of a locate answer; false for "absent" and anything else.
func parsePoint(s string) (x, y float64, ok bool) {
	m := pointXYRE.FindStringSubmatch(s)
	if m == nil {
		m = point2dRE.FindStringSubmatch(s)
	}
	if m == nil {
		return 0, 0, false
	}
	x, ex := strconv.ParseFloat(m[1], 64)
	y, ey := strconv.ParseFloat(m[2], 64)
	if ex != nil || ey != nil || x < 0 || y < 0 {
		return 0, 0, false
	}
	return x, y, true
}

func clampInt(v, lo, hi int) int {
	return max(lo, min(v, max(lo, hi)))
}

// clickedBefore reports whether an earlier round of the run clicked for the same thing as
// intent: "Save the note" and "Click the Save button to save the note" are one target, the
// title field and the body field are two.
func clickedBefore(history []Entry, round int, intent string) bool {
	want := targetWords(intent)
	if len(want) == 0 {
		return false
	}
	for _, e := range history {
		if e.Round >= round || e.Ref != "" || !locatable[e.Kind] {
			continue
		}
		got := targetWords(e.Intent)
		same := 0
		for w := range got {
			if want[w] {
				same++
			}
		}
		if all := len(want) + len(got) - same; same > 0 && same*2 >= all {
			return true
		}
	}
	return false
}

// clickFiller are an intent's words that say nothing about which control it means.
var clickFiller = map[string]bool{}

func init() {
	for _, w := range strings.Fields("click clicking press tap button field box area text focus select the a an on to it in into of for and its") {
		clickFiller[w] = true
	}
}

// targetWords are intent's words that name its target, lower case.
func targetWords(intent string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(intent), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if !clickFiller[w] {
			out[w] = true
		}
	}
	return out
}
