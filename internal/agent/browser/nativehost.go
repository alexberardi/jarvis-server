package browser

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// HostConfig configures the native-messaging host relay.
type HostConfig struct {
	Origin string                   // the caller the browser named in argv (HostInvocation)
	Dial   func() (net.Conn, error) // connects to the agent (Dial(SocketPath()))
	Retry  time.Duration            // between connection attempts while the agent is down; default 2 s
	Log    *slog.Logger             // stderr: the browser shows it in its console/log
}

// RunHost relays between the extension (native messaging on in/out) and the agent (JSON lines
// on a socket) until the browser closes in (io.EOF, a normal exit) or ctx ends. While the
// agent is down it keeps retrying, and it replays the extension's last hello to every new
// agent connection, so the agent may restart without the browser noticing. Nothing but
// native-messaging frames is ever written to out.
func RunHost(ctx context.Context, in io.Reader, out io.Writer, cfg HostConfig) error {
	if cfg.Retry <= 0 {
		cfg.Retry = 2 * time.Second
	}
	log := cfg.Log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	fromExt := make(chan []byte)
	extErr := make(chan error, 1)
	go func() {
		for {
			b, err := ReadNative(in, maxFromExtension)
			if err != nil {
				extErr <- err
				return
			}
			var buf bytes.Buffer
			if json.Compact(&buf, b) != nil {
				log.Warn("browser-host: dropping a malformed message from the extension")
				continue
			}
			select {
			case fromExt <- buf.Bytes():
			case <-ctx.Done():
				return
			}
		}
	}()

	var outMu sync.Mutex
	toExt := func(line []byte) {
		outMu.Lock()
		defer outMu.Unlock()
		if err := WriteNative(out, line, maxToExtension); err != nil {
			log.Warn("browser-host: dropping a message to the extension", "err", err)
		}
	}

	var hello []byte
	remember := func(b []byte) {
		var m struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(b, &m) == nil && m.Type == "hello" {
			hello = b
		}
	}
	intro := marshal(Message{Type: "host", Origin: cfg.Origin, PID: os.Getpid()})

	for {
		conn, err := cfg.Dial()
		if err != nil {
			// Agent not running (yet): wait, keeping the extension's hello for later.
			t := time.NewTimer(cfg.Retry)
		wait:
			for {
				select {
				case b := <-fromExt:
					remember(b)
				case err := <-extErr:
					t.Stop()
					return endOf(err)
				case <-ctx.Done():
					t.Stop()
					return nil
				case <-t.C:
					break wait
				}
			}
			continue
		}
		log.Debug("browser-host: connected to the agent")
		agentDone := make(chan struct{})
		go func() {
			defer close(agentDone)
			r := bufio.NewReaderSize(conn, 64<<10)
			for {
				line, err := readLine(r, maxToExtension)
				if errors.Is(err, ErrTooLarge) {
					log.Warn("browser-host: dropping an oversize message from the agent")
					continue
				}
				if err != nil {
					return
				}
				toExt(line)
			}
		}()
		write := func(b []byte) bool {
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			_, err := conn.Write(append(b, '\n'))
			return err == nil
		}
		ok := write(intro) && (hello == nil || write(hello))
	relay:
		for ok {
			select {
			case b := <-fromExt:
				remember(b)
				if !write(b) {
					break relay
				}
			case <-agentDone:
				break relay
			case err := <-extErr:
				_ = conn.Close()
				<-agentDone
				return endOf(err)
			case <-ctx.Done():
				_ = conn.Close()
				<-agentDone
				return nil
			}
		}
		_ = conn.Close()
		<-agentDone
		log.Debug("browser-host: agent connection lost; retrying")
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.Retry / 4):
		}
	}
}

// readLine reads one '\n'-terminated line (without it). A line over max is consumed and
// reported as ErrTooLarge, so the stream stays usable.
func readLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			if len(line)+len(chunk) > max+1 {
				tooLong, line = true, nil
			} else {
				line = append(line, chunk...)
			}
		}
		switch {
		case err == nil:
			if tooLong {
				return nil, ErrTooLarge
			}
			return line[:len(line)-1], nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, err
		}
	}
}

func endOf(err error) error {
	if err == io.EOF {
		return nil // the browser closed the port
	}
	return err
}
