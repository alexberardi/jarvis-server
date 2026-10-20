package notifications

// Inbox attachments (docs/decisions/2026-10-17-inbox-attachments.md): images and video a
// producer uploads first (POST /api/v0/inbox/attachments, or UploadInboxAttachment in process)
// and then names in the item it creates (attachment_ids). The bytes live in the blob store,
// content-addressed under inbox/sha256/; rows in notifications_inbox_attachments say which
// item (and so which user) each belongs to. Rows go with their item (ON DELETE CASCADE); the
// cleanup job's sweep removes unclaimed uploads and blobs no row references any more.

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // DecodeConfig
	_ "image/jpeg" // DecodeConfig
	_ "image/png"  // DecodeConfig
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	_ "golang.org/x/image/webp" // DecodeConfig

	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// Attachment settings: the byte caps per file and per item.
const (
	SettingAttachmentMaxFileBytes = "inbox.attachment_max_file_bytes"
	SettingAttachmentMaxItemBytes = "inbox.attachment_max_item_bytes"
)

func positiveInt(v any) error {
	if n, ok := v.(int64); ok && n <= 0 {
		return errors.New("must be a positive number of bytes")
	}
	return nil
}

var attachmentDefinitions = []settings.Definition{
	{Key: SettingAttachmentMaxFileBytes, Category: "inbox", Type: settings.Int, Default: int64(25 << 20), Validate: positiveInt,
		Description: "Largest inbox attachment (image or video) a service may upload, in bytes."},
	{Key: SettingAttachmentMaxItemBytes, Category: "inbox", Type: settings.Int, Default: int64(50 << 20), Validate: positiveInt,
		Description: "Largest total size of the attachments on one inbox item, in bytes."},
}

// attachmentGrace is how long an upload may wait to be claimed by an item, and how old an
// unreferenced blob must be before the sweep removes it (so an upload between writing its blob
// and its row is never swept).
const attachmentGrace = time.Hour

// sniffBytes is how much of an upload is kept to sniff its type and read image dimensions
// (a JPEG's size can sit behind tens of kilobytes of EXIF).
const sniffBytes = 256 << 10

// Attachment is one file on an inbox item, as the item JSON lists it. Width and height are
// known for images; duration is not read yet (no video parsing), so it is null.
type Attachment struct {
	ID         string `json:"id"`
	Mime       string `json:"mime"`
	Size       int64  `json:"size"`
	Width      *int   `json:"width"`
	Height     *int   `json:"height"`
	DurationMS *int64 `json:"duration_ms"`
}

// AttachmentError is a refused upload or claim; Status is the HTTP status to answer with.
type AttachmentError struct {
	Status int
	Detail string
}

func (e *AttachmentError) Error() string { return "notifications: attachment: " + e.Detail }

// IsAttachmentError reports whether err is (or wraps) an AttachmentError.
func IsAttachmentError(err error) bool {
	var ae *AttachmentError
	return errors.As(err, &ae)
}

// WriteAttachmentError answers err when it is an AttachmentError and reports whether it did.
func WriteAttachmentError(w http.ResponseWriter, err error) bool {
	var ae *AttachmentError
	if !errors.As(err, &ae) {
		return false
	}
	httpx.Error(w, ae.Status, ae.Detail)
	return true
}

func attachmentErr(status int, format string, args ...any) error {
	return &AttachmentError{Status: status, Detail: fmt.Sprintf(format, args...)}
}

func blobKey(sum string) string { return "inbox/sha256/" + sum[:2] + "/" + sum }

const tmpPrefix = "inbox/tmp/"

// boxBrands are ISO BMFF brands that are not MP4 video (HEIF/AVIF images, QuickTime).
var boxBrands = map[string]bool{"heic": true, "heix": true, "heim": true, "heis": true, "mif1": true, "msf1": true,
	"avif": true, "avis": true, "qt  ": true}

// sniffMedia names an allowed type from the content alone: image/png, jpeg, webp, gif and
// video/mp4, webm.
func sniffMedia(b []byte) (string, bool) {
	if len(b) >= 12 && string(b[4:8]) == "ftyp" {
		return "video/mp4", !boxBrands[string(b[8:12])]
	}
	switch t := http.DetectContentType(b); t {
	case "image/png", "image/jpeg", "image/webp", "image/gif", "video/webm":
		return t, true
	}
	return "", false
}

// XLSXMime is an Excel workbook, which only jarvisd itself attaches (UploadServerAttachment).
const XLSXMime = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

// isWorkbook is whether the upload of n bytes at key (head is its start) is an .xlsx: a zip
// with a [Content_Types].xml and an xl/workbook.xml.
func (m *Module) isWorkbook(ctx context.Context, key string, head []byte, n int64) bool {
	if !bytes.HasPrefix(head, []byte("PK\x03\x04")) {
		return false
	}
	b := head
	if n > int64(len(head)) {
		rc, _, err := m.deps.Blobs.Get(ctx, key)
		if err != nil {
			return false
		}
		defer rc.Close()
		if b, err = io.ReadAll(io.LimitReader(rc, n)); err != nil {
			return false
		}
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return false
	}
	var types, book bool
	for _, f := range z.File {
		types = types || f.Name == "[Content_Types].xml"
		book = book || f.Name == "xl/workbook.xml"
	}
	return types && book
}

// prefixWriter keeps the first max bytes written to it.
type prefixWriter struct {
	b   []byte
	max int
}

func (p *prefixWriter) Write(b []byte) (int, error) {
	if room := p.max - len(p.b); room > 0 {
		p.b = append(p.b, b[:min(room, len(b))]...)
	}
	return len(b), nil
}

func (m *Module) attachmentCaps(ctx context.Context) (perFile, perItem int64) {
	return m.settings.Int(ctx, SettingAttachmentMaxFileBytes, settings.Scope{}),
		m.settings.Int(ctx, SettingAttachmentMaxItemBytes, settings.Scope{})
}

// UploadInboxAttachment stores one file for an item that owner will create next (in
// householdID, when the uploader has one: a node's household; "" for an app, which names the
// household at create). The type comes from the content: contentType, when given and not
// application/octet-stream, must agree with it. The result's id goes in the item's
// AttachmentIDs; an upload no item claims within an hour is swept.
func (m *Module) UploadInboxAttachment(ctx context.Context, owner, householdID, contentType string, r io.Reader) (Attachment, error) {
	return m.uploadAttachment(ctx, owner, householdID, contentType, r, false)
}

// UploadServerAttachment is UploadInboxAttachment for a file jarvisd made itself (an errand's
// spreadsheet): an .xlsx workbook is allowed too. The upload routes never take one.
func (m *Module) UploadServerAttachment(ctx context.Context, owner, householdID, contentType string, r io.Reader) (Attachment, error) {
	return m.uploadAttachment(ctx, owner, householdID, contentType, r, true)
}

func (m *Module) uploadAttachment(ctx context.Context, owner, householdID, contentType string, r io.Reader, server bool) (Attachment, error) {
	if m.deps.Blobs == nil {
		return Attachment{}, attachmentErr(http.StatusServiceUnavailable, "Attachments are not available on this server")
	}
	maxFile, _ := m.attachmentCaps(ctx)
	id := newUUID()
	tmp := tmpPrefix + id
	sum := sha256.New()
	head := &prefixWriter{max: sniffBytes}
	n, err := m.deps.Blobs.Put(ctx, tmp, io.TeeReader(io.LimitReader(r, maxFile+1), io.MultiWriter(sum, head)), "application/octet-stream")
	defer func() { _ = m.deps.Blobs.Delete(context.WithoutCancel(ctx), tmp) }()
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return Attachment{}, attachmentErr(http.StatusRequestEntityTooLarge, "Attachment too large")
		}
		return Attachment{}, err
	}
	if n > maxFile {
		return Attachment{}, attachmentErr(http.StatusRequestEntityTooLarge, "Attachment too large (the limit is %d bytes)", maxFile)
	}
	typ, ok := sniffMedia(head.b)
	if !ok && server && m.isWorkbook(ctx, tmp, head.b, n) {
		typ, ok = XLSXMime, true
	}
	if !ok {
		return Attachment{}, attachmentErr(http.StatusUnsupportedMediaType,
			"Unsupported attachment type: send image/png, image/jpeg, image/webp, image/gif, video/mp4 or video/webm")
	}
	if declared, _, _ := mime.ParseMediaType(contentType); declared != "" && declared != "application/octet-stream" && declared != typ {
		return Attachment{}, attachmentErr(http.StatusUnsupportedMediaType, "The content is %s, not %s", typ, declared)
	}
	a := Attachment{ID: id, Mime: typ, Size: n}
	if strings.HasPrefix(typ, "image/") {
		if cfg, _, err := image.DecodeConfig(bytes.NewReader(head.b)); err == nil {
			a.Width, a.Height = &cfg.Width, &cfg.Height
		}
	}
	hash := hex.EncodeToString(sum.Sum(nil))
	// Always rewrite the blob, even when the hash is already stored: that refreshes its
	// modification time, which keeps the sweep off it until the row below references it.
	if err := m.copyBlob(ctx, tmp, blobKey(hash), typ); err != nil {
		return Attachment{}, err
	}
	var hh any
	if householdID != "" {
		hh = householdID
	}
	if _, err := m.deps.DB.Write.ExecContext(ctx, `INSERT INTO notifications_inbox_attachments
		(id, owner, household_id, sha256, mime, size, width, height, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, owner, hh, hash, typ, n, a.Width, a.Height, ts(m.now())); err != nil {
		return Attachment{}, err
	}
	m.deps.Log.Info("notifications: stored inbox attachment", "id", id, "mime", typ, "size", n, "owner", owner)
	return a, nil
}

func (m *Module) copyBlob(ctx context.Context, from, to, contentType string) error {
	rc, _, err := m.deps.Blobs.Get(ctx, from)
	if err != nil {
		return err
	}
	defer rc.Close()
	_, err = m.deps.Blobs.Put(ctx, to, rc, contentType)
	return err
}

// claimAttachments binds the uploads in.AttachmentIDs to item id, inside the item's
// transaction. Each must be in.AttachmentOwner's, unclaimed, and uploaded for this household
// (or for none); together they must fit the per-item cap.
func (m *Module) claimAttachments(ctx context.Context, tx *sql.Tx, itemID string, in NewInboxItem, maxItem int64) error {
	var ids []string
	seen := map[string]bool{}
	for _, id := range in.AttachmentIDs {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var total int64
	for _, id := range ids {
		var owner string
		var hh, item sql.NullString
		var size int64
		err := tx.QueryRowContext(ctx, `SELECT owner, household_id, item_id, size FROM notifications_inbox_attachments WHERE id = ?`, id).
			Scan(&owner, &hh, &item, &size)
		if errors.Is(err, sql.ErrNoRows) || err == nil && (owner != in.AttachmentOwner || in.AttachmentOwner == "" || item.Valid ||
			hh.Valid && hh.String != in.HouseholdID) {
			return attachmentErr(http.StatusUnprocessableEntity, "Unknown or already used attachment: %s", id)
		}
		if err != nil {
			return err
		}
		total += size
	}
	if total > maxItem {
		return attachmentErr(http.StatusRequestEntityTooLarge, "Attachments too large for one item (the limit is %d bytes)", maxItem)
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE notifications_inbox_attachments SET item_id = ?, household_id = ?, position = ? WHERE id = ?`,
			itemID, in.HouseholdID, i, id); err != nil {
			return err
		}
	}
	return nil
}

// loadAttachments fills in the attachments of items, in one query.
func loadAttachments(ctx context.Context, q querier, items []InboxItem) error {
	if len(items) == 0 {
		return nil
	}
	idx := map[string]int{}
	args := make([]any, 0, len(items))
	for i, it := range items {
		idx[it.ID] = i
		args = append(args, it.ID)
	}
	rows, err := q.QueryContext(ctx, `SELECT item_id, id, mime, size, width, height, duration_ms FROM notifications_inbox_attachments
		WHERE item_id IN (?`+strings.Repeat(", ?", len(args)-1)+`) ORDER BY item_id, position`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item string
		var a Attachment
		var w, h sql.NullInt64
		var d sql.NullInt64
		if err := rows.Scan(&item, &a.ID, &a.Mime, &a.Size, &w, &h, &d); err != nil {
			return err
		}
		if w.Valid && h.Valid {
			wi, hi := int(w.Int64), int(h.Int64)
			a.Width, a.Height = &wi, &hi
		}
		if d.Valid {
			a.DurationMS = &d.Int64
		}
		i := idx[item]
		items[i].Attachments = append(items[i].Attachments, a)
	}
	return rows.Err()
}

// --- HTTP ---

// handleUploadAttachment is POST /api/v0/inbox/attachments (app-to-app): the raw file as the
// body, its type as Content-Type. 201 with the Attachment.
func (m *Module) handleUploadAttachment(w http.ResponseWriter, r *http.Request) {
	app, _ := authn.AppFrom(r.Context())
	a, err := m.UploadInboxAttachment(r.Context(), "app:"+app.ID, "", r.Header.Get("Content-Type"), r.Body)
	if WriteAttachmentError(w, err) {
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, a)
}

// handleGetAttachment is GET /api/v0/inbox/{item_id}/attachments/{attachment_id}: the file, to
// whoever may see the item (404 for anyone else), with Range support. It does not mark the
// item read.
func (m *Module) handleGetAttachment(w http.ResponseWriter, r *http.Request, u authn.User) {
	if !m.needHousehold(w, r, u) {
		return
	}
	var hash, typ string
	err := m.deps.DB.Read.QueryRowContext(r.Context(), `SELECT sha256, mime FROM notifications_inbox_attachments
		WHERE id = ? AND item_id IN (SELECT id FROM notifications_inbox_items WHERE id = ? AND `+visible+`)`,
		r.PathValue("attachment_id"), r.PathValue("item_id"), u.HouseholdID, u.ID).Scan(&hash, &typ)
	if errors.Is(err, sql.ErrNoRows) || err == nil && m.deps.Blobs == nil {
		httpx.Error(w, http.StatusNotFound, "Attachment not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	rs, _, err := blob.OpenSeeker(r.Context(), m.deps.Blobs, blobKey(hash))
	if errors.Is(err, blob.ErrNotFound) {
		httpx.Error(w, http.StatusNotFound, "Attachment not found")
		return
	}
	if err != nil {
		m.internalError(w, err)
		return
	}
	defer rs.Close()
	h := w.Header()
	h.Set("Content-Type", typ)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Disposition", "inline")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, max-age=86400")
	h.Set("ETag", `"`+hash+`"`)
	http.ServeContent(w, r, "", time.Time{}, rs)
}

// --- sweep ---

// SweepAttachments deletes uploads no item claimed within attachmentGrace, then every blob
// under inbox/ that no attachment row references and that is older than attachmentGrace
// (blobs are shared by content, so deleting an item only deletes its rows). It returns the
// number of blobs removed.
func (m *Module) SweepAttachments(ctx context.Context) (int, error) {
	if m.deps.Blobs == nil {
		return 0, nil
	}
	cutoff := m.now().Add(-attachmentGrace)
	if _, err := m.deps.DB.Write.ExecContext(ctx, `DELETE FROM notifications_inbox_attachments
		WHERE item_id IS NULL AND julianday(created_at) < julianday(?)`, ts(cutoff)); err != nil {
		return 0, err
	}
	// List before reading the references: a blob written after the list is not considered,
	// and one re-written by an upload in between is caught by the re-Stat below.
	objs, err := m.deps.Blobs.List(ctx, "inbox/")
	if err != nil {
		return 0, err
	}
	refs := map[string]bool{}
	rows, err := m.deps.DB.Read.QueryContext(ctx, `SELECT DISTINCT sha256 FROM notifications_inbox_attachments`)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return 0, err
		}
		refs[s] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	removed := 0
	for _, o := range objs {
		hash := o.Key[strings.LastIndex(o.Key, "/")+1:]
		if !o.ModTime.Before(cutoff) || strings.HasPrefix(o.Key, "inbox/sha256/") && refs[hash] {
			continue
		}
		if st, err := m.deps.Blobs.Stat(ctx, o.Key); err != nil || !st.ModTime.Before(cutoff) {
			continue
		}
		if err := m.deps.Blobs.Delete(ctx, o.Key); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
