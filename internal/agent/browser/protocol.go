// Package browser lets the agent control the user's own browser through the Jarvis Browser
// Control extension (Firefox, and Chrome/Edge/Brave/Arc from one Manifest V3 codebase).
//
//	extension ⇄ (native messaging: stdin/stdout, 4-byte length + JSON) ⇄ jarvis-agent browser-host
//	browser-host ⇄ (Unix socket, user-only, JSON lines) ⇄ the running agent's Hub ⇄ Tools
//
// The browser starts the native host (the same jarvis-agent binary, recognised by its argv)
// from a manifest `jarvis-agent browser install` registered. The host is a dumb relay: it
// connects to the agent's socket, replays the extension's hello, and copies messages both ways,
// reconnecting while the agent restarts. The agent's Hub tracks every connected browser,
// targets the one the user focused last, and turns tool calls into commands with timeouts.
// Tools are offered only while a browser is connected.
package browser

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// HostName is the native-messaging host name the extension connects to.
const HostName = "io.jarvis.agent.browser"

// Protocol is the extension⇄agent message protocol version (hello.protocol).
const Protocol = 1

// FirefoxExtensionID is the Gecko id in the extension's manifest (permanent once AMO signs it).
const FirefoxExtensionID = "browser@jarvis.agent"

// ChromeExtensionID is the id Chromium browsers give the unpacked extension: derived from the
// public "key" in its manifest, so it is the same on every machine. A Web Store listing would
// get its own id, which then joins this one in AllowedOrigins.
const ChromeExtensionID = "hdjdhmmlggbjjdokpofkghjnjnmjfhmk"

// AllowedOrigins are the Chromium extension origins the host manifest admits.
var AllowedOrigins = []string{"chrome-extension://" + ChromeExtensionID + "/"}

// Limits. Chrome refuses host→extension messages over 1 MiB; the agent's commands are tiny.
// Extension→host messages are bounded by what the extension sends (page text is capped there
// and again here), so 8 MiB is generous and still stops a runaway allocation.
const (
	maxToExtension   = 1 << 20
	maxFromExtension = 8 << 20
)

// Message is one protocol message, in either direction. Type is "hello", "result", "event"
// (extension → agent) or "command" (agent → extension); "host" and "status" open an IPC
// connection (see Hub).
type Message struct {
	Type string `json:"type"`

	// command / result
	ID     int64          `json:"id,omitempty"`
	Cmd    string         `json:"cmd,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	OK     bool           `json:"ok,omitempty"`
	Result map[string]any `json:"result,omitempty"`
	Error  string         `json:"error,omitempty"`
	Code   string         `json:"code,omitempty"`

	// hello
	Protocol         int    `json:"protocol,omitempty"`
	ExtensionVersion string `json:"extension_version,omitempty"`
	Browser          string `json:"browser,omitempty"`
	BrowserVersion   string `json:"browser_version,omitempty"`

	// hello / event "focus"
	Event   string `json:"event,omitempty"`
	Focused bool   `json:"focused,omitempty"`

	// host (first line of a host's IPC connection)
	Origin string `json:"origin,omitempty"`
	PID    int    `json:"pid,omitempty"`
}

// ErrTooLarge is a native message over the size limit.
var ErrTooLarge = errors.New("native message too large")

// ReadNative reads one native-messaging frame: a 4-byte length in native byte order, then that
// many bytes of UTF-8 JSON. io.EOF means the browser closed the pipe.
func ReadNative(r io.Reader, max int) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, io.EOF
		}
		return nil, err
	}
	n := binary.NativeEndian.Uint32(hdr[:])
	if int64(n) > int64(max) {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooLarge, n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("short native message: %w", err)
	}
	return buf, nil
}

// WriteNative writes one native-messaging frame.
func WriteNative(w io.Writer, msg []byte, max int) error {
	if len(msg) > max {
		return fmt.Errorf("%w: %d bytes", ErrTooLarge, len(msg))
	}
	buf := make([]byte, 4+len(msg))
	binary.NativeEndian.PutUint32(buf, uint32(len(msg)))
	copy(buf[4:], msg)
	_, err := w.Write(buf)
	return err
}

// HostInvocation reports whether argv (without the program name) is a browser starting this
// binary as its native-messaging host, and the caller's origin. Chromium browsers pass the
// origin ("chrome-extension://ID/", plus --parent-window=N on Windows); Firefox passes the
// host manifest's path and the extension id. The manifest's "path" can't carry arguments, so
// this is how one binary serves as both agent and host.
func HostInvocation(args []string) (origin string, ok bool) {
	if len(args) == 0 {
		return "", false
	}
	if len(args[0]) > len("chrome-extension://") && args[0][:len("chrome-extension://")] == "chrome-extension://" {
		return args[0], true
	}
	if len(args) >= 2 && args[1] == FirefoxExtensionID {
		return args[1], true
	}
	return "", false
}

func marshal(m Message) []byte {
	b, _ := json.Marshal(m)
	return b
}
