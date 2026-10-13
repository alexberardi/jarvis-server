package browser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestNativeFraming(t *testing.T) {
	var buf bytes.Buffer
	for _, m := range []string{`{"type":"hello"}`, `{}`, `{"text":"ünïcode ✓"}`} {
		if err := WriteNative(&buf, []byte(m), maxToExtension); err != nil {
			t.Fatal(err)
		}
	}
	// The header is the length in native byte order (what browsers write).
	if n := binary.NativeEndian.Uint32(buf.Bytes()[:4]); n != uint32(len(`{"type":"hello"}`)) {
		t.Fatalf("header %d", n)
	}
	for _, want := range []string{`{"type":"hello"}`, `{}`, `{"text":"ünïcode ✓"}`} {
		got, err := ReadNative(&buf, maxFromExtension)
		if err != nil || string(got) != want {
			t.Fatalf("got %q, %v; want %q", got, err, want)
		}
	}
	if _, err := ReadNative(&buf, maxFromExtension); err != io.EOF {
		t.Fatalf("at end: %v, want io.EOF", err)
	}
}

func TestNativeFramingLimits(t *testing.T) {
	if err := WriteNative(io.Discard, make([]byte, 11), 10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("write over limit: %v", err)
	}
	var hdr [4]byte
	binary.NativeEndian.PutUint32(hdr[:], 1<<31)
	// A huge length is refused before allocating.
	if _, err := ReadNative(bytes.NewReader(hdr[:]), maxFromExtension); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("read over limit: %v", err)
	}
	// A partial header is the browser going away.
	if _, err := ReadNative(bytes.NewReader([]byte{1, 0}), maxFromExtension); err != io.EOF {
		t.Fatalf("partial header: %v", err)
	}
	binary.NativeEndian.PutUint32(hdr[:], 10)
	if _, err := ReadNative(bytes.NewReader(append(hdr[:], 'x')), maxFromExtension); err == nil || err == io.EOF {
		t.Fatalf("short body: %v", err)
	}
}

func TestHostInvocation(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		origin string
		ok     bool
	}{
		{[]string{"chrome-extension://" + ChromeDevExtensionID + "/"}, "chrome-extension://" + ChromeDevExtensionID + "/", true},
		{[]string{"chrome-extension://" + ChromeStoreExtensionID + "/"}, "chrome-extension://" + ChromeStoreExtensionID + "/", true},
		{[]string{"chrome-extension://abc/", "--parent-window=0"}, "chrome-extension://abc/", true}, // Windows
		{[]string{"/home/u/.mozilla/native-messaging-hosts/io.jarvis.agent.browser.json", FirefoxExtensionID}, FirefoxExtensionID, true},
		{[]string{"run"}, "", false},
		{[]string{"try", "browser_list_tabs"}, "", false},
		{[]string{"x.json", "other@extension"}, "", false},
		{[]string{"chrome-extension://"}, "", false},
		{nil, "", false},
	} {
		origin, ok := HostInvocation(tc.args)
		if origin != tc.origin || ok != tc.ok {
			t.Errorf("HostInvocation(%q) = %q, %v; want %q, %v", tc.args, origin, ok, tc.origin, tc.ok)
		}
	}
}
