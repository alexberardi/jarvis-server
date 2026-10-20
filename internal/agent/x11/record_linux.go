//go:build linux

package x11

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/record"
	"github.com/jezek/xgb/xproto"
)

// recordStopWait is how long stop waits for the server's EndOfData before closing the data
// connection anyway.
const recordStopWait = 2 * time.Second

// Record starts recording every client's key presses and releases and button presses, and
// calls send with each (on one goroutine of its own) until stop returns. The keymap is the
// keyboard mapping when it started.
func (d *Display) Record(send func(RecordedInput)) (Keymap, func(), error) {
	if ok, why := d.Session(); !ok {
		return Keymap{}, nil, errors.New(why)
	}
	quietOnce.Do(func() { xgb.Logger = log.New(io.Discard, "", 0) })
	ctrl, err := xgb.NewConnDisplay(d.getenv("DISPLAY"))
	if err != nil {
		return Keymap{}, nil, fmt.Errorf("can't reach the X server: %w", err)
	}
	fail := func(err error) (Keymap, func(), error) { ctrl.Close(); return Keymap{}, nil, err }
	if record.Init(ctrl) != nil {
		return fail(ErrNoRecord)
	}
	x := &xconn{c: ctrl, setup: xproto.Setup(ctrl)}
	lo, _ := x.KeycodeRange()
	per, syms, err := x.Keymap()
	if err != nil {
		return fail(fmt.Errorf("keyboard mapping: %w", err))
	}
	id, err := ctrl.NewId()
	if err != nil {
		return fail(err)
	}
	rc := record.Context(id)
	ranges := []record.Range{{DeviceEvents: record.Range8{First: KeyPress, Last: ButtonPress}}}
	if err := record.CreateContextChecked(ctrl, rc, 0, 1, 1, []record.ClientSpec{record.CsAllClients}, ranges).Check(); err != nil {
		return fail(fmt.Errorf("RECORD: %w", err))
	}
	ctrl.ExtLock.RLock()
	opcode := ctrl.Extensions["RECORD"]
	ctrl.ExtLock.RUnlock()
	data, err := dialData(d.getenv("DISPLAY"))
	if err == nil {
		_, err = data.Write(enableContext(opcode, id))
		if err != nil {
			data.Close()
		}
	}
	if err != nil {
		_ = record.FreeContextChecked(ctrl, rc).Check()
		return fail(fmt.Errorf("RECORD data connection: %w", err))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = readRecorded(data, send)
	}()
	stop := func() {
		_ = record.DisableContextChecked(ctrl, rc).Check() // the server ends the stream
		select {
		case <-done:
		case <-time.After(recordStopWait):
		}
		data.Close()
		<-done
		_ = record.FreeContextChecked(ctrl, rc).Check()
		ctrl.Close()
	}
	return Keymap{Min: lo, Per: per, Syms: syms}, stop, nil
}

// dialData opens a raw connection to display and does the setup handshake with its
// MIT-MAGIC-COOKIE-1 from the Xauthority file, the way xgb.NewConnDisplay does.
func dialData(display string) (net.Conn, error) {
	host, num, socket, err := parseDisplay(display)
	if err != nil {
		return nil, err
	}
	var c net.Conn
	switch {
	case socket != "":
		c, err = net.Dial("unix", socket+":"+num)
	case host != "" && host != "unix":
		proto, h := "tcp", host
		if i := strings.LastIndex(host, "/"); i >= 0 {
			proto, h = host[:i], host[i+1:]
		}
		n, _ := strconv.Atoi(num)
		c, err = net.Dial(proto, net.JoinHostPort(h, strconv.Itoa(6000+n)))
	default:
		c, err = net.Dial("unix", "/tmp/.X11-unix/X"+num)
	}
	if err != nil {
		return nil, err
	}
	auth, _ := readXauth(hostOnly(host), num)
	if err := handshake(c, auth); err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// parseDisplay splits [protocol/]host:display[.screen] or /socket:display.
func parseDisplay(display string) (host, num, socket string, err error) {
	i := strings.LastIndex(display, ":")
	if i < 0 {
		return "", "", "", fmt.Errorf("bad display %q", display)
	}
	num = display[i+1:]
	if j := strings.Index(num, "."); j >= 0 {
		num = num[:j]
	}
	if n, err := strconv.Atoi(num); err != nil || n < 0 {
		return "", "", "", fmt.Errorf("bad display %q", display)
	}
	if strings.HasPrefix(display, "/") {
		return "", num, display[:i], nil
	}
	return display[:i], num, "", nil
}

func hostOnly(host string) string {
	if i := strings.LastIndex(host, "/"); i >= 0 {
		return host[i+1:]
	}
	return host
}

// readXauth finds display's entry in the Xauthority file: a FamilyWild one, or a FamilyLocal
// one for this host, for this display or any.
func readXauth(host, display string) (xauth, error) {
	const familyLocal, familyWild = 256, 65535
	if host == "" || host == "localhost" || host == "unix" {
		h, err := os.Hostname()
		if err != nil {
			return xauth{}, err
		}
		host = h
	}
	name := os.Getenv("XAUTHORITY")
	if name == "" {
		home := os.Getenv("HOME")
		if home == "" {
			return xauth{}, errors.New("no Xauthority file")
		}
		name = home + "/.Xauthority"
	}
	f, err := os.Open(name)
	if err != nil {
		return xauth{}, err
	}
	defer f.Close()
	field := func() ([]byte, error) {
		var n uint16
		if err := binary.Read(f, binary.BigEndian, &n); err != nil {
			return nil, err
		}
		b := make([]byte, n)
		_, err := io.ReadFull(f, b)
		return b, err
	}
	for {
		var family uint16
		if err := binary.Read(f, binary.BigEndian, &family); err != nil {
			return xauth{}, err
		}
		var parts [4][]byte
		for i := range parts {
			if parts[i], err = field(); err != nil {
				return xauth{}, err
			}
		}
		addr, disp := string(parts[0]), string(parts[1])
		if (family == familyWild || (family == familyLocal && addr == host)) && (disp == "" || disp == display) {
			if string(parts[2]) != "MIT-MAGIC-COOKIE-1" || len(parts[3]) != 16 {
				return xauth{}, fmt.Errorf("unsupported X authorization %q", parts[2])
			}
			return xauth{name: string(parts[2]), data: parts[3]}, nil
		}
	}
}
