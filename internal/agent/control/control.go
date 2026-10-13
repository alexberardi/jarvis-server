// Package control is the running agent's local control socket (installers plan item 1.9): how
// a second launch, the start-only shortcut, the jarvis-agent://start link and uninstall reach
// the agent that is already running. It is also the agent's single-instance lock: a second
// `run` finds the socket live and stops.
//
// One JSON line per connection, one answer: {"cmd": "show-running"} → {"ok": true}. The
// commands are a closed set and take no arguments. The socket lives in the browser socket's
// user-only directory, is 0600, and peers must be this user (browser.Listen).
package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"time"
)

// The commands.
const (
	// ShowRunning flashes the island's "Jarvis is running" pill.
	ShowRunning = "show-running"
	// Quit stops the agent (uninstall, a reinstall replacing it).
	Quit = "quit"
	// Ping only checks the agent is there (start at login: no pill).
	Ping = "ping"
)

// Commands is every command the agent accepts.
var Commands = []string{ShowRunning, Quit, Ping}

// IOTimeout bounds a request and its answer.
const IOTimeout = 3 * time.Second

type request struct {
	Cmd string `json:"cmd"`
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Handler runs a command (one of Commands).
type Handler func(cmd string) error

// Serve answers connections on ln until ctx ends or ln is closed.
func Serve(ctx context.Context, ln net.Listener, h Handler) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go handle(c, h)
	}
}

func handle(c net.Conn, h Handler) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(IOTimeout))
	line, err := bufio.NewReader(c).ReadBytes('\n')
	var req request
	resp := response{OK: true}
	switch {
	case err != nil:
		return
	case json.Unmarshal(line, &req) != nil:
		resp = response{Error: "bad request"}
	case !slices.Contains(Commands, req.Cmd):
		resp = response{Error: fmt.Sprintf("unknown command %q", req.Cmd)}
	default:
		if err := h(req.Cmd); err != nil {
			resp = response{Error: err.Error()}
		}
	}
	b, _ := json.Marshal(resp)
	_, _ = c.Write(append(b, '\n'))
}

// Dialer connects to the control socket (browser.Dial on the real path; a pipe in tests).
type Dialer func() (net.Conn, error)

// ErrNotRunning: nothing answers on the socket.
var ErrNotRunning = errors.New("the agent isn't running")

// Send asks the running agent to do cmd. ErrNotRunning when no agent answers.
func Send(dial Dialer, cmd string) error {
	c, err := dial()
	if err != nil {
		return ErrNotRunning
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(IOTimeout))
	b, _ := json.Marshal(request{Cmd: cmd})
	if _, err := c.Write(append(b, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("the agent didn't answer: %w", err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

// WaitGone waits until no agent answers (after Quit), checking up to attempts times.
func WaitGone(ctx context.Context, dial Dialer, attempts int, pause func()) bool {
	for n := 0; n < attempts && ctx.Err() == nil; n++ {
		if errors.Is(Send(dial, Ping), ErrNotRunning) {
			return true
		}
		pause()
	}
	return false
}

// WaitSend retries Send while the agent isn't up yet: after starting one, until it opens its
// socket. pause is called between attempts (time.Sleep; a no-op in tests).
func WaitSend(ctx context.Context, dial Dialer, cmd string, attempts int, pause func()) error {
	for n := 0; ; n++ {
		err := Send(dial, cmd)
		if !errors.Is(err, ErrNotRunning) || n+1 >= attempts || ctx.Err() != nil {
			return err
		}
		pause()
	}
}
