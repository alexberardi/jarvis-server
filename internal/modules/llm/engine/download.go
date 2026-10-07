package engine

import (
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Download fetches one file resumably: bytes land in Part (HTTP Range continues an earlier
// attempt, even across restarts), and only a file whose size and sha256 check out is renamed
// to Dest, so an engine never sees a half-written file (05 §7.2). Model weights and engine
// archives both use it.
type Download struct {
	URL    string
	Dest   string
	Part   string // default Dest + ".part"
	Size   int64  // expected bytes; 0 = unknown
	SHA256 string // expected hex digest; "" = not verified
	Header http.Header
	Client *http.Client
	// Progress is called with the bytes on disk so far, at most every ProgressEvery.
	Progress      func(done int64)
	ProgressEvery time.Duration // default 500ms
}

// HTTPError is a non-2xx answer from the download server.
type HTTPError struct {
	URL    string
	Status int
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("download %s: HTTP %d %s", e.URL, e.Status, http.StatusText(e.Status))
}

// Permanent reports whether retrying cannot help (auth, missing file).
func (e *HTTPError) Permanent() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden || e.Status == http.StatusNotFound
}

// ErrChecksum is returned when the finished file's size or sha256 is wrong. The partial file
// is deleted, so a retry starts over.
var ErrChecksum = errors.New("download: checksum mismatch")

// hashState is the saved sha256 state of a partial file, so a resume needn't rehash
// gigabytes. It is trusted only when Offset equals the part file's size.
type hashState struct {
	Offset int64  `json:"offset"`
	State  []byte `json:"state"`
}

const saveStateEvery = 64 << 20

// Run downloads (or finishes downloading) the file.
func (d Download) Run(ctx context.Context) error {
	if d.Part == "" {
		d.Part = d.Dest + ".part"
	}
	if d.Client == nil {
		d.Client = http.DefaultClient
	}
	if d.ProgressEvery <= 0 {
		d.ProgressEvery = 500 * time.Millisecond
	}
	if err := os.MkdirAll(filepath.Dir(d.Part), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(d.Part, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			f.Close()
		}
	}()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	off := st.Size()
	if d.Size > 0 && off > d.Size {
		off = 0
	}
	h := sha256.New()
	if off > 0 {
		if !d.loadState(h, off) {
			h.Reset()
			if _, err := io.Copy(h, io.NewSectionReader(f, 0, off)); err != nil {
				return err
			}
		}
	}
	if !(d.Size > 0 && off == d.Size) {
		off, err = d.fetch(ctx, f, h, off)
		if err != nil {
			return err
		}
	}
	if d.Progress != nil {
		d.Progress(off)
	}
	if err := f.Sync(); err != nil {
		return err
	}
	closed = true
	if err := f.Close(); err != nil {
		return err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if (d.Size > 0 && off != d.Size) || (d.SHA256 != "" && !strings.EqualFold(sum, d.SHA256)) {
		os.Remove(d.Part)
		os.Remove(d.statePath())
		return fmt.Errorf("%w: %s: got %d bytes sha256 %s, want %d bytes sha256 %s",
			ErrChecksum, filepath.Base(d.Dest), off, sum, d.Size, d.SHA256)
	}
	if err := os.MkdirAll(filepath.Dir(d.Dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(d.Part, d.Dest); err != nil {
		return err
	}
	os.Remove(d.statePath())
	return nil
}

func (d Download) statePath() string { return d.Part + ".sha256" }

func (d Download) loadState(h hash.Hash, off int64) bool {
	b, err := os.ReadFile(d.statePath())
	if err != nil {
		return false
	}
	var s hashState
	if json.Unmarshal(b, &s) != nil || s.Offset != off {
		return false
	}
	return h.(encoding.BinaryUnmarshaler).UnmarshalBinary(s.State) == nil
}

func (d Download) saveState(h hash.Hash, off int64) {
	st, err := h.(encoding.BinaryMarshaler).MarshalBinary()
	if err != nil {
		return
	}
	b, _ := json.Marshal(hashState{Offset: off, State: st})
	_ = os.WriteFile(d.statePath(), b, 0o644)
}

// fetch requests the bytes from off on and appends them, returning the new size.
func (d Download) fetch(ctx context.Context, f *os.File, h hash.Hash, off int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if err != nil {
		return off, err
	}
	for k, v := range d.Header {
		req.Header[k] = v
	}
	if off > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(off, 10)+"-")
	}
	resp, err := d.Client.Do(req)
	if err != nil {
		return off, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusPartialContent && off > 0:
	case resp.StatusCode == http.StatusOK:
		// No range support (or a fresh start): begin again.
		if off > 0 {
			off = 0
			h.Reset()
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && off > 0:
		// Everything is already here; Run verifies it.
		return off, nil
	default:
		return off, &HTTPError{URL: d.URL, Status: resp.StatusCode}
	}
	if err := f.Truncate(off); err != nil {
		return off, err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off, err
	}
	buf := make([]byte, 1<<20)
	last, lastSave := time.Now(), off
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, err := f.Write(buf[:n]); err != nil {
				return off, err
			}
			h.Write(buf[:n])
			off += int64(n)
			if off-lastSave >= saveStateEvery {
				d.saveState(h, off)
				lastSave = off
			}
			if d.Progress != nil && time.Since(last) >= d.ProgressEvery {
				d.Progress(off)
				last = time.Now()
			}
		}
		if rerr == io.EOF {
			return off, nil
		}
		if rerr != nil {
			// Keep what arrived for the next attempt.
			d.saveState(h, off)
			return off, rerr
		}
	}
}
