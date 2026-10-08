package recipes

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/ocr"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func TestVisionSize(t *testing.T) {
	// Expected values from legacy _resize_for_vision's arithmetic (python3).
	for _, c := range []struct{ w, h, ww, wh int }{
		{4032, 3024, 1148, 868}, {3024, 4032, 868, 1148}, {2048, 1536, 1148, 868}, {1000, 1000, 1000, 1000},
		{5000, 100, 5000, 100}, {1003, 1001, 1008, 1008},
	} {
		if w, h := visionSize(c.w, c.h); w != c.ww || h != c.wh {
			t.Errorf("visionSize(%d, %d) = %d×%d, want %d×%d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
}

// withOrientation inserts an EXIF APP1 segment carrying orientation o after the JPEG SOI.
func withOrientation(jpg []byte, o uint16, bigEndian bool) []byte {
	var tiff bytes.Buffer
	var bo binary.ByteOrder = binary.LittleEndian
	tiff.WriteString("II")
	if bigEndian {
		bo = binary.BigEndian
		tiff.Reset()
		tiff.WriteString("MM")
	}
	w := func(v any) { _ = binary.Write(&tiff, bo, v) }
	w(uint16(42))
	w(uint32(8))
	w(uint16(1))      // one entry
	w(uint16(0x0112)) // orientation
	w(uint16(3))      // SHORT
	w(uint32(1))      // count
	w(uint16(o))      // value
	w(uint16(0))      // padding
	w(uint32(0))      // next IFD
	seg := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
	out := append([]byte{}, jpg[:2]...)
	out = append(out, app1...)
	out = append(out, seg...)
	return append(out, jpg[2:]...)
}

// markedJPEG is w×h, black with a red block in the top-left corner.
func markedJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{0, 0, 0, 255}
			if x < w/4 && y < h/4 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func redAt(img image.Image, x, y int) bool {
	r, g, b, _ := img.At(x, y).RGBA()
	return r > 0xa000 && g < 0x6000 && b < 0x6000
}

func TestPreparePhotoOrientation(t *testing.T) {
	src := markedJPEG(t, 160, 80)
	// The red corner's position after each EXIF transpose of a 160×80 image (PIL semantics).
	for _, c := range []struct {
		o      uint16
		w, h   int
		cx, cy int
	}{
		{1, 160, 80, 5, 5}, {2, 160, 80, 154, 5}, {3, 160, 80, 154, 74}, {4, 160, 80, 5, 74},
		{5, 80, 160, 5, 5}, {6, 80, 160, 74, 5}, {7, 80, 160, 74, 154}, {8, 80, 160, 5, 154},
	} {
		for _, be := range []bool{false, true} {
			data := withOrientation(src, c.o, be)
			if got := exifOrientation(data); got != int(c.o) {
				t.Fatalf("orientation %d (big endian %v) read as %d", c.o, be, got)
			}
			out, ok := preparePhoto(data)
			if !ok {
				t.Fatal("prepare failed")
			}
			img, err := jpeg.Decode(bytes.NewReader(out))
			if err != nil {
				t.Fatal(err)
			}
			if b := img.Bounds(); b.Dx() != c.w || b.Dy() != c.h || !redAt(img, c.cx, c.cy) {
				t.Errorf("orientation %d: %v, red at (%d,%d): %v", c.o, b, c.cx, c.cy, redAt(img, c.cx, c.cy))
			}
		}
	}
	if exifOrientation(src) != 1 || exifOrientation([]byte("nope")) != 1 {
		t.Fatal("no EXIF reads as 1")
	}
	// A big PNG is shrunk to the vision budget and re-encoded as JPEG.
	big := image.NewRGBA(image.Rect(0, 0, 2048, 1536))
	var pb bytes.Buffer
	_ = png.Encode(&pb, big)
	out, ok := preparePhoto(pb.Bytes())
	if !ok {
		t.Fatal("png")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil || format != "jpeg" || cfg.Width != 1148 || cfg.Height != 868 {
		t.Fatalf("resized: %v %s %v", cfg, format, err)
	}
	if _, ok := preparePhoto([]byte("<html>not an image</html>")); ok {
		t.Fatal("garbage decoded")
	}
}

// fakeOCR answers Recognize with fixed readings.
type fakeOCR struct {
	mu       sync.Mutex
	readings []ocr.Reading
	err      error
	images   int
}

func (f *fakeOCR) Recognize(_ context.Context, imgs []ocr.Image, _ ocr.Options, _ []string) ([]ocr.Reading, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.images = len(imgs)
	return f.readings, f.err
}

func photoForm(t *testing.T, files ...[]byte) (string, io.Reader) {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for i, f := range files {
		fw, err := mw.CreateFormFile("images", "page"+string(rune('0'+i))+".jpg")
		if err != nil {
			t.Fatal(err)
		}
		fw.Write(f)
	}
	mw.Close()
	return mw.FormDataContentType(), &b
}

func (e *env) postPhotos(t *testing.T, path string, h []string, files ...[]byte) (int, map[string]any) {
	t.Helper()
	ct, body := photoForm(t, files...)
	r := httptest.NewRequest(http.MethodPost, path, body)
	r.Header.Set("Content-Type", ct)
	r.Header.Set(h[0], h[1])
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	var o map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &o)
	return rec.Code, o
}

// recipeCard is OCR text that passes the quality gate: 10+ lines, 250+ characters, 50+
// tokens, ingredient-like and numbered lines.
var recipeCard = strings.Join([]string{
	"Grandma's Pancakes", "Serves 4", "Ingredients",
	"1 cup flour", "2 tablespoons sugar", "1 teaspoon baking powder", "1 cup milk", "2 eggs", "3 tablespoons melted butter",
	"Directions",
	"1. Whisk the flour, sugar and baking powder together in a large bowl.",
	"2. Beat the milk, eggs and butter in another bowl, then pour into the flour.",
	"3. Cook ladles of batter on a hot oiled griddle until bubbles appear, then flip once.",
	"4. Serve warm with maple syrup and fresh berries on the side.",
}, "\n")

const pancakeDraft = `{"title": "Grandma's Pancakes", "description": "Fluffy pancakes.", "ingredients": [` +
	`{"name": "flour", "quantity": "1", "unit": "cup"}, {"name": "milk", "quantity": "1", "unit": "cup"}, ` +
	`{"name": "eggs", "quantity": "2"}], "steps": ["Whisk", "Cook"], "servings": 4}`

func TestFromImageRoute(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	h := tok(1, "A")
	jpg := markedJPEG(t, 64, 64)

	if c, o := e.postPhotos(t, "/recipes/from-image/jobs", h); c != http.StatusUnprocessableEntity || o["error_code"] != "validation_error" {
		t.Fatalf("no images: %d %v", c, o)
	}
	var nine [][]byte
	for range 9 {
		nine = append(nine, jpg)
	}
	for _, c := range []struct {
		files  [][]byte
		status int
		detail string
	}{
		{nine, 400, "Too many images (max 8)"},
		{[][]byte{{}}, 400, "Empty image upload"},
		{[][]byte{[]byte("not an image at all")}, 400, "Unrecognized image file"},
	} {
		if code, o := e.postPhotos(t, "/recipes/from-image/jobs", h, c.files...); code != c.status || o["detail"] != c.detail {
			t.Fatalf("%q: %d %v", c.detail, code, o)
		}
	}
	if err := e.m.settings.Set(e.ctx, SettingImageMaxBytes, int64(100), settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	if code, o := e.postPhotos(t, "/recipes/from-image/jobs", h, jpg); code != 413 || o["detail"] != "Image too large" {
		t.Fatalf("too large: %d %v", code, o)
	}
	if err := e.m.settings.Set(e.ctx, SettingImageMaxBytes, int64(10<<20), settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	if c, _ := e.postPhotos(t, "/recipes/from-image/jobs?tier_max=x", h, jpg); c != http.StatusUnprocessableEntity {
		t.Fatalf("tier_max: %d", c)
	}
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipe_parse_jobs`) != 0 {
		t.Fatal("a refused upload created a job")
	}

	// No OCR configured: the job is created, and fails with ocr_unavailable.
	code, o := e.postPhotos(t, "/recipes/from-image/jobs?title_hint=pancakes", h, jpg, jpg)
	if code != http.StatusAccepted || len(o["ingestion_id"].(string)) != 36 || len(o["job_id"].(string)) != 36 {
		t.Fatalf("submit: %d %v", code, o)
	}
	ing := o["ingestion_id"].(string)
	if e.count(t, `SELECT COUNT(*) FROM recipes_recipe_ingestions WHERE id = ? AND household_id = 'A' AND title_hint = 'pancakes'
		AND tier_max = 3`, ing) != 1 {
		t.Fatal("ingestion row")
	}
	for _, k := range []string{ingestKey("1", ing, 0), ingestKey("1", ing, 1)} {
		if info, err := e.blobs.Stat(e.ctx, k); err != nil || info.ContentType != "image/jpeg" {
			t.Fatalf("blob %s: %+v %v", k, info, err)
		}
	}
	job := e.waitJob(t, o["job_id"].(string), h)
	if job["status"] != "ERROR" || job["error_code"] != "ocr_unavailable" {
		t.Fatalf("job: %v", job)
	}
}

func TestPhotoJob(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	jpg := markedJPEG(t, 64, 64)
	conf := 91.0
	f := &fakeOCR{readings: []ocr.Reading{
		{Engine: "tesseract", Results: []ocr.ImageReading{{Index: 0, Text: recipeCard, Confidence: &conf}}},
		{Engine: "apple_vision", Results: []ocr.ImageReading{{Index: 0, Error: "unreachable"}}},
	}}
	e.m.OCR = f
	var mu sync.Mutex
	var prompts []string
	l := &fakeLLM{reply: func(req llm.ChatRequest) (string, error) {
		mu.Lock()
		prompts = append(prompts, msgText(req.Messages[0]))
		mu.Unlock()
		if strings.HasPrefix(msgText(req.Messages[0]), "Clean recipe data.") {
			return `{"title": "Grandma's Pancakes", "description": "Fluffy pancakes.", "ingredients": [` +
				`{"name": "flour", "quantity": "1", "unit": "cup"}, {"name": "milk", "quantity": "1", "unit": "cup"}, ` +
				`{"name": "eggs", "quantity": "2", "unit": "whole"}], "steps": ["Whisk", "Cook"]}`, nil
		}
		return pancakeDraft, nil
	}}
	e.m.LLM = l
	code, o := e.postPhotos(t, "/recipes/from-image/jobs", h, jpg)
	if code != http.StatusAccepted {
		t.Fatalf("submit %d %v", code, o)
	}
	job := e.waitJob(t, o["job_id"].(string), h)
	if job["status"] != "COMPLETE" {
		t.Fatalf("job: %v", job)
	}
	res := job["result"].(map[string]any)
	d := res["recipe_draft"].(map[string]any)
	// B5: numeric servings is kept; the P3 cleanup ran (the draft had "eggs" without a unit).
	if d["title"] != "Grandma's Pancakes" || d["servings"] != nil && d["servings"] != "4" || len(d["ingredients"].([]any)) != 3 {
		t.Fatalf("draft: %v", d)
	}
	if src := d["source"].(map[string]any); src["type"] != "ocr" {
		t.Fatalf("source %v", src)
	}
	pipe := res["pipeline"].(map[string]any)
	if pipe["providers_used"].([]any)[0] != "tesseract" || pipe["metrics"].(map[string]any)["pass_gate"] != true {
		t.Fatalf("pipeline: %v", pipe)
	}
	// Apple Vision answered nothing, so only tesseract's reading reached the prompt (one
	// reading: no ensemble rules).
	if len(prompts) != 2 || strings.Contains(prompts[0], "SEVERAL independent") || !strings.HasPrefix(prompts[1], "Clean recipe data.") {
		t.Fatalf("prompts: %d %q", len(prompts), prompts)
	}
	req := l.requests()[0]
	if *req.MaxTokens != 1100 || !strings.Contains(msgText(req.Messages[1]), "<<<OCR_START>>>\nGrandma's Pancakes") {
		t.Fatalf("P2 request: %+v", req)
	}
	// B7: completed_at is set, so the photo job is in the list.
	list := e.obj(t, http.StatusOK, http.MethodGet, "/recipes/parse-url/jobs", nil, h...)["jobs"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["job_type"] != "image" {
		t.Fatalf("list: %v", list)
	}
	var status, readings string
	if err := e.d.Read.QueryRowContext(e.ctx, `SELECT status, ocr_readings FROM recipes_recipe_ingestions`).Scan(&status, &readings); err != nil {
		t.Fatal(err)
	}
	if status != "SUCCEEDED" || !strings.Contains(readings, `"provider":"apple_vision"`) {
		t.Fatalf("ingestion: %s %s", status, readings)
	}

	// The quality gate refuses a thin reading with a person-readable message.
	f.mu.Lock()
	f.readings = []ocr.Reading{{Engine: "tesseract", Results: []ocr.ImageReading{{Index: 0, Text: "Crepe\n3 eggs"}}}}
	f.mu.Unlock()
	_, o = e.postPhotos(t, "/recipes/from-image/jobs", h, jpg)
	job = e.waitJob(t, o["job_id"].(string), h)
	if job["status"] != "ERROR" || job["error_code"] != "quality_gate_failed" || job["error_message"] != gateFailedMsg {
		t.Fatalf("gate: %v", job)
	}
	// No engine read anything (B8: only then does the job fail on OCR).
	f.mu.Lock()
	f.readings = []ocr.Reading{{Engine: "tesseract", Results: []ocr.ImageReading{{Index: 0, Text: "  "}}}}
	f.mu.Unlock()
	_, o = e.postPhotos(t, "/recipes/from-image/jobs", h, jpg)
	if job = e.waitJob(t, o["job_id"].(string), h); job["error_code"] != "ocr_no_text" {
		t.Fatalf("no text: %v", job)
	}
	// The model refuses: garbage_ocr → text_structuring_exception.
	f.mu.Lock()
	f.readings = []ocr.Reading{{Engine: "tesseract", Results: []ocr.ImageReading{{Index: 0, Text: recipeCard}}}}
	f.mu.Unlock()
	l.reply = func(llm.ChatRequest) (string, error) { return `{"error": "garbage_ocr"}`, nil }
	_, o = e.postPhotos(t, "/recipes/from-image/jobs", h, jpg)
	if job = e.waitJob(t, o["job_id"].(string), h); job["error_code"] != "text_structuring_exception" {
		t.Fatalf("garbage: %v", job)
	}
	l.reply = func(llm.ChatRequest) (string, error) { return "", errors.New("model down") }
	_, o = e.postPhotos(t, "/recipes/from-image/jobs", h, jpg)
	if job = e.waitJob(t, o["job_id"].(string), h); job["error_code"] != "text_structuring_exception" {
		t.Fatalf("model down: %v", job)
	}
}
