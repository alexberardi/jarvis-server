//go:build contract

package contract

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Helpers for the LLM, STT and TTS contracts: raw (non-JSON) requests with a longer timeout,
// multipart bodies, generated WAVs, WAV header parsing and strict SSE framing.

// EnvSlowTimeout bounds inference calls (LLM, STT, TTS synthesis), which can be much slower
// than the control-plane requests JARVIS_CONTRACT_TIMEOUT covers. Default 180s.
const EnvSlowTimeout = "JARVIS_CONTRACT_SLOW_TIMEOUT"

func slowTimeout() time.Duration {
	if v := os.Getenv(EnvSlowTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return 180 * time.Second
}

// RawResp is a fully read response plus the transport facts Resp drops: Go's client moves
// Transfer-Encoding out of the header map and into the response.
type RawResp struct {
	*Resp
	TransferEncoding []string
	ContentLength    int64
}

// Hdr is the first value of a response header ("" when absent).
func (r *Resp) HeaderVal(name string) string {
	if v := r.Header[http.CanonicalHeaderKey(name)]; len(v) > 0 {
		return v[0]
	}
	return ""
}

// HasHdr reports whether the response carries a header at all (even an empty one).
func (r *Resp) HasHeader(name string) bool {
	_, ok := r.Header[http.CanonicalHeaderKey(name)]
	return ok
}

// newMediaRequest builds a request with an explicit content type (empty: none).
func (tg *Target) newMediaRequest(listener, method, path, contentType string, body []byte, hs ...H) (*http.Request, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, tg.URL(listener, path), rdr)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for _, h := range hs {
		for k, v := range h {
			req.Header.Set(k, v)
		}
	}
	return req, nil
}

// SlowDo sends body with contentType using the slow (inference) timeout and reads the whole
// response. body may be nil, []byte, string, or any value (sent as JSON, contentType ignored).
func (tg *Target) SlowDo(t testing.TB, listener, method, path, contentType string, body any, hs ...H) *RawResp {
	t.Helper()
	var payload []byte
	switch b := body.(type) {
	case nil:
	case []byte:
		payload = b
	case string:
		payload = []byte(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		payload, contentType = buf, "application/json"
	}
	req, err := tg.newMediaRequest(listener, method, path, contentType, payload, hs...)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	client := &http.Client{Timeout: slowTimeout()}
	res, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, req.URL, err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("%s %s: read body: %v", method, req.URL, err)
	}
	return &RawResp{
		Resp:             &Resp{t: t, Method: method, URL: req.URL.String(), Status: res.StatusCode, Header: res.Header, Body: data},
		TransferEncoding: res.TransferEncoding,
		ContentLength:    res.ContentLength,
	}
}

// SlowJSON posts a JSON body with the slow timeout.
func (tg *Target) SlowJSON(t testing.TB, listener, path string, body any, hs ...H) *RawResp {
	t.Helper()
	return tg.SlowDo(t, listener, http.MethodPost, path, "application/json", body, hs...)
}

// ExpectHeader fails unless the response header equals want.
func (r *Resp) ExpectHeaderVal(name, want string) *Resp {
	r.t.Helper()
	if got := r.HeaderVal(name); got != want {
		r.Fatalf("header %s: want %q, got %q (present=%v)", name, want, got, r.HasHeader(name))
	}
	return r
}

// ExpectMediaType fails unless the Content-Type's media type (parameters ignored) is want.
func (r *Resp) ExpectMediaType(want string) *Resp {
	r.t.Helper()
	ct := r.HeaderVal("Content-Type")
	mt := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
	if mt != want {
		r.Fatalf("content-type: want %s, got %q", want, ct)
	}
	return r
}

// ExpectChunked fails unless the body was sent with chunked transfer encoding (no
// Content-Length), which is what lets the node start playback before synthesis ends.
func (r *RawResp) ExpectChunked() *RawResp {
	r.t.Helper()
	if len(r.TransferEncoding) == 0 || r.TransferEncoding[0] != "chunked" || r.ContentLength != -1 {
		r.Fatalf("want chunked transfer encoding, got transfer-encoding=%v content-length=%d", r.TransferEncoding, r.ContentLength)
	}
	return r
}

// --- multipart ---

// FormPart is one multipart form part. A part with a Filename is a file upload.
type FormPart struct {
	Name        string
	Filename    string
	ContentType string
	Data        []byte
}

// FormField is a plain text form field.
func FormField(name, value string) FormPart { return FormPart{Name: name, Data: []byte(value)} }

// FormFile is a WAV file upload, the way CC's httpx client sends it.
func FormFile(name, filename string, data []byte) FormPart {
	return FormPart{Name: name, Filename: filename, ContentType: "audio/wav", Data: data}
}

// Multipart encodes parts in order and returns the body and its Content-Type.
func MultipartBody(parts ...FormPart) ([]byte, string) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, p := range parts {
		var (
			pw  io.Writer
			err error
		)
		if p.Filename == "" {
			pw, err = w.CreateFormField(p.Name)
		} else {
			h := textproto.MIMEHeader{}
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, p.Name, p.Filename))
			h.Set("Content-Type", p.ContentType)
			pw, err = w.CreatePart(h)
		}
		if err != nil {
			panic(err)
		}
		if _, err := pw.Write(p.Data); err != nil {
			panic(err)
		}
	}
	if err := w.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes(), w.FormDataContentType()
}

// --- WAV ---

// SineWAV is a mono 16-bit PCM RIFF WAV of a sine tone, the shape a node uploads.
func SineWAV(sampleRate int, seconds, freq float64) []byte {
	n := int(float64(sampleRate) * seconds)
	pcm := make([]byte, 2*n)
	for i := 0; i < n; i++ {
		v := int16(0.3 * math.MaxInt16 * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate)))
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	var b bytes.Buffer
	le := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1)) // PCM
	le(uint16(1)) // mono
	le(uint32(sampleRate))
	le(uint32(sampleRate * 2))
	le(uint16(2))
	le(uint16(16))
	b.WriteString("data")
	le(uint32(len(pcm)))
	b.Write(pcm)
	return b.Bytes()
}

// WAVInfo is what a RIFF/WAVE header says about its PCM payload.
type WAVInfo struct {
	Format        uint16
	Channels      int
	SampleRate    int
	BitsPerSample int
	DataLen       int // the data chunk's declared size
	DataAvailable int // bytes actually present after the data chunk header
}

// ParseWAV walks the RIFF chunks of a WAV file.
func ParseWAV(b []byte) (WAVInfo, error) {
	var info WAVInfo
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return info, fmt.Errorf("not a RIFF/WAVE file (first bytes %q)", b[:min(len(b), 12)])
	}
	if got := int(binary.LittleEndian.Uint32(b[4:8])); got != len(b)-8 {
		return info, fmt.Errorf("RIFF size %d, want %d (file length - 8)", got, len(b)-8)
	}
	sawFmt := false
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		switch id {
		case "fmt ":
			if size < 16 || body+16 > len(b) {
				return info, fmt.Errorf("short fmt chunk (%d)", size)
			}
			info.Format = binary.LittleEndian.Uint16(b[body:])
			info.Channels = int(binary.LittleEndian.Uint16(b[body+2:]))
			info.SampleRate = int(binary.LittleEndian.Uint32(b[body+4:]))
			info.BitsPerSample = int(binary.LittleEndian.Uint16(b[body+14:]))
			sawFmt = true
		case "data":
			if !sawFmt {
				return info, fmt.Errorf("data chunk before fmt chunk")
			}
			info.DataLen = size
			info.DataAvailable = len(b) - body
			return info, nil
		}
		off = body + size + size%2
	}
	return info, fmt.Errorf("no data chunk")
}

// --- SSE ---

// SSEFrame is one `data: <json>\n\n` event.
type SSEFrame struct {
	Raw  string
	JSON any // decoded with UseNumber, like Resp.JSON
}

// ParseSSE splits a complete text/event-stream body into frames and fails unless every event
// is exactly one `data: ` line holding a JSON value, terminated by a blank line. That is the
// framing CC's parser relies on (`line.startswith("data: ")`, then json.loads).
func ParseSSE(body []byte) ([]SSEFrame, error) {
	s := string(body)
	if s == "" {
		return nil, fmt.Errorf("empty event stream")
	}
	if !strings.HasSuffix(s, "\n\n") {
		return nil, fmt.Errorf("stream does not end with a blank line: …%q", sseTail(s, 80))
	}
	var frames []SSEFrame
	for i, ev := range strings.Split(strings.TrimSuffix(s, "\n\n"), "\n\n") {
		f, err := parseSSEEvent(ev)
		if err != nil {
			return nil, fmt.Errorf("event %d: %w", i, err)
		}
		frames = append(frames, f)
	}
	return frames, nil
}

func parseSSEEvent(ev string) (SSEFrame, error) {
	if strings.Contains(ev, "\n") || strings.Contains(ev, "\r") {
		return SSEFrame{}, fmt.Errorf("multi-line event %q", ev)
	}
	if !strings.HasPrefix(ev, "data: ") {
		return SSEFrame{}, fmt.Errorf("event is not a `data: ` line: %q", ev)
	}
	dec := json.NewDecoder(strings.NewReader(ev[len("data: "):]))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return SSEFrame{}, fmt.Errorf("data is not JSON: %v (%q)", err, ev)
	}
	if dec.More() {
		return SSEFrame{}, fmt.Errorf("trailing data after JSON in %q", ev)
	}
	return SSEFrame{Raw: ev, JSON: v}, nil
}

// SSEStream reads an open event stream one event at a time.
type SSEStream struct {
	Res *http.Response
	rd  *bufio.Reader
}

// OpenStream sends a JSON POST and returns the open response for incremental reading. The
// caller must Close it.
func (tg *Target) OpenSSE(t testing.TB, listener, path string, body any, hs ...H) *SSEStream {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := tg.newMediaRequest(listener, http.MethodPost, path, "application/json", buf, hs...)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	res, err := (&http.Client{Timeout: slowTimeout()}).Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", req.URL, err)
	}
	return &SSEStream{Res: res, rd: bufio.NewReader(res.Body)}
}

// Next returns the next event, or io.EOF at a clean end of stream.
func (s *SSEStream) Next() (SSEFrame, error) {
	var lines []string
	for {
		line, err := s.rd.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" && len(lines) == 0 {
				return SSEFrame{}, io.EOF
			}
			if err == io.EOF {
				return SSEFrame{}, fmt.Errorf("stream ended mid-event: %q", strings.Join(append(lines, line), "\n"))
			}
			return SSEFrame{}, err
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			if len(lines) == 0 {
				return SSEFrame{}, fmt.Errorf("stray blank line")
			}
			return parseSSEEvent(strings.Join(lines, "\n"))
		}
		lines = append(lines, line)
	}
}

func (s *SSEStream) Close() { s.Res.Body.Close() }

// FrameObj is the frame's JSON as an object (nil when it is not one).
func (f SSEFrame) FrameObj() map[string]any {
	m, _ := f.JSON.(map[string]any)
	return m
}

// MatchFrame checks a frame against m and returns the mismatches.
func MatchFrame(f SSEFrame, path string, m Matcher) []string { return m.Match(path, f.JSON) }

func sseTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// atoiHeader parses a decimal header value; ok is false when it is absent or not an integer.
func atoiHeader(r *Resp, name string) (int, bool) {
	v := r.HeaderVal(name)
	n, err := strconv.Atoi(v)
	return n, err == nil && strconv.Itoa(n) == v
}

// EnvSSHCleanup, when set to an ssh destination (e.g. user@10.0.0.103) and a whisper repo path
// separated by a colon ("user@host:~/jarvis/jarvis-whisper-api"), lets the suite remove the
// empty voice_profiles/<household>/ directory the legacy purge leaves behind.
const EnvSSHCleanup = "JARVIS_CONTRACT_WHISPER_SSH_CLEANUP"

var safeID = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

func removeEmptyProfileDir(t *testing.T, householdID string) {
	t.Helper()
	target := os.Getenv(EnvSSHCleanup)
	if target == "" || !safeID.MatchString(householdID) {
		return
	}
	dest, repo, ok := strings.Cut(target, ":")
	if !ok {
		t.Logf("%s must be user@host:/path/to/jarvis-whisper-api", EnvSSHCleanup)
		return
	}
	// rmdir only removes an empty directory, so this can never delete a real profile.
	cmd := exec.Command("ssh", "-o", "BatchMode=yes", dest, "rmdir "+repo+"/voice_profiles/"+householdID+" 2>/dev/null; true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Logf("voice profile dir cleanup: %v %s", err, out)
	}
}
