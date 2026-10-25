package cc

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"strings"
	"testing"
	"time"
)

const kfRecID = "rec_feedface0000aa01"

// testJPEG is a w×h JPEG, padded with a comment segment to at least pad bytes.
func testJPEG(t *testing.T, w, h, pad int) string {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewGray(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	for len(b) < pad { // COM segments after SOI: still a JPEG
		n := min(pad-len(b), 0xfff0)
		seg := append([]byte{0xff, 0xfe, byte((n + 2) >> 8), byte(n + 2)}, make([]byte, n)...)
		b = append(b[:2:2], append(seg, b[2:]...)...)
	}
	return string(b)
}

// uploadDesktopRecording stores a desktop recording from the env's computer (signed in as 7):
// clicks at seq 1 and 3, a key at 2.
func uploadDesktopRecording(ve *voiceEnv) {
	ve.t.Helper()
	ve.makeDesktop(7)
	ve.do("POST", uploadPath, map[string]any{"recording_id": kfRecID, "mode": "desktop", "keyframes": 3,
		"trace": []any{
			map[string]any{"seq": 1, "kind": "click", "x": 10, "y": 20, "keyframe_point": map[string]any{"x": 5, "y": 10}},
			map[string]any{"seq": 2, "kind": "key", "key": "Return"},
			map[string]any{"seq": 3, "kind": "click", "x": 30, "y": 40},
		}}, ve.node.h()).want(200)
}

func kfPath(id, seq string, crop bool) string {
	p := uploadPath + "/" + id + "/keyframes/" + seq
	if crop {
		p += "/crop"
	}
	return p
}

func (e *env) blobKeys(prefix string) []string {
	e.t.Helper()
	infos, err := e.m.deps.Blobs.List(context.Background(), prefix)
	if err != nil {
		e.t.Fatal(err)
	}
	var keys []string
	for _, i := range infos {
		keys = append(keys, i.Key)
	}
	return keys
}

// A computer's agent uploads each click's keyframe and its crop after the trace; they are kept
// under the recording, the owner sees which, and only that computer adds to it.
func TestUploadRecordingKeyframes(t *testing.T) {
	ve, _, _ := newRecToolEnv(t)
	uploadDesktopRecording(ve)
	frame, crop := testJPEG(t, 64, 48, 0), testJPEG(t, 16, 16, 0)
	jh := ve.node.h()
	jh["Content-Type"] = "image/jpeg"

	ve.do("PUT", kfPath(kfRecID, "1", false), frame, jh).want(200)
	ve.do("PUT", kfPath(kfRecID, "1", true), crop, jh).want(200)
	ve.do("PUT", kfPath(kfRecID, "3", false), frame, jh).want(200)
	// The same again (the agent's retry) is no second copy.
	ve.do("PUT", kfPath(kfRecID, "1", false), frame, jh).want(200)
	want := "recordings/" + kfRecID + "/1.crop.jpg recordings/" + kfRecID + "/1.jpg recordings/" + kfRecID + "/3.jpg"
	if got := strings.Join(ve.blobKeys("recordings/"), " "); got != want {
		t.Fatalf("stored %s, want %s", got, want)
	}
	rc, info, err := ve.m.deps.Blobs.Get(context.Background(), "recordings/"+kfRecID+"/1.crop.jpg")
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if info.Size != int64(len(crop)) || info.ContentType != "image/jpeg" {
		t.Fatalf("crop stored as %+v", info)
	}

	// The owner's recording says which clicks have a keyframe (and a crop).
	got := ve.do("GET", recBase+"/"+kfRecID, nil, bearer("tok-7")).want(200).json()
	kfs, _ := got["keyframe_images"].([]any)
	if len(kfs) != 2 || kfs[0].(map[string]any)["seq"] != 1.0 || kfs[0].(map[string]any)["crop"] != true ||
		kfs[1].(map[string]any)["seq"] != 3.0 || kfs[1].(map[string]any)["crop"] != false {
		t.Fatalf("keyframe_images %v", got["keyframe_images"])
	}
	// The owner reads them back (the Recordings page's thumbnails); no one else does.
	for path, body := range map[string]string{kfPath(kfRecID, "1", false): frame, kfPath(kfRecID, "1", true): crop} {
		path = strings.Replace(path, uploadPath, recBase, 1)
		r := ve.do("GET", path, nil, bearer("tok-7")).want(200)
		if string(r.body) != body || r.header.Get("Content-Type") != "image/jpeg" {
			t.Fatalf("GET %s: %s, %d bytes", path, r.header.Get("Content-Type"), len(r.body))
		}
		ve.do("GET", path, nil, bearer("tok-8")).detail(404, "Recording not found")
	}
	ve.do("GET", recBase+"/"+kfRecID+"/keyframes/3/crop", nil, bearer("tok-7")).detail(404, "Keyframe not found")
	ve.do("GET", recBase+"/"+kfRecID+"/keyframes/2", nil, bearer("tok-7")).detail(404, "Keyframe not found")
	ve.do("GET", recBase+"/"+kfRecID+"/keyframes/x", nil, bearer("tok-7")).want(400)

	// Not a click (the key at 2), no such event, a bad seq: refused.
	ve.do("PUT", kfPath(kfRecID, "2", false), frame, jh).want(422)
	ve.do("PUT", kfPath(kfRecID, "9", false), frame, jh).want(422)
	ve.do("PUT", kfPath(kfRecID, "x", false), frame, jh).want(400)
	ve.do("PUT", kfPath(kfRecID, "0", false), frame, jh).want(400)
	// Not a JPEG.
	ve.do("PUT", kfPath(kfRecID, "3", true), "GIF89a....", jh).want(400)
	// An unknown recording.
	ve.do("PUT", kfPath("rec_feedface0000aa99", "1", false), frame, jh).want(404)
	// No node key, a wrong one.
	ve.do("PUT", kfPath(kfRecID, "1", false), frame, nil).want(400)
	ve.do("PUT", kfPath(kfRecID, "1", false), frame, testNode{id: ve.node.id, key: "wrong"}.h()).want(401)
	// Another computer, even in the household with the same member signed in, doesn't add to it.
	other := ve.createNode("node-desk-2", voiceHH)
	if _, err := ve.d.Write.Exec(`UPDATE cc_nodes SET node_kind = 'desktop', owner_user_id = 7 WHERE node_id = ?`, other.id); err != nil {
		t.Fatal(err)
	}
	ve.do("PUT", kfPath(kfRecID, "3", true), crop, other.h()).want(404)
	if n := len(ve.blobKeys("recordings/")); n != 3 {
		t.Fatalf("refused uploads stored something: %d keys", n)
	}
}

// One image is at most 2 MiB (a crop 256 KiB), a recording's at most recordingKeyframesMax
// keyframes and recordingKeyframeBytesMax bytes: past that a keyframe is refused (413) and the
// recording and what it has stay.
func TestRecordingKeyframeLimits(t *testing.T) {
	ve, _, _ := newRecToolEnv(t)
	uploadDesktopRecording(ve)
	jh := ve.node.h()
	jh["Content-Type"] = "image/jpeg"

	ve.do("PUT", kfPath(kfRecID, "1", false), testJPEG(t, 8, 8, keyframeMaxBytes+1), jh).want(413)
	ve.do("PUT", kfPath(kfRecID, "1", true), testJPEG(t, 8, 8, keyframeCropMaxBytes+1), jh).want(413)
	ve.do("PUT", kfPath(kfRecID, "1", true), testJPEG(t, 8, 8, keyframeCropMaxBytes-64), jh).want(200)

	// The per-recording byte budget: lower it so the test stays small.
	defer func(n int64) { recordingKeyframeBytesMax = n }(recordingKeyframeBytesMax)
	recordingKeyframeBytesMax = keyframeCropMaxBytes + 1000
	big := testJPEG(t, 8, 8, 2000)
	got := ve.do("PUT", kfPath(kfRecID, "3", false), big, jh).want(413).json()
	if got["code"] != keyframeLimitCode {
		t.Fatalf("413 %v doesn't name the recording's limit", got)
	}
	// Replacing what is there counts the new size, not both.
	ve.do("PUT", kfPath(kfRecID, "1", true), testJPEG(t, 8, 8, keyframeCropMaxBytes-64), jh).want(200)

	// The count: at most recordingKeyframesMax frames (crops come with their frame).
	defer func(n int) { recordingKeyframesMax = n }(recordingKeyframesMax)
	recordingKeyframeBytesMax, recordingKeyframesMax = 100<<20, 1
	ve.do("PUT", kfPath(kfRecID, "1", false), big, jh).want(200)
	ve.do("PUT", kfPath(kfRecID, "1", false), big, jh).want(200) // the same one again
	ve.do("PUT", kfPath(kfRecID, "3", false), big, jh).want(413)

	if !ve.recordingExists(kfRecID) {
		t.Fatal("a refused keyframe took the recording")
	}
	if got := strings.Join(ve.blobKeys("recordings/"), " "); got != "recordings/"+kfRecID+"/1.crop.jpg recordings/"+kfRecID+"/1.jpg" {
		t.Fatalf("stored %s", got)
	}
}

// A recording's keyframes go with it: deleted by its owner, expired by the sweep; and the
// sweep removes keyframes whose recording is gone (one uploaded while it was deleted).
func TestRecordingKeyframesPurged(t *testing.T) {
	ve, _, _ := newRecToolEnv(t)
	ctx := context.Background()
	uploadDesktopRecording(ve)
	jh := ve.node.h()
	jh["Content-Type"] = "image/jpeg"
	frame := testJPEG(t, 32, 32, 0)
	ve.do("PUT", kfPath(kfRecID, "1", false), frame, jh).want(200)
	ve.do("PUT", kfPath(kfRecID, "1", true), frame, jh).want(200)

	// Another recording (stored later, so it expires later) keeps its own.
	later := "rec_feedface0000aa02"
	ve.advance(time.Hour)
	ve.do("POST", uploadPath, map[string]any{"recording_id": later, "mode": "desktop",
		"trace": []any{map[string]any{"seq": 1, "kind": "click"}}}, ve.node.h()).want(200)
	ve.do("PUT", kfPath(later, "1", false), frame, jh).want(200)

	// The owner deletes the first: its keyframes go, the other's stay.
	ve.do("DELETE", recBase+"/"+kfRecID, nil, bearer("tok-7")).want(200)
	if got := strings.Join(ve.blobKeys("recordings/"), " "); got != "recordings/"+later+"/1.jpg" {
		t.Fatalf("after delete: %s", got)
	}

	// An orphan (its recording is gone) is swept, as is an expired recording's.
	if _, err := ve.m.deps.Blobs.Put(ctx, "recordings/rec_gone0000000001/4.jpg", strings.NewReader(frame), "image/jpeg"); err != nil {
		t.Fatal(err)
	}
	if err := ve.m.sweepRecordings(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ve.blobKeys("recordings/"), " "); got != "recordings/"+later+"/1.jpg" {
		t.Fatalf("after the first sweep: %s", got)
	}
	ve.advance(time.Duration(defaultRecordingRetention)*24*time.Hour + time.Minute)
	if err := ve.m.sweepRecordings(ctx); err != nil {
		t.Fatal(err)
	}
	if ve.recordingExists(later) || len(ve.blobKeys("recordings/")) != 0 {
		t.Fatalf("expired recording %v, keyframes %v", ve.recordingExists(later), ve.blobKeys("recordings/"))
	}
}

// Without a blob store keyframes aren't taken (503); the trace upload still works.
func TestRecordingKeyframesNeedBlobs(t *testing.T) {
	withEnvExtra(t, envExtra{noBlobs: true})
	ve, _, _ := newRecToolEnv(t)
	uploadDesktopRecording(ve)
	ve.do("PUT", kfPath(kfRecID, "1", false), testJPEG(t, 8, 8, 0), ve.node.h()).want(503)
}
