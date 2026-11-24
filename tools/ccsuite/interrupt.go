package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/control"
)

// Interrupt is the user stepping in while the task runs: a stop (the tray's "Stop Jarvis", the
// agent's control socket), the user's hand on the mouse (user_mouse: the pointer moved 60 px from
// another process) or on the keyboard (user_input: Shift, as the suite's UserInput), once the task's AfterActions-th action has begun. Actions
// are seen in the agent's status, which shows each action's intent as it starts: the model sends
// a whole note as one batch, logged once, so the log can't fire mid-batch.
type Interrupt struct {
	Do           string `json:"do"`                      // stop | user_mouse | user_input
	AfterActions int    `json:"after_actions,omitempty"` // default 1
}

// Watch is what the suite saw of the agent while the case ran: its activity (the indicator's
// state, at each change), the actions it took and when the interrupt fired.
type Watch struct {
	Activity    []Sample    `json:"activity"`
	Actions     []time.Time `json:"actions"` // successful computer_act calls, by their log time
	Interrupt   string      `json:"interrupt,omitempty"`
	Interrupted time.Time   `json:"interrupted,omitempty"`
	After       int         `json:"after,omitempty"` // the action whose beginning fired the interrupt
	FireErr     string      `json:"fire_error,omitempty"`
}

// Sample is the agent's computer-status at a time: "idle", "controlling: <intent>", "paused: …".
type Sample struct {
	Time   time.Time `json:"time"`
	Status string    `json:"status"`
}

// kind is the sample's activity: idle, controlling, paused or recording.
func (s Sample) kind() string {
	k, _, _ := strings.Cut(s.Status, ":")
	return strings.TrimSpace(k)
}

// watcher is the watch loop's world: the agent's status, the actions logged since the last call,
// and the interrupt itself.
type watcher struct {
	status func() (string, error)
	acts   func() []time.Time
	fire   func(ctx context.Context) error
	clk    clock
	every  time.Duration
}

// run samples until ctx ends, firing in (nil: never) once its actions have landed; the watch
// ends with one last sample, so it shows how the indicator was left.
func (w watcher) run(ctx context.Context, in *Interrupt) *Watch {
	out := &Watch{}
	sample := func() {
		st, err := w.status()
		if err != nil {
			st = "error: " + err.Error()
		}
		if n := len(out.Activity); n == 0 || out.Activity[n-1].Status != st {
			out.Activity = append(out.Activity, Sample{Time: w.clk.now(), Status: st})
		}
	}
	after := 1
	if in != nil && in.AfterActions > 0 {
		after = in.AfterActions
	}
	begun := 0 // actions begun: each new intent while controlling, after the task's first status
	for {
		n := len(out.Activity)
		sample()
		if len(out.Activity) > n && n > 0 && out.Activity[n].kind() == "controlling" && out.Activity[n-1].kind() == "controlling" {
			begun++
		}
		out.Actions = append(out.Actions, w.acts()...)
		if in != nil && out.Interrupted.IsZero() && begun >= after {
			out.Interrupt, out.Interrupted, out.After = in.Do, w.clk.now(), after
			if err := w.fire(ctx); err != nil {
				out.FireErr = err.Error()
			}
			continue // sample straight away: how fast it paused or stopped
		}
		if !w.clk.wait(ctx, w.every) {
			sample()
			out.Actions = append(out.Actions, w.acts()...)
			return out
		}
	}
}

// actRE is a successful computer_act in the agent's log (slog text: time=… msg=tool_call
// command=computer_act … success=true).
var actRE = regexp.MustCompile(`^time=(\S+) .*msg=tool_call command=computer_act .*success=true`)

// logTail reads the lines appended to path since the last call, from offset.
type logTail struct {
	path   string
	offset int64
	part   string
}

// acts is the successful computer_act calls in the lines appended since the last call.
func (t *logTail) acts() []time.Time {
	f, err := os.Open(t.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return nil
	}
	b, _ := io.ReadAll(f)
	t.offset += int64(len(b))
	text := t.part + string(b)
	cut := strings.LastIndexByte(text, '\n')
	t.part = text[cut+1:]
	return actTimes(text[:cut+1])
}

func actTimes(text string) []time.Time {
	var out []time.Time
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if m := actRE.FindStringSubmatch(sc.Text()); m != nil {
			if at, err := time.Parse(time.RFC3339Nano, m[1]); err == nil {
				out = append(out, at)
			}
		}
	}
	return out
}

// Watch samples the agent while the case runs, interrupting it as in says; stop ends the watch
// and returns what it saw. Without a desktop session (no agent) it sees nothing.
func (e *Env) Watch(ctx context.Context, in *Interrupt) (stop func() *Watch) {
	d := e.desk
	if d == nil {
		return func() *Watch { return nil }
	}
	ctl := filepath.Join(d.rt, "control.sock")
	ask := func(cmd string) (string, error) {
		r, err := control.Ask(func() (net.Conn, error) { return net.Dial("unix", ctl) }, cmd)
		return r.Answer, err
	}
	tail := &logTail{path: e.path("logs", "agent.log"), offset: fileSize(e.path("logs", "agent.log"))}
	w := watcher{
		status: func() (string, error) { return ask(control.ComputerStatus) },
		acts:   tail.acts,
		fire: func(ctx context.Context) error {
			switch in.Do {
			case "stop":
				a, err := ask(control.ComputerStop)
				if err == nil && a != "stopped" {
					err = fmt.Errorf("the agent answered %q", a)
				}
				return err
			case "user_input":
				return e.pressKey(ctx)
			case "user_mouse":
				return e.runSelf(ctx, userMouseCmd)
			}
			return errors.New("no interrupt " + in.Do)
		},
		clk: realClock, every: 50 * time.Millisecond, // a batch's actions are ~250 ms apart
	}
	wctx, cancel := context.WithCancel(ctx)
	var (
		once sync.Once
		out  *Watch
		done = make(chan struct{})
	)
	go func() {
		defer close(done)
		out = w.run(wctx, in)
	}()
	return func() *Watch {
		once.Do(func() { cancel(); <-done })
		return out
	}
}

// pressKey is the user pressing Shift, from a process of its own in the session (the agent's
// guard tells its own input from anyone else's).
func (e *Env) pressKey(ctx context.Context) error { return e.runSelf(ctx, userInputCmd) }

// runSelf runs one of ccsuite's own input subcommands in the desktop session.
func (e *Env) runSelf(ctx context.Context, sub string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, self, sub)
	cmd.Env = e.desk.env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%w: %s", err, lastOutputLine(out))
	}
	return nil
}
