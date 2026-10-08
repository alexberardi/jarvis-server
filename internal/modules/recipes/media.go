package recipes

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	pathpkg "path"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Editor photos (§3.2 rows #20 and #22, §4.8; CHANGE: the blob store instead of ./media).
//
// POST /recipes/import/image stores the upload as is (no resize, no re-encode) under
// recipes/media/<uuid4 hex><ext> and answers legacy's placeholder RecipeDraft whose only field
// the app reads is image_url = "/media/<name>". RD3: the URL stays relative on the wire; the app
// resolves it against the recipes base URL. GET /media/<name> serves it without auth (the
// names are 128-bit random, as legacy's were).
//
// Differences from legacy, all on the safe side: the upload is gated by image.max_bytes (413),
// must be an image (legacy stored any bytes under any extension, and served them back from the
// recipes origin; an .html "photo" was stored XSS), and is served with nosniff. A photo goes
// once no recipe refers to it: deleting or re-pointing a recipe enqueues the R1 blob purge,
// which keeps it while another recipe still shows it.

// mediaTypes are the accepted photo extensions and the content type each is served with.
// HEIC/HEIF stay accepted (iOS photos); Go cannot decode them, but this route never decodes.
var mediaTypes = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".gif":  "image/gif",
	".webp": "image/webp",
	".heic": "image/heic",
	".heif": "image/heif",
}

// sniffedExt maps what http.DetectContentType recognises to an extension.
var sniffedExt = map[string]string{
	"image/jpeg": ".jpg",
	"image/png":  ".png",
	"image/gif":  ".gif",
	"image/webp": ".webp",
}

// multipartSlack is room for the multipart framing around an image of image.max_bytes.
const multipartSlack = 1 << 20

// mediaExt picks the stored extension: the upload's own (lowercased) when it is an image type
// and the bytes do not contradict it, else what the bytes sniff as. ok is false for anything
// that is not an image.
func mediaExt(filename string, data []byte) (string, bool) {
	sniffed := http.DetectContentType(data)
	ext := strings.ToLower(pathpkg.Ext(filename))
	if _, known := mediaTypes[ext]; known {
		// Sniffing knows the common formats; HEIC/HEIF (and anything it cannot name) sniff as
		// octet-stream, which is fine. Text or HTML under an image name is not.
		if strings.HasPrefix(sniffed, "image/") || sniffed == "application/octet-stream" {
			return ext, true
		}
		return "", false
	}
	if e, ok := sniffedExt[sniffed]; ok {
		return e, true
	}
	return "", false
}

func randomHex() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// handleImportImage is POST /recipes/import/image (#20).
func (m *Module) handleImportImage(w http.ResponseWriter, r *http.Request, c caller) {
	ctx := r.Context()
	maxBytes := m.settings.Int(ctx, SettingImageMaxBytes, settings.Scope{})
	r.Body = http.MaxBytesReader(w, r.Body, maxBytes+multipartSlack)
	filename, data, status, msg := readUpload(r, "file", maxBytes)
	switch {
	case status == http.StatusUnprocessableEntity:
		writeValidation(w, fieldErr{loc: []any{"body", "file"}, msg: msg})
		return
	case status != 0:
		httpx.Error(w, status, msg)
		return
	}
	ext, ok := mediaExt(filename, data)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "Unrecognized image file")
		return
	}
	name := randomHex() + ext
	if m.deps.Blobs == nil {
		m.internalError(w, errors.New("no blob store"))
		return
	}
	if _, err := m.deps.Blobs.Put(ctx, mediaBlobPrefix+name, bytes.NewReader(data), mediaTypes[ext]); err != nil {
		m.internalError(w, err)
		return
	}
	m.deps.Log.Info("recipes: editor photo stored", "user_id", c.ID, "name", name, "bytes", len(data))
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"title":       "Draft from image",
		"ingredients": []string{"1 cup ingredient A", "2 tbsp ingredient B"},
		"steps":       []string{"Step 1: placeholder", "Step 2: placeholder"},
		"tags":        []string{},
		"image_url":   mediaPrefix + name,
	})
}

// readUpload finds the multipart file field and reads it, up to maxBytes. On failure status is
// 422 (msg is the pydantic message for body.<field>), 413 or 400 (msg is the detail).
func readUpload(r *http.Request, field string, maxBytes int64) (filename string, data []byte, status int, msg string) {
	mr, err := r.MultipartReader()
	if err != nil {
		return "", nil, http.StatusUnprocessableEntity, msgMissing
	}
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return "", nil, http.StatusUnprocessableEntity, msgMissing
		}
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				return "", nil, http.StatusRequestEntityTooLarge, "Image too large"
			}
			return "", nil, http.StatusBadRequest, "Malformed multipart body"
		}
		if p.FormName() != field {
			_ = p.Close()
			continue
		}
		if p.FileName() == "" {
			return "", nil, http.StatusUnprocessableEntity, "Value error, Expected UploadFile, received: <class 'str'>"
		}
		data, status, msg := readPart(p, maxBytes)
		return p.FileName(), data, status, msg
	}
}

func readPart(p *multipart.Part, maxBytes int64) ([]byte, int, string) {
	defer p.Close()
	data, err := io.ReadAll(io.LimitReader(p, maxBytes+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return nil, http.StatusRequestEntityTooLarge, "Image too large"
		}
		return nil, http.StatusBadRequest, "Malformed multipart body"
	}
	if int64(len(data)) > maxBytes {
		return nil, http.StatusRequestEntityTooLarge, "Image too large"
	}
	if len(data) == 0 {
		return nil, http.StatusBadRequest, "Empty image upload"
	}
	return data, 0, ""
}

// handleMedia is GET /media/{name} (#22): the stored bytes, unauthenticated, with the
// static-file headers legacy's StaticFiles sent (ETag, Last-Modified, conditional GETs) plus a
// long cache lifetime (a name is never reused for other bytes) and nosniff.
func (m *Module) handleMedia(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ct, ok := mediaTypes[strings.ToLower(pathpkg.Ext(name))]
	if !ok || name == "" || strings.ContainsAny(name, `/\`) || m.deps.Blobs == nil {
		httpx.Error(w, http.StatusNotFound, "Not Found")
		return
	}
	rc, info, err := m.deps.Blobs.Get(r.Context(), mediaBlobPrefix+name)
	if errors.Is(err, blob.ErrNotFound) || errors.Is(err, blob.ErrInvalidKey) {
		httpx.Error(w, http.StatusNotFound, "Not Found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		m.internalError(w, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "public, max-age=31536000, immutable")
	h.Set("ETag", fmt.Sprintf(`"%x-%x"`, info.ModTime.UnixNano(), info.Size))
	http.ServeContent(w, r, name, info.ModTime, bytes.NewReader(data))
}

// mediaName is the blob name behind a recipe's image_url, or "" when it is not an editor photo.
func mediaName(imageURL string) string {
	name, ok := strings.CutPrefix(imageURL, mediaPrefix)
	if !ok || name == "" || strings.ContainsAny(name, `/\`) {
		return ""
	}
	return name
}

// releaseMedia enqueues the purge of photos a write stopped referring to, in the write's
// transaction; the job deletes each one only if no recipe still shows it.
func (m *Module) releaseMedia(ctx context.Context, tx *sql.Tx, imageURLs ...string) error {
	var p blobPurge
	for _, u := range imageURLs {
		if name := mediaName(u); name != "" {
			p.Media = append(p.Media, name)
		}
	}
	return m.enqueuePurge(ctx, tx, p)
}
