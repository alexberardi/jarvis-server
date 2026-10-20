package computer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

// keyframeSource is the desktop: it keeps a keyframe per event, <id>.<seq>.jpg in dir.
type keyframeSource struct {
	fakeSource
	dir string
	k   []Keyframe
}

func (s *keyframeSource) Keyframes(id string) []Keyframe {
	for i := range 2 {
		p := filepath.Join(s.dir, id+"."+strconv.Itoa(i)+".jpg")
		_ = os.WriteFile(p, []byte{0xff, 0xd8, byte(i)}, 0o600)
		s.k = append(s.k, Keyframe{Seq: i, Path: p})
	}
	return s.k
}

// A trace handed on (to be uploaded) is pending, and reads back from its file as it was, until
// it is sent; one the caller took (Stop) isn't.
func TestHandedOnTraceIsPending(t *testing.T) {
	e := newRecEnv(t)
	ks := &keyframeSource{dir: e.r.Dir}
	e.r.Sources[ModeDesktop] = ks
	first, _ := e.r.Start(context.Background(), ModeBrowser)
	e.src.send(`{"kind":"click","x":1}`)
	e.src.send(`{"kind":"type","text":"hi"}`)
	if _, err := e.r.End(); err != nil {
		t.Fatal(err)
	}
	second, _ := e.r.Start(context.Background(), ModeDesktop)
	ks.send(`{"kind":"click"}`)
	e.activity.Stop() // the status icon's "Stop Jarvis"
	taken, _ := e.r.Start(context.Background(), ModeBrowser)
	if _, err := e.r.Stop(taken); err != nil {
		t.Fatal(err)
	}

	got := e.r.Pending()
	handed := e.handedOn()
	if len(got) != 2 || got[0].ID != first || got[1].ID != second {
		t.Fatalf("pending %+v, want %s then %s", got, first, second)
	}
	for i, tr := range got {
		if !reflect.DeepEqual(TraceOutput(tr), TraceOutput(handed[i])) {
			t.Fatalf("read back\n%+v\nhanded on\n%+v", TraceOutput(tr), TraceOutput(handed[i]))
		}
	}
	if len(got[1].Keyframes) != 2 {
		t.Fatalf("keyframes %+v", got[1].Keyframes)
	}

	e.r.Sent(first)
	if got := e.r.Pending(); len(got) != 1 || got[0].ID != second {
		t.Fatalf("after sent: %+v", got)
	}
	if err := e.r.Delete(second); err != nil {
		t.Fatal(err)
	}
	if got := e.r.Pending(); len(got) != 0 {
		t.Fatalf("after delete: %+v", got)
	}
	if left, _ := filepath.Glob(filepath.Join(e.r.Dir, second+"*")); len(left) != 0 {
		t.Fatalf("left after delete: %v", left)
	}
}

// A pending marker whose trace is gone is dropped; one with no recordings dir is no error.
func TestPendingWithoutTrace(t *testing.T) {
	e := newRecEnv(t)
	if got := e.r.Pending(); len(got) != 0 {
		t.Fatalf("no dir: %+v", got)
	}
	_ = os.MkdirAll(e.r.Dir, 0o700)
	marker := filepath.Join(e.r.Dir, "rec_gone.pending")
	_ = os.WriteFile(marker, nil, 0o600)
	if got := e.r.Pending(); len(got) != 0 {
		t.Fatalf("pending %+v", got)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker kept: %v", err)
	}
}
