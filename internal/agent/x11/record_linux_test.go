//go:build linux

package x11

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

func xauthEntry(family uint16, addr, display, name string, data []byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.BigEndian, family)
	for _, f := range [][]byte{[]byte(addr), []byte(display), []byte(name), data} {
		binary.Write(&b, binary.BigEndian, uint16(len(f)))
		b.Write(f)
	}
	return b.Bytes()
}

// An entry with another protocol (XDM-AUTHORIZATION-1, say) is skipped, not the end of the search.
func TestReadXauthSkipsUnsupported(t *testing.T) {
	cookie := bytes.Repeat([]byte{7}, 16)
	var file []byte
	file = append(file, xauthEntry(65535, "", "3", "XDM-AUTHORIZATION-1", make([]byte, 16))...)
	file = append(file, xauthEntry(256, "otherhost", "3", "MIT-MAGIC-COOKIE-1", make([]byte, 16))...)
	file = append(file, xauthEntry(256, "box", "3", "MIT-MAGIC-COOKIE-1", cookie)...)
	name := filepath.Join(t.TempDir(), "Xauthority")
	if err := os.WriteFile(name, file, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XAUTHORITY", name)

	got, err := readXauth("box", "3")
	if err != nil {
		t.Fatal(err)
	}
	if got.name != "MIT-MAGIC-COOKIE-1" || !bytes.Equal(got.data, cookie) {
		t.Fatalf("got %q %x, want the cookie entry", got.name, got.data)
	}
	if _, err := readXauth("box", "4"); err == nil {
		t.Fatal("display 4 has no entry: want an error")
	}
}
