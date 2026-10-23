package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// fakeJarvisd is jarvisd's POST /api/v0/computer/recordings and its keyframes' PUTs: it
// answers status (200 when 0; a PUT's own in putStatus first), counts the uploads by recording
// id and keeps the keyframes' paths. A transport, so no listener is needed.
type fakeJarvisd struct {
	mu        sync.Mutex
	status    int
	posts     map[string]int
	images    bool // an upload carried image_attachments
	putStatus map[string]int
	puts      []string
	refused   map[string]int // PUTs answered with putStatus, by path
}

func (j *fakeJarvisd) RoundTrip(req *http.Request) (*http.Response, error) {
	var body struct {
		ID     string `json:"recording_id"`
		Images []any  `json:"image_attachments"`
	}
	if req.Method == http.MethodPost {
		_ = json.NewDecoder(req.Body).Decode(&body)
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	status := j.status
	if s := j.putStatus[req.URL.Path]; s != 0 && req.Method == http.MethodPut {
		status = s
		if j.refused == nil {
			j.refused = map[string]int{}
		}
		j.refused[req.URL.Path]++
	}
	if status == 0 {
		status = http.StatusOK
	}
	if req.URL.Path == "/api/v0/computer/recordings" && status == http.StatusOK {
		j.posts[body.ID]++
		j.images = j.images || len(body.Images) > 0
	}
	if req.Method == http.MethodPut && status == http.StatusOK {
		if ct := req.Header.Get("Content-Type"); ct != "image/jpeg" {
			status = http.StatusBadRequest
		} else {
			j.puts = append(j.puts, req.URL.Path)
		}
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}},
		Body: io.NopCloser(bytes.NewReader([]byte(`{}`))), Request: req}, nil
}

func (j *fakeJarvisd) set(status int) { j.mu.Lock(); j.status = status; j.mu.Unlock() }

func (j *fakeJarvisd) setPut(path string, status int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.putStatus == nil {
		j.putStatus = map[string]int{}
	}
	j.putStatus[path] = status
}

func (j *fakeJarvisd) keyframes() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return slices.Clone(j.puts)
}

// refusals is how many PUTs to path got its putStatus.
func (j *fakeJarvisd) refusals(path string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.refused[path]
}

func (j *fakeJarvisd) attachments() bool { j.mu.Lock(); defer j.mu.Unlock(); return j.images }

func (j *fakeJarvisd) uploads() map[string]int {
	j.mu.Lock()
	defer j.mu.Unlock()
	out := map[string]int{}
	for k, v := range j.posts {
		out[k] = v
	}
	return out
}

type uploadEnv struct {
	rec     *computer.Recorder
	up      *recordingUploads
	src     *fakeRecSource
	jarvisd *fakeJarvisd
	cc      *node.CC
	live    func() *node.CC
	mu      sync.Mutex
	conn    *node.CC
	notes   chan string
}

func newUploadEnv(t *testing.T) *uploadEnv {
	t.Helper()
	e := &uploadEnv{src: &fakeRecSource{}, jarvisd: &fakeJarvisd{posts: map[string]int{}}, notes: make(chan string, 10)}
	e.cc = &node.CC{BaseURL: "http://jarvisd.test", HTTP: &http.Client{Transport: e.jarvisd}}
	e.live = func() *node.CC { e.mu.Lock(); defer e.mu.Unlock(); return e.conn }
	e.rec = &computer.Recorder{Dir: filepath.Join(t.TempDir(), "recordings"), Activity: &computer.Activity{},
		Sources: map[string]computer.Source{computer.ModeBrowser: e.src}}
	e.up = &recordingUploads{rec: e.rec, cc: e.live, note: func(s string) { e.notes <- s },
		log: slog.New(slog.DiscardHandler), every: time.Hour}
	e.rec.Stopped = e.up.stopped
	return e
}

func (e *uploadEnv) connect(cc *node.CC) { e.mu.Lock(); e.conn = cc; e.mu.Unlock() }

// record makes one recording with one event and stops it from the menu.
func (e *uploadEnv) record(t *testing.T) string {
	t.Helper()
	id, err := e.rec.Start(context.Background(), computer.ModeBrowser)
	if err != nil {
		t.Fatal(err)
	}
	e.src.emit(json.RawMessage(`{"kind":"click"}`))
	if _, err := e.rec.End(); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *uploadEnv) note(t *testing.T) string {
	t.Helper()
	select {
	case n := <-e.notes:
		return n
	case <-time.After(5 * time.Second):
		t.Fatal("the user wasn't told")
		return ""
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A recording stopped while not connected is kept, the user is told so, and it is sent on the
// next connect, oldest first, once.
func TestRecordingStoppedOfflineSentOnConnect(t *testing.T) {
	e := newUploadEnv(t)
	first := e.record(t)
	if n := e.note(t); n != recordingKeptNote {
		t.Fatalf("note %q", n)
	}
	second := e.record(t)
	e.note(t)
	if got := e.rec.Pending(); len(got) != 2 || got[0].ID != first {
		t.Fatalf("pending %+v", got)
	}

	e.connect(e.cc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.up.serve(ctx, e.cc); close(done) }()
	waitFor(t, "not sent on connect", func() bool { return len(e.rec.Pending()) == 0 })
	cancel()
	<-done
	if got := e.jarvisd.uploads(); got[first] != 1 || got[second] != 1 || len(got) != 2 {
		t.Fatalf("uploads %v", got)
	}
}

// A failed upload stays pending and the user is told; the next try (while connected) sends it.
func TestFailedRecordingUploadRetried(t *testing.T) {
	e := newUploadEnv(t)
	e.connect(e.cc)
	e.jarvisd.set(http.StatusServiceUnavailable)
	id := e.record(t)
	if n := e.note(t); n != recordingKeptNote {
		t.Fatalf("note %q", n)
	}
	if got := e.rec.Pending(); len(got) != 1 || got[0].ID != id {
		t.Fatalf("pending %+v", got)
	}

	e.jarvisd.set(0)
	e.up.every = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.up.serve(ctx, e.cc); close(done) }()
	waitFor(t, "not retried", func() bool { return e.jarvisd.uploads()[id] == 1 })
	waitFor(t, "still pending", func() bool { return len(e.rec.Pending()) == 0 })
	time.Sleep(20 * time.Millisecond) // more sweeps: nothing is sent again
	cancel()
	<-done
	if got := e.jarvisd.uploads(); got[id] != 1 {
		t.Fatalf("uploads %v", got)
	}
	select {
	case n := <-e.notes:
		t.Fatalf("told %q after it was sent", n)
	default:
	}

	// One jarvisd refuses for good (the id is another node's) isn't tried again.
	e.jarvisd.set(http.StatusConflict)
	refused := e.record(t)
	if n := e.note(t); n != recordingRefusedNote {
		t.Fatalf("note %q", n)
	}
	if got := e.rec.Pending(); len(got) != 0 {
		t.Fatalf("refused %s still pending: %+v", refused, got)
	}
}

// Sweeps racing each other and a stop send each recording once.
func TestRecordingUploadedOnce(t *testing.T) {
	e := newUploadEnv(t)
	ids := []string{e.record(t), e.record(t)}
	e.note(t)
	e.note(t)
	e.connect(e.cc)
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() { defer wg.Done(); e.up.sweep(e.cc) }()
	}
	ids = append(ids, e.record(t))
	wg.Wait()
	waitFor(t, "the last one not sent", func() bool { return len(e.rec.Pending()) == 0 })
	got := e.jarvisd.uploads()
	for _, id := range ids {
		if got[id] != 1 {
			t.Fatalf("uploads %v", got)
		}
	}
}
