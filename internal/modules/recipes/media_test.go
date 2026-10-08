package recipes

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for x := 0; x < 8; x++ {
		img.Set(x, x, color.RGBA{200, 10, 10, 255})
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// upload posts a multipart body with one part (field, filename) to /recipes/import/image.
func (e *env) upload(t *testing.T, field, filename string, data []byte, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if filename == "" {
		_ = mw.WriteField(field, string(data))
	} else {
		fw, err := mw.CreateFormFile(field, filename)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	_ = mw.Close()
	r := httptest.NewRequest(http.MethodPost, "/recipes/import/image", &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *env) get(t *testing.T, p string, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, p, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func uploadedURL(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var d map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	u, _ := d["image_url"].(string)
	return u
}

func TestImportImageRoundTrip(t *testing.T) {
	e := setup(t)
	h := tok(1, "A")
	e.hh.set(1, "A")
	img := testJPEG(t)

	rec := e.upload(t, "file", "Photo.JPG", img, h...)
	var d map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if d["title"] != "Draft from image" || len(d["ingredients"].([]any)) != 2 || len(d["steps"].([]any)) != 2 ||
		len(d["tags"].([]any)) != 0 {
		t.Fatalf("draft shape: %v", d)
	}
	u := uploadedURL(t, rec)
	if !regexp.MustCompile(`^/media/[0-9a-f]{32}\.jpg$`).MatchString(u) {
		t.Fatalf("image_url %q (RD3: relative, lowercase ext)", u)
	}

	// Served without auth, byte for byte, with static-file headers.
	got := e.get(t, u)
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), img) {
		t.Fatalf("GET %s: %d, %d bytes", u, got.Code, got.Body.Len())
	}
	for k, want := range map[string]string{"Content-Type": "image/jpeg", "X-Content-Type-Options": "nosniff"} {
		if got.Header().Get(k) != want {
			t.Errorf("%s = %q", k, got.Header().Get(k))
		}
	}
	etag, lm := got.Header().Get("ETag"), got.Header().Get("Last-Modified")
	if etag == "" || lm == "" || !strings.Contains(got.Header().Get("Cache-Control"), "max-age") {
		t.Fatalf("headers: %v", got.Header())
	}
	if c := e.get(t, u, "If-None-Match", etag); c.Code != http.StatusNotModified {
		t.Fatalf("conditional GET: %d", c.Code)
	}

	// The extension is sniffed when the filename has none.
	if u := uploadedURL(t, e.upload(t, "file", "blob", testPNG(t), h...)); !strings.HasSuffix(u, ".png") {
		t.Fatalf("sniffed ext: %q", u)
	}
	// HEIC (which Go cannot sniff) is accepted by name.
	if u := uploadedURL(t, e.upload(t, "file", "IMG_1.HEIC", []byte("\x00\x00\x00\x18ftypheic...."), h...)); !strings.HasSuffix(u, ".heic") {
		t.Fatalf("heic: %q", u)
	}
}

func TestImportImageRejects(t *testing.T) {
	e := setup(t)
	h := tok(1, "")
	if rec := e.upload(t, "file", "x.jpg", testJPEG(t)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no auth: %d", rec.Code)
	}
	if rec := e.upload(t, "other", "x.jpg", testJPEG(t), h...); rec.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(rec.Body.String(), `"body.file"`) {
		t.Fatalf("missing file: %d %s", rec.Code, rec.Body)
	}
	if rec := e.upload(t, "file", "", []byte("text"), h...); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("file as a plain field: %d %s", rec.Code, rec.Body)
	}
	for name, data := range map[string][]byte{
		"evil.html": []byte("<html><script>alert(1)</script></html>"),
		"evil.jpg":  []byte("<html><script>alert(1)</script></html>"),
		"notes":     []byte("just text"),
	} {
		if rec := e.upload(t, "file", name, data, h...); rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), "Unrecognized image file") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := e.upload(t, "file", "x.jpg", nil, h...); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty: %d", rec.Code)
	}
	if err := e.m.settings.Set(e.ctx, SettingImageMaxBytes, int64(100), settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	if rec := e.upload(t, "file", "x.jpg", testJPEG(t), h...); rec.Code != http.StatusRequestEntityTooLarge ||
		!strings.Contains(rec.Body.String(), "Image too large") {
		t.Fatalf("too large: %d %s", rec.Code, rec.Body)
	}
}

func TestMediaNotFound(t *testing.T) {
	e := setup(t)
	for _, p := range []string{"/media/" + strings.Repeat("0", 32) + ".jpg", "/media/a/b.jpg", "/media/x.html", "/media/..%2fx.jpg"} {
		rec := e.get(t, p)
		if rec.Code != http.StatusNotFound || strings.TrimSpace(rec.Body.String()) != `{"detail":"Not Found"}` {
			t.Errorf("%s: %d %s", p, rec.Code, rec.Body)
		}
	}
}

// waitBlob polls until the blob's existence matches want (the purge runs on the queue).
func (e *env) waitBlob(t *testing.T, key string, want bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, err := e.blobs.Stat(e.ctx, key)
		if (err == nil) == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("blob %s: exists=%v, want %v", key, err == nil, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestMediaReleasedWithLastRecipe: a photo goes once no recipe shows it (delete or re-point),
// and stays while another recipe does.
func TestMediaReleasedWithLastRecipe(t *testing.T) {
	e := setup(t)
	e.hh.set(1, "A")
	h := tok(1, "A")
	u := uploadedURL(t, e.upload(t, "file", "a.jpg", testJPEG(t), h...))
	key := mediaBlobPrefix + strings.TrimPrefix(u, mediaPrefix)
	r1 := e.create(t, h, recipeBody("one", map[string]any{"image_url": u}))
	r2 := e.create(t, h, recipeBody("two", map[string]any{"image_url": u}))

	e.json(t, http.StatusNoContent, http.MethodDelete, path("/recipes/%d", idOf(r1)), nil, h...)
	time.Sleep(50 * time.Millisecond)
	e.waitBlob(t, key, true) // r2 still shows it

	// PATCH without image_url, or with the same one, keeps it.
	e.json(t, http.StatusOK, http.MethodPatch, path("/recipes/%d", idOf(r2)), map[string]any{"title": "2b"}, h...)
	e.json(t, http.StatusOK, http.MethodPatch, path("/recipes/%d", idOf(r2)), map[string]any{"image_url": u}, h...)
	time.Sleep(50 * time.Millisecond)
	e.waitBlob(t, key, true)

	// Re-pointing the last recipe releases it.
	e.json(t, http.StatusOK, http.MethodPatch, path("/recipes/%d", idOf(r2)), map[string]any{"image_url": "https://example.com/x.jpg"}, h...)
	e.waitBlob(t, key, false)
	if rec := e.get(t, u); rec.Code != http.StatusNotFound {
		t.Fatalf("released photo still served: %d", rec.Code)
	}

	// Deleting the only recipe releases its photo.
	u2 := uploadedURL(t, e.upload(t, "file", "b.png", testPNG(t), h...))
	r3 := e.create(t, h, recipeBody("three", map[string]any{"image_url": u2}))
	e.json(t, http.StatusNoContent, http.MethodDelete, path("/recipes/%d", idOf(r3)), nil, h...)
	e.waitBlob(t, mediaBlobPrefix+strings.TrimPrefix(u2, mediaPrefix), false)
}

func TestMediaExt(t *testing.T) {
	jpg := testJPEG(t)
	for _, c := range []struct {
		name string
		data []byte
		ext  string
		ok   bool
	}{
		{"a.JPEG", jpg, ".jpeg", true},
		{"a.png", jpg, ".png", true}, // a mislabelled image is still an image
		{"a", jpg, ".jpg", true},
		{"a.txt", jpg, ".jpg", true},
		{"a.webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), ".webp", true},
		{"a.svg", []byte("<svg></svg>"), "", false},
		{"a.jpg", []byte("%PDF-1.4"), "", false},
	} {
		ext, ok := mediaExt(c.name, c.data)
		if ext != c.ext || ok != c.ok {
			t.Errorf("mediaExt(%q) = %q %v, want %q %v", c.name, ext, ok, c.ext, c.ok)
		}
	}
}
