package cc

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"image/jpeg"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// A desktop recording's keyframes (#344): after the trace, the computer's agent sends each
// click's keyframe (the screen, the click marked on Wayland) and its crop (the 256 px around
// the click), one JPEG per PUT /api/v0/computer/recordings/{id}/keyframes/{seq}[/crop]. They
// are kept in the blob store under recordings/<id>/<seq>.jpg and <seq>.crop.jpg, for as long as
// the recording (deleteRecordingRow, the retention sweep), within keyframeMaxBytes /
// keyframeCropMaxBytes each and recordingKeyframesMax keyframes or recordingKeyframeBytesMax
// bytes a recording (decision 2026-10-20-recording-keyframes.md).

const (
	keyframeMaxBytes     = 2 << 20   // one keyframe
	keyframeCropMaxBytes = 256 << 10 // one crop
	keyframeLimitCode    = "keyframe_limit"
	recordingBlobPrefix  = "recordings/"
	keyframeCropSuffix   = ".crop.jpg"
)

// The per-recording limits (vars: tests lower them).
var (
	recordingKeyframesMax           = 300
	recordingKeyframeBytesMax int64 = 100 << 20
)

// KeyframeImage is one click keyframe the server keeps for a recording.
type KeyframeImage struct {
	Seq  int  `json:"seq"`
	Crop bool `json:"crop"` // its crop is kept too
}

func recordingBlobDir(id string) string { return recordingBlobPrefix + id + "/" }

func keyframeKey(id string, seq int, crop bool) string {
	if crop {
		return recordingBlobDir(id) + strconv.Itoa(seq) + keyframeCropSuffix
	}
	return recordingBlobDir(id) + strconv.Itoa(seq) + ".jpg"
}

// handlePutKeyframe: PUT /api/v0/computer/recordings/{id}/keyframes/{seq}[/crop], node auth,
// a JPEG body. Only the computer that uploaded the recording adds to it, and only for an event
// of its trace that is a click; the same one again replaces it (the agent retries).
func (m *Module) handlePutKeyframe(crop bool) nodeHandler {
	return func(w http.ResponseWriter, r *http.Request, n *nodeCtx) {
		if m.deps.Blobs == nil {
			detail(w, http.StatusServiceUnavailable, "This server keeps no recording keyframes (no blob store)")
			return
		}
		if n.row == nil || !n.row.desktop() {
			detail(w, http.StatusForbidden, "Only a computer's agent uploads recordings")
			return
		}
		seq, err := strconv.Atoi(r.PathValue("seq"))
		if err != nil || seq < 1 {
			validationError(w, "path -> seq: Input should be a positive integer")
			return
		}
		id := r.PathValue("id")
		ctx := r.Context()
		if ok, err := m.recordingOfNode(ctx, id, n); err != nil {
			m.internalError(w, err)
			return
		} else if !ok {
			detail(w, http.StatusNotFound, "Recording not found")
			return
		}
		if click, err := m.recordingClick(ctx, id, seq); err != nil {
			m.internalError(w, err)
			return
		} else if !click {
			detail(w, http.StatusUnprocessableEntity, fmt.Sprintf("Event %d of this recording is not a click", seq))
			return
		}
		limit := int64(keyframeMaxBytes)
		if crop {
			limit = keyframeCropMaxBytes
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			validationError(w, "body: "+err.Error())
			return
		}
		if int64(len(data)) > limit {
			detail(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("A keyframe is at most %d KiB", limit>>10))
			return
		}
		if _, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
			validationError(w, "body: Input should be a JPEG")
			return
		}
		key := keyframeKey(id, seq, crop)
		var full errKeyframeLimit
		if err := m.putKeyframe(ctx, id, key, data); errors.As(err, &full) {
			detailCode(w, http.StatusRequestEntityTooLarge, full.msg, keyframeLimitCode)
			return
		} else if err != nil {
			m.internalError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "stored", "id": id, "seq": seq, "crop": crop})
	}
}

// handleGetKeyframe: GET /api/v0/recordings/{id}/keyframes/{seq}[/crop], the owner's (the
// Recordings page shows them), the JPEG as stored.
func (m *Module) handleGetKeyframe(crop bool) userHandler {
	return func(w http.ResponseWriter, r *http.Request, u authn.User) {
		seq, err := strconv.Atoi(r.PathValue("seq"))
		if err != nil || seq < 1 {
			validationError(w, "path -> seq: Input should be a positive integer")
			return
		}
		rec, err := m.ownRecording(r.Context(), u, r.PathValue("id"))
		if err != nil {
			m.writeErr(w, err)
			return
		}
		if m.deps.Blobs == nil {
			detail(w, http.StatusNotFound, "Keyframe not found")
			return
		}
		rs, _, err := blob.OpenSeeker(r.Context(), m.deps.Blobs, keyframeKey(rec.ID, seq, crop))
		if errors.Is(err, blob.ErrNotFound) {
			detail(w, http.StatusNotFound, "Keyframe not found")
			return
		} else if err != nil {
			m.internalError(w, err)
			return
		}
		defer rs.Close()
		h := w.Header()
		h.Set("Content-Type", "image/jpeg")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "private, max-age=86400")
		http.ServeContent(w, r, "", time.Time{}, rs)
	}
}

// errKeyframeLimit is a recording that has all the keyframes it may keep.
type errKeyframeLimit struct{ msg string }

func (e errKeyframeLimit) Error() string { return e.msg }

// putKeyframe stores data under key unless the recording's keyframes would then be more than
// its limits (what key already holds doesn't count: it is replaced).
func (m *Module) putKeyframe(ctx context.Context, id, key string, data []byte) error {
	m.keyframeMu.Lock()
	defer m.keyframeMu.Unlock()
	infos, err := m.deps.Blobs.List(ctx, recordingBlobDir(id))
	if err != nil {
		return err
	}
	frames, size := 0, int64(len(data))
	for _, i := range infos {
		if i.Key == key {
			continue
		}
		size += i.Size
		if !strings.HasSuffix(i.Key, keyframeCropSuffix) {
			frames++
		}
	}
	if !strings.HasSuffix(key, keyframeCropSuffix) && frames >= recordingKeyframesMax {
		return errKeyframeLimit{fmt.Sprintf("This recording keeps at most %d keyframes", recordingKeyframesMax)}
	}
	if size > recordingKeyframeBytesMax {
		return errKeyframeLimit{fmt.Sprintf("This recording keeps at most %d MiB of keyframes", recordingKeyframeBytesMax>>20)}
	}
	_, err = m.deps.Blobs.Put(ctx, key, bytes.NewReader(data), "image/jpeg")
	return err
}

// recordingOfNode reports whether recording id is there and was uploaded by node n.
func (m *Module) recordingOfNode(ctx context.Context, id string, n *nodeCtx) (bool, error) {
	if !recordingIDPattern.MatchString(id) {
		return false, nil
	}
	var node sql.NullString
	var hh string
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT node_id, household_id FROM cc_recordings WHERE id = ?`, id).Scan(&node, &hh)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return node.String == n.ID && hh == n.HouseholdID, nil
}

// recordingClick reports whether recording id's trace has a click numbered seq.
func (m *Module) recordingClick(ctx context.Context, id string, seq int) (bool, error) {
	var found int
	err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM cc_recordings r, json_each(r.trace) e
		WHERE r.id = ? AND json_type(r.trace) = 'array' AND json_extract(e.value, '$.seq') = ?
		AND json_extract(e.value, '$.kind') = 'click')`, id, seq).Scan(&found)
	return found == 1, err
}

// keyframeImages is the click keyframes kept for recording id, by seq.
func (m *Module) keyframeImages(ctx context.Context, id string) ([]KeyframeImage, error) {
	if m.deps.Blobs == nil {
		return nil, nil
	}
	infos, err := m.deps.Blobs.List(ctx, recordingBlobDir(id))
	if err != nil {
		return nil, err
	}
	bySeq := map[int]*KeyframeImage{}
	for _, i := range infos {
		name := strings.TrimPrefix(i.Key, recordingBlobDir(id))
		crop := strings.HasSuffix(name, keyframeCropSuffix)
		seq, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSuffix(name, keyframeCropSuffix), ".jpg"))
		if err != nil {
			continue
		}
		k := bySeq[seq]
		if k == nil {
			k = &KeyframeImage{Seq: seq}
			bySeq[seq] = k
		}
		k.Crop = k.Crop || crop
	}
	out := []KeyframeImage{}
	for _, k := range bySeq {
		out = append(out, *k)
	}
	slices.SortFunc(out, func(a, b KeyframeImage) int { return a.Seq - b.Seq })
	return out, nil
}

// deleteKeyframes removes recording id's keyframes. A failure is logged: the sweep's orphan
// pass (sweepOrphanKeyframes) tries again.
func (m *Module) deleteKeyframes(ctx context.Context, id string) {
	if m.deps.Blobs == nil {
		return
	}
	infos, err := m.deps.Blobs.List(ctx, recordingBlobDir(id))
	if err == nil {
		for _, i := range infos {
			if err = m.deps.Blobs.Delete(ctx, i.Key); err != nil {
				break
			}
		}
	}
	if err != nil {
		m.deps.Log.Warn("cc: couldn't delete a recording's keyframes", "recording", id, "err", err)
	}
}

// sweepOrphanKeyframes deletes the keyframes of recordings that are gone (one uploaded while
// its recording was deleted, a delete that failed).
func (m *Module) sweepOrphanKeyframes(ctx context.Context) error {
	if m.deps.Blobs == nil {
		return nil
	}
	infos, err := m.deps.Blobs.List(ctx, recordingBlobPrefix)
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for _, i := range infos {
		id, _, _ := strings.Cut(strings.TrimPrefix(i.Key, recordingBlobPrefix), "/")
		if _, seen := known[id]; !seen {
			var n int
			if err := m.deps.DB.Read.QueryRowContext(ctx, `SELECT COUNT(*) FROM cc_recordings WHERE id = ?`, id).Scan(&n); err != nil {
				return err
			}
			known[id] = n > 0
		}
		if !known[id] {
			if err := m.deps.Blobs.Delete(ctx, i.Key); err != nil {
				return err
			}
		}
	}
	return nil
}
