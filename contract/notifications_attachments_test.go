//go:build contract

package contract

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"net/http"
	"strconv"
	"testing"
)

// Inbox attachments are jarvisd's (docs/decisions/2026-10-17-inbox-attachments.md); the legacy
// service has no such routes, so this runs against jarvisd only.

var attachmentShape = Obj{
	"id":          UUID,
	"mime":        String,
	"size":        Int,
	"width":       NullOr(Int),
	"height":      NullOr(Int),
	"duration_ms": NullOr(Int),
}

func TestNotificationsInboxAttachments(t *testing.T) {
	tg := T(t)
	tg.Need(t, Notifications)
	if !Jarvisd() {
		t.Skip("inbox attachments are jarvisd-only")
	}
	app := SharedApp(t)
	u := NewUser(t)
	other := NewUser(t) // another household

	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 4, 3))); err != nil {
		t.Fatal(err)
	}
	clip := append([]byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"), bytes.Repeat([]byte{1}, 64)...)
	upload := func(ct string, body []byte) map[string]any {
		t.Helper()
		return tg.Post(t, Notifications, "/api/v0/inbox/attachments", body, app.H(), H{"Content-Type": ct}).
			Expect(http.StatusCreated, with(attachmentShape, "mime", Eq(ct), "size", Eq(len(body)))).Object()
	}
	a := upload("image/png", img.Bytes())
	if fmt.Sprint(a["width"], "x", a["height"]) != "4x3" {
		t.Fatalf("image dimensions: %v", a)
	}
	v := upload("video/mp4", clip)

	t.Run("upload refusals", func(t *testing.T) {
		tg.Post(t, Notifications, "/api/v0/inbox/attachments", []byte("<svg/>"), app.H(), H{"Content-Type": "image/svg+xml"}).
			ExpectStatus(http.StatusUnsupportedMediaType)
		tg.Post(t, Notifications, "/api/v0/inbox/attachments", img.Bytes(), H{"Content-Type": "image/png"}).
			ExpectError(http.StatusUnauthorized, "Missing app credentials")
	})

	body := newInboxItem(u.HouseholdID, u.ID, "with attachments")
	body["attachment_ids"] = []string{a["id"].(string), v["id"].(string)}
	it := tg.Post(t, Notifications, "/api/v0/inbox", body, app.H()).
		Expect(http.StatusOK, with(inboxItemShape, "attachments", ArrayOf(attachmentShape))).Object()
	id := it["id"].(string)
	t.Cleanup(func() { tg.Do(t, Notifications, "DELETE", "/api/v0/inbox/"+id, nil, u.H()) })

	t.Run("claimed once", func(t *testing.T) {
		b := newInboxItem(u.HouseholdID, u.ID, "again")
		b["attachment_ids"] = []string{a["id"].(string)}
		tg.Post(t, Notifications, "/api/v0/inbox", b, app.H()).ExpectStatus(http.StatusUnprocessableEntity)
	})

	t.Run("get lists them", func(t *testing.T) {
		got := tg.Get(t, Notifications, "/api/v0/inbox/"+id, u.H()).
			Expect(http.StatusOK, with(inboxItemShape, "is_read", Eq(true), "attachments", ArrayOf(attachmentShape))).Object()
		if l := got["attachments"].([]any); len(l) != 2 || l[0].(map[string]any)["id"] != a["id"] {
			t.Fatalf("attachments: %v", l)
		}
	})

	path := "/api/v0/inbox/" + id + "/attachments/"
	t.Run("download", func(t *testing.T) {
		r := tg.Get(t, Notifications, path+a["id"].(string), u.H()).ExpectStatus(http.StatusOK)
		if !bytes.Equal(r.Body, img.Bytes()) || http.Header(r.Header).Get("Content-Type") != "image/png" ||
			http.Header(r.Header).Get("X-Content-Type-Options") != "nosniff" {
			r.Fatalf("image download")
		}
		r = tg.Get(t, Notifications, path+v["id"].(string), u.H(), H{"Range": "bytes=0-9"}).ExpectStatus(http.StatusPartialContent)
		if len(r.Body) != 10 || http.Header(r.Header).Get("Content-Range") != "bytes 0-9/"+strconv.Itoa(len(clip)) {
			r.Fatalf("range")
		}
	})

	t.Run("download is the item's user's only", func(t *testing.T) {
		tg.Get(t, Notifications, path+a["id"].(string), other.H()).ExpectError(http.StatusNotFound, "Attachment not found")
		tg.Get(t, Notifications, path+"nope", u.H()).ExpectError(http.StatusNotFound, "Attachment not found")
		tg.Get(t, Notifications, path+a["id"].(string)).ExpectError(http.StatusUnauthorized, "Not authenticated")
	})
}
