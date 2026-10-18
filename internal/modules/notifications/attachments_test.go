package notifications

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// mp4Bytes is enough of an ISO BMFF file to sniff: an ftyp box, then filler.
func mp4Bytes(n int) []byte {
	return append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), bytes.Repeat([]byte{0x42}, n)...)
}

// raw sends a non-JSON body with the given content type.
func (e *env) raw(t *testing.T, method, path, contentType string, body []byte, hdr ...string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *env) upload(t *testing.T, contentType string, body []byte) map[string]any {
	t.Helper()
	rec := e.raw(t, "POST", "/api/v0/inbox/attachments", contentType, body, appH...)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	var v map[string]any
	dec := json.NewDecoder(rec.Body)
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func itemWith(userID string, ids ...string) string {
	q, _ := json.Marshal(ids)
	uid := ""
	if userID != "" {
		uid = `"user_id":` + userID + `,`
	}
	return `{"household_id":"hh1",` + uid + `"title":"pic","summary":"s","body":"b","category":"media","source_service":"comfyui","attachment_ids":` + string(q) + `}`
}

func (e *env) blobCount(t *testing.T) int {
	t.Helper()
	l, err := e.blobs.List(e.ctx, "inbox/")
	if err != nil {
		t.Fatal(err)
	}
	return len(l)
}

func TestInboxAttachmentsRoundTrip(t *testing.T) {
	e := setup(t)
	img := pngBytes(t, 3, 2)
	a := e.upload(t, "image/png", img)
	if a["mime"] != "image/png" || a["size"].(json.Number).String() != strconv.Itoa(len(img)) ||
		a["width"].(json.Number).String() != "3" || a["height"].(json.Number).String() != "2" || a["duration_ms"] != nil {
		t.Fatalf("upload: %v", a)
	}
	v := e.upload(t, "video/mp4", mp4Bytes(100))
	if v["mime"] != "video/mp4" || v["width"] != nil || v["duration_ms"] != nil {
		t.Fatalf("mp4: %v", v)
	}

	it := asObj(e.json(t, 200, "POST", "/api/v0/inbox", itemWith("1", a["id"].(string), v["id"].(string)), appH...))
	atts, _ := it["attachments"].([]any)
	if len(atts) != 2 || asObj(atts[0])["id"] != a["id"] || asObj(atts[1])["mime"] != "video/mp4" {
		t.Fatalf("created: %v", it)
	}
	id := it["id"].(string)
	u := userH(t, 1, "hh1")
	list := e.json(t, 200, "GET", "/api/v0/inbox", "", u...).([]any)
	if got := asObj(list[0])["attachments"].([]any); len(got) != 2 {
		t.Fatalf("list: %v", list)
	}
	got := asObj(e.json(t, 200, "GET", "/api/v0/inbox/"+id, "", u...))
	if len(got["attachments"].([]any)) != 2 {
		t.Fatalf("get: %v", got)
	}
	// An item without attachments keeps the legacy shape (no key), for the contract suite.
	plain := asObj(e.json(t, 200, "POST", "/api/v0/inbox", itemWith("1"), appH...))
	if _, has := plain["attachments"]; has {
		t.Fatalf("plain item: %v", plain)
	}

	// Download: the bytes, typed, never sniffed, with Range for video.
	path := "/api/v0/inbox/" + id + "/attachments/"
	rec := e.raw(t, "GET", path+a["id"].(string), "", nil, u...)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), img) || rec.Header().Get("Content-Type") != "image/png" ||
		rec.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(rec.Header().Get("Content-Disposition"), "inline") {
		t.Fatalf("download: %d %v", rec.Code, rec.Header())
	}
	rec = e.raw(t, "GET", path+v["id"].(string), "", nil, append(u, "Range", "bytes=0-9")...)
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 10 || rec.Header().Get("Content-Range") != "bytes 0-9/"+strconv.Itoa(len(mp4Bytes(100))) {
		t.Fatalf("range: %d %v %d", rec.Code, rec.Header(), rec.Body.Len())
	}
	if rec := e.raw(t, "GET", path+"nope", "", nil, u...); rec.Code != 404 {
		t.Fatalf("unknown attachment: %d", rec.Code)
	}
	if rec := e.raw(t, "GET", "/api/v0/inbox/"+plain["id"].(string)+"/attachments/"+a["id"].(string), "", nil, u...); rec.Code != 404 {
		t.Fatalf("attachment under another item: %d", rec.Code)
	}
}

func TestInboxAttachmentVisibility(t *testing.T) {
	e := setup(t)
	a := e.upload(t, "image/png", pngBytes(t, 1, 1))
	personal := asObj(e.json(t, 200, "POST", "/api/v0/inbox", itemWith("1", a["id"].(string)), appH...))
	b := e.upload(t, "image/png", pngBytes(t, 2, 2))
	household := asObj(e.json(t, 200, "POST", "/api/v0/inbox", itemWith("", b["id"].(string)), appH...))
	pp := "/api/v0/inbox/" + personal["id"].(string) + "/attachments/" + a["id"].(string)
	hp := "/api/v0/inbox/" + household["id"].(string) + "/attachments/" + b["id"].(string)
	for _, c := range []struct {
		name string
		hdr  []string
		path string
		want int
	}{
		{"owner", userH(t, 1, "hh1"), pp, 200},
		{"another member, personal item", userH(t, 2, "hh1"), pp, 404},
		{"another member, household item", userH(t, 2, "hh1"), hp, 200},
		{"another household", userH(t, 1, "hh2"), pp, 404},
		{"another household, household item", userH(t, 3, "hh2"), hp, 404},
		{"no token", nil, pp, 401},
		{"app credentials", appH, pp, 401},
	} {
		if rec := e.raw(t, "GET", c.path, "", nil, c.hdr...); rec.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, rec.Code, c.want, rec.Body)
		}
	}
	// Downloading is not opening: the item stays unread.
	var read bool
	e.d.Read.QueryRow(`SELECT is_read FROM notifications_inbox_items WHERE id = ?`, household["id"]).Scan(&read)
	if read {
		t.Fatal("download marked the item read")
	}
}

func TestInboxAttachmentValidation(t *testing.T) {
	e := setup(t)
	if err := e.m.settings.Set(e.ctx, SettingAttachmentMaxFileBytes, int64(200), settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	if err := e.m.settings.Set(e.ctx, SettingAttachmentMaxItemBytes, int64(300), settings.Scope{}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name, ct string
		body     []byte
		want     int
	}{
		{"too big", "video/mp4", mp4Bytes(500), 413},
		{"text", "image/png", []byte("hello there, not an image"), 415},
		{"declared type disagrees", "image/jpeg", pngBytes(t, 1, 1), 415},
		{"empty", "image/png", nil, 415},
		{"svg is not allowed", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 415},
	} {
		if rec := e.raw(t, "POST", "/api/v0/inbox/attachments", c.ct, c.body, appH...); rec.Code != c.want {
			t.Errorf("%s: %d, want %d: %s", c.name, rec.Code, c.want, rec.Body)
		}
	}
	if rec := e.raw(t, "POST", "/api/v0/inbox/attachments", "image/png", pngBytes(t, 1, 1)); rec.Code != 401 {
		t.Errorf("no app creds: %d", rec.Code)
	}
	if e.blobCount(t) != 0 {
		t.Fatalf("refused uploads left %d blobs", e.blobCount(t))
	}
	// A sniffable type without a declared one (or octet-stream) is fine.
	x := e.upload(t, "application/octet-stream", mp4Bytes(100))
	y := e.upload(t, "", mp4Bytes(101))
	z := e.upload(t, "video/mp4", mp4Bytes(102))

	// Per-item cap: x + y + z > 300.
	if c, b := e.do(t, "POST", "/api/v0/inbox", itemWith("1", x["id"].(string), y["id"].(string), z["id"].(string)), appH...); c != 413 {
		t.Fatalf("item cap: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/inbox", itemWith("1", "nope"), appH...); c != 422 {
		t.Fatalf("unknown id: %d %s", c, b)
	}
	if c, b := e.do(t, "POST", "/api/v0/inbox", `{"household_id":"hh1","title":"t","summary":"s","body":"b","category":"c","source_service":"x","attachment_ids":"x"}`, appH...); c != 422 {
		t.Fatalf("not a list: %d %s", c, b)
	}
	// Nothing was claimed by the refused creates: x is still usable, once.
	e.json(t, 200, "POST", "/api/v0/inbox", itemWith("1", x["id"].(string), x["id"].(string)), appH...)
	if c, b := e.do(t, "POST", "/api/v0/inbox", itemWith("1", x["id"].(string)), appH...); c != 422 {
		t.Fatalf("claimed twice: %d %s", c, b)
	}
	var items int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM notifications_inbox_items`).Scan(&items)
	if items != 1 {
		t.Fatalf("%d items after refused creates", items)
	}
}

func TestInboxAttachmentOwnership(t *testing.T) {
	e := setup(t)
	// Another uploader (a node, through the in-process API) cannot hand its upload to the app.
	n, err := e.m.UploadInboxAttachment(e.ctx, "node:kitchen", "hh1", "image/png", bytes.NewReader(pngBytes(t, 1, 1)))
	if err != nil {
		t.Fatal(err)
	}
	if c, b := e.do(t, "POST", "/api/v0/inbox", itemWith("1", n.ID), appH...); c != 422 {
		t.Fatalf("someone else's upload: %d %s", c, b)
	}
	// A node's upload is bound to its household too.
	_, err = e.m.CreateInboxItem(e.ctx, nil, NewInboxItem{HouseholdID: "hh2", Title: "t", Category: "c", SourceService: "x",
		AttachmentOwner: "node:kitchen", AttachmentIDs: []string{n.ID}})
	if !IsAttachmentError(err) {
		t.Fatalf("other household: %v", err)
	}
	it, err := e.m.CreateInboxItem(e.ctx, nil, NewInboxItem{HouseholdID: "hh1", Title: "t", Category: "c", SourceService: "x",
		AttachmentOwner: "node:kitchen", AttachmentIDs: []string{n.ID}})
	if err != nil || len(it.Attachments) != 1 || it.Attachments[0].Width == nil || *it.Attachments[0].Width != 1 {
		t.Fatalf("node claim: %+v %v", it, err)
	}
}

func TestInboxAttachmentsDeletedWithItems(t *testing.T) {
	e := setup(t)
	shared := pngBytes(t, 4, 4)
	claim := func(userID string, body []byte) string {
		a := e.upload(t, "image/png", body)
		return asObj(e.json(t, 200, "POST", "/api/v0/inbox", itemWith(userID, a["id"].(string)), appH...))["id"].(string)
	}
	one := claim("1", shared)
	two := claim("1", pngBytes(t, 5, 5))
	three := claim("1", pngBytes(t, 6, 6))
	keep := claim("2", shared) // same bytes, another item: one blob
	orphan := e.upload(t, "image/png", pngBytes(t, 7, 7))
	if n := e.blobCount(t); n != 4 {
		t.Fatalf("%d blobs, want 4 (content-addressed)", n)
	}
	u := userH(t, 1, "hh1")
	e.json(t, 204, "DELETE", "/api/v0/inbox/"+one, "", u...)
	e.json(t, 200, "POST", "/api/v0/inbox/bulk/delete", `{"ids":["`+two+`"]}`, u...)
	if err := e.d.Tx(e.ctx, func(tx *sql.Tx) error { return e.m.PurgeUser(e.ctx, tx, 1) }); err != nil {
		t.Fatal(err)
	}
	_ = three
	var rows int
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM notifications_inbox_attachments`).Scan(&rows)
	if rows != 2 { // keep's and the unclaimed orphan
		t.Fatalf("%d attachment rows after deletes", rows)
	}
	// The sweep removes unreferenced blobs and unclaimed uploads once they are an hour old;
	// the shared blob stays while keep references it.
	if _, err := e.m.SweepAttachments(e.ctx); err != nil {
		t.Fatal(err)
	}
	if n := e.blobCount(t); n != 4 {
		t.Fatalf("young blobs swept: %d left", n)
	}
	e.m.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, err := e.m.runCleanup(e.ctx, queue.Job{}); err != nil {
		t.Fatal(err)
	}
	if n := e.blobCount(t); n != 1 {
		t.Fatalf("%d blobs after sweep, want 1 (the shared one)", n)
	}
	e.d.Read.QueryRow(`SELECT COUNT(*) FROM notifications_inbox_attachments WHERE id = ?`, orphan["id"]).Scan(&rows)
	if rows != 0 {
		t.Fatal("unclaimed upload survived the sweep")
	}
	rec := e.raw(t, "GET", "/api/v0/inbox/"+keep+"/attachments/"+
		asObj(asObj(e.json(t, 200, "GET", "/api/v0/inbox/"+keep, "", userH(t, 2, "hh1")...))["attachments"].([]any)[0])["id"].(string),
		"", nil, userH(t, 2, "hh1")...)
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), shared) {
		t.Fatalf("shared blob: %d", rec.Code)
	}
}
