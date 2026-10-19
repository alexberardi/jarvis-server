// Package control is the running agent's local control socket (installers plan item 1.9): how
// a second launch, the start-only shortcut, the jarvis-agent://start link and uninstall reach
// the agent that is already running. It is also the agent's single-instance lock: a second
// `run` finds the socket live and stops.
//
// One JSON line per connection, one answer: {"cmd": "show-running"} → {"ok": true}, with an
// "answer" for the commands that report something (computer-status). The commands are a closed
// set and take no arguments. The socket lives in the browser socket's
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
	// Quit stops the agent (stop, the tray menu, uninstall, a reinstall replacing it).
	Quit = "quit"
	// Ping only checks the agent is there (start at login: no pill).
	Ping = "ping"
	// VoiceOn and VoiceOff turn listening for the wake word on or off, as the status icon's
	// toggle does (the setup page's switch).
	VoiceOn  = "voice-on"
	VoiceOff = "voice-off"
	// Health answers ok only once the agent is connected to jarvisd (MQTT): the self-update's
	// check that a new version came up (01-no-cli-plan 1.7). The answer names the version.
	Health = "health"
	// ComputerStop stops what Jarvis is doing with the computer (a computer task, a
	// recording): the tray's "Stop Jarvis", `jarvis-agent computer stop`. The answer says
	// whether anything was stopped.
	ComputerStop = "computer-stop"
	// ComputerStatus answers what Jarvis is doing with the computer: "idle", or the activity
	// and its intent ("controlling: Ordering the blue mug").
	ComputerStatus = "computer-status"
	// ComputerOn and ComputerOff turn computer control on or off, as the status icon's
	// "Computer control" does (COMPUTER-CONTROL.md C11). Off stops a task going on.
	ComputerOn  = "computer-on"
	ComputerOff = "computer-off"
)

// Commands is every command the agent accepts.
var Commands = []string{ShowRunning, Quit, Ping, VoiceOn, VoiceOff, Health, ComputerStop, ComputerStatus, ComputerOn, ComputerOff}

// IOTimeout bounds a request and its answer.
const IOTimeout = 3 * time.Second

type request struct {
	Cmd string `json:"cmd"`
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	// Answer is what a command reports (computer-status).
	Answer string `json:"answer,omitempty"`
	// Version is the answering agent's version (every answer, from agents that know it).
	Version string `json:"version,omitempty"`
}

// Reply is an agent's answer.
type Reply struct {
	Version string // "" from an agent that predates it
	Answer  string // what the command reported, if anything
}

// Handler runs a command (one of Commands).
type Handler func(cmd string) error

// AnswerHandler runs a command and returns what it reports.
type AnswerHandler func(cmd string) (string, error)

// Serve answers connections on ln until ctx ends or ln is closed.
func Serve(ctx context.Context, ln net.Listener, h Handler) error {
	return ServeVersion(ctx, ln, h, "")
}

// ServeVersion is Serve, naming version in every answer.
func ServeVersion(ctx context.Context, ln net.Listener, h Handler, version string) error {
	return ServeAnswers(ctx, ln, func(cmd string) (string, error) { return "", h(cmd) }, version)
}

// ServeAnswers is ServeVersion with a handler that reports something.
func ServeAnswers(ctx context.Context, ln net.Listener, h AnswerHandler, version string) error {
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
		go handle(c, h, version)
	}
}

func handle(c net.Conn, h AnswerHandler, version string) {
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
		answer, err := h(req.Cmd)
		if err != nil {
			resp = response{Error: err.Error()}
		} else {
			resp.Answer = answer
		}
	}
	resp.Version = version
	b, _ := json.Marshal(resp)
	_, _ = c.Write(append(b, '\n'))
}

// Dialer connects to the control socket (browser.Dial on the real path; a pipe in tests).
type Dialer func() (net.Conn, error)

// ErrNotRunning: nothing answers on the socket.
var ErrNotRunning = errors.New("the agent isn't running")

// Send asks the running agent to do cmd. ErrNotRunning when no agent answers.
func Send(dial Dialer, cmd string) error {
	_, err := Ask(dial, cmd)
	return err
}

// Ask is Send, returning the answer.
func Ask(dial Dialer, cmd string) (Reply, error) {
	c, err := dial()
	if err != nil {
		return Reply{}, ErrNotRunning
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(IOTimeout))
	b, _ := json.Marshal(request{Cmd: cmd})
	if _, err := c.Write(append(b, '\n')); err != nil {
		return Reply{}, err
	}
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return Reply{}, fmt.Errorf("the agent didn't answer: %w", err)
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return Reply{}, err
	}
	r := Reply{Version: resp.Version, Answer: resp.Answer}
	if !resp.OK {
		return r, errors.New(resp.Error)
	}
	return r, nil
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
