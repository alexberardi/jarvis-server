package evdev

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"
)

// frame is one input_event as a 64-bit kernel writes it.
func frame(sec, usec int64, typ, code uint16, value int32) []byte {
	var b bytes.Buffer
	_ = binary.Write(&b, binary.LittleEndian, sec)
	_ = binary.Write(&b, binary.LittleEndian, usec)
	_ = binary.Write(&b, binary.LittleEndian, typ)
	_ = binary.Write(&b, binary.LittleEndian, code)
	_ = binary.Write(&b, binary.LittleEndian, value)
	return b.Bytes()
}

func TestRead(t *testing.T) {
	var stream []byte
	stream = append(stream, frame(1760000000, 250000, EvKey, KeyA, 1)...)
	stream = append(stream, frame(1760000000, 250001, EvSyn, 0, 0)...)
	stream = append(stream, frame(1760000001, 0, EvKey, BtnLeft, 0)...)
	stream = append(stream, frame(1760000001, 0, EvRel, RelWheel, -1)...)
	var got []Event
	if err := Read(bytes.NewReader(stream), func(e Event) { got = append(got, e) }); err != nil {
		t.Fatal(err)
	}
	want := []Event{
		{Time: time.Unix(1760000000, 250000*1000), Type: EvKey, Code: KeyA, Value: 1},
		{Time: time.Unix(1760000001, 0), Type: EvKey, Code: BtnLeft, Value: 0},
		{Time: time.Unix(1760000001, 0), Type: EvRel, Code: RelWheel, Value: -1},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if !got[i].Time.Equal(want[i].Time) || got[i].Type != want[i].Type || got[i].Code != want[i].Code || got[i].Value != want[i].Value {
			t.Fatalf("event %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestReadTornFrame(t *testing.T) {
	stream := append(frame(1, 0, EvKey, KeyA, 1), frame(2, 0, EvKey, KeyA, 0)[:10]...)
	var n int
	err := Read(bytes.NewReader(stream), func(Event) { n++ })
	if n != 1 || err == nil {
		t.Fatalf("n=%d err=%v: a torn last frame is an error, the whole ones still count", n, err)
	}
}

func TestBits(t *testing.T) {
	// A keyboard's capabilities/key: 64-bit words, most significant first.
	kbd := "1000000000007 ff9f207ac14057ff febeffdfffefffff fffffffffffffffe"
	if !hasBits(kbd, KeyA, KeyZ, KeyEnter) || hasBits(kbd, BtnLeft) {
		t.Fatal("a keyboard has letters and no left button")
	}
	mouse := "70000 0 0 0 0"
	if !hasBits(mouse, BtnLeft) || hasBits(mouse, KeyA) {
		t.Fatal("a mouse has a left button and no letters")
	}
	if hasBits("", KeyA) || hasBits("zz", KeyA) {
		t.Fatal("nothing")
	}
}

// fakeTree is a sysfs and /dev/input with the given devices.
func fakeTree(t *testing.T, devs map[string][2]string) (sys, dev string) {
	t.Helper()
	root := t.TempDir()
	sys, dev = filepath.Join(root, "sys"), filepath.Join(root, "dev")
	for node, d := range devs {
		caps := filepath.Join(sys, node, "device", "capabilities")
		if err := os.MkdirAll(caps, 0o755); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(sys, node, "device", "name"), []byte(d[0]+"\n"), 0o644)
		_ = os.WriteFile(filepath.Join(caps, "key"), []byte(d[1]+"\n"), 0o644)
	}
	_ = os.MkdirAll(dev, 0o755)
	return sys, dev
}

func TestDevices(t *testing.T) {
	sys, dev := fakeTree(t, map[string][2]string{
		"event0": {"Power Button", "10000000000000 0"},
		"event3": {"AT Translated Set 2 keyboard", "402000000 3803078f800d001 feffffdfffefffff fffffffffffffffe"},
		"event7": {"Logitech USB Mouse", "1f0000 0 0 0 0"},
		"event9": {"Video Bus", "3e000b00000000 0 0 0"},
	})
	got, err := Devices(sys, dev)
	if err != nil {
		t.Fatal(err)
	}
	want := []Device{
		{Path: filepath.Join(dev, "event3"), Name: "AT Translated Set 2 keyboard", Keyboard: true},
		{Path: filepath.Join(dev, "event7"), Name: "Logitech USB Mouse", Pointer: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestOpenReadsEveryDevice(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "event1"), filepath.Join(dir, "event2")
	_ = os.WriteFile(a, frame(1, 0, EvKey, KeyA, 1), 0o600)
	_ = os.WriteFile(b, frame(2, 0, EvKey, BtnLeft, 1), 0o600)
	var mu sync.Mutex
	var got []uint16
	done := make(chan struct{}, 2)
	stop, err := Open([]Device{{Path: a, Keyboard: true}, {Path: b, Pointer: true}}, func(e Event) {
		mu.Lock()
		got = append(got, e.Code)
		mu.Unlock()
		done <- struct{}{}
	})
	if err != nil {
		t.Fatal(err)
	}
	<-done
	<-done
	stop()
	slices.Sort(got)
	if !slices.Equal(got, []uint16{KeyA, BtnLeft}) {
		t.Fatalf("got %v", got)
	}
}

func TestOpenWithoutAccess(t *testing.T) {
	old := openDevice
	t.Cleanup(func() { openDevice = old })
	openDevice = func(string) (*os.File, error) {
		return nil, &fs.PathError{Op: "open", Path: "/dev/input/event3", Err: fs.ErrPermission}
	}
	_, err := Open([]Device{{Path: "/dev/input/event3", Keyboard: true}}, func(Event) {})
	if !errors.Is(err, ErrNoAccess) {
		t.Fatalf("err = %v, want ErrNoAccess", err)
	}
	if err := Access([]Device{{Path: "/dev/input/event3", Keyboard: true}}); !errors.Is(err, ErrNoAccess) {
		t.Fatalf("Access = %v", err)
	}
}

func TestOpenNoKeyboard(t *testing.T) {
	if _, err := Open([]Device{{Path: "x", Pointer: true}}, func(Event) {}); !errors.Is(err, ErrNoKeyboard) {
		t.Fatalf("err = %v, want ErrNoKeyboard", err)
	}
	if err := Access(nil); !errors.Is(err, ErrNoKeyboard) {
		t.Fatalf("Access(nil) = %v", err)
	}
}
