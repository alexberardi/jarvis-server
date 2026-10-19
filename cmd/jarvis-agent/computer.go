package main

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

// computerCmd is `jarvis-agent computer stop|status|on|off|record start [--desktop]|record stop`:
// stop what Jarvis is doing with this computer (a computer task, a recording), as the status
// icon's "Stop Jarvis" does, say what it is doing, turn computer control on or off, as its
// "Computer control" does, or record what the user does in the browser, as its "Watch me" does
// (or on the desktop, with --desktop). All ask
// the running agent over its control socket. (`computer snapshot` and `computer record
// --browser` are computerSnapshotCmd and computerRecordCmd, which main dispatches before this.)
func computerCmd(args []string, w io.Writer, dial control.Dialer) error {
	cmds := map[string]string{"stop": control.ComputerStop, "status": control.ComputerStatus,
		"on": control.ComputerOn, "off": control.ComputerOff,
		"record start": control.ComputerRecordStart, "record stop": control.ComputerRecordStop,
		"record start --desktop": control.ComputerRecordDesktop}
	cmd := ""
	if len(args) >= 1 && len(args) <= 3 {
		cmd = cmds[strings.Join(args, " ")]
	}
	if cmd == "" {
		return errors.New("usage: jarvis-agent computer stop|status|on|off|record start [--desktop]|record stop|snapshot URL|record --browser")
	}
	r, err := control.Ask(dial, cmd)
	if err != nil {
		return err
	}
	switch {
	case cmd == control.ComputerRecordStart:
		fmt.Fprintf(w, "Recording (%s): do the task in the browser, then run `jarvis-agent computer record stop` or choose Stop recording in the menu.\n", r.Answer)
	case cmd == control.ComputerRecordDesktop:
		fmt.Fprintf(w, "Recording the desktop (%s): do the task, then run `jarvis-agent computer record stop` or choose Stop recording in the menu. "+
			"What you type into password fields isn't kept.\n", r.Answer)
	case cmd == control.ComputerRecordStop:
		id, n, _ := strings.Cut(r.Answer, " ")
		fmt.Fprintf(w, "Stopped %s: %s events, sent to Jarvis.\n", id, n)
	case cmd == control.ComputerOn:
		fmt.Fprintln(w, "Computer control is on: Jarvis may use this computer.")
	case cmd == control.ComputerOff:
		fmt.Fprintln(w, "Computer control is off: Jarvis won't use this computer.")
	case cmd == control.ComputerStop && r.Answer == "stopped":
		fmt.Fprintln(w, "Stopped.")
	case cmd == control.ComputerStop:
		fmt.Fprintln(w, "Jarvis wasn't using this computer.")
	case r.Answer == "idle" || r.Answer == "":
		fmt.Fprintln(w, "Jarvis isn't using this computer.")
	default:
		// "controlling: Ordering the blue mug" (computer.Activity's String).
		kind, text, _ := strings.Cut(r.Answer, ": ")
		line := map[string]string{string(computer.Controlling): "Jarvis is controlling this computer",
			string(computer.Paused): "Jarvis is paused", string(computer.Recording): "Jarvis is recording"}[kind]
		if line == "" {
			line = r.Answer
		} else if text != "" {
			line += ": " + text
		}
		fmt.Fprintln(w, line)
	}
	return nil
}

// --- recording (plan P3 piece 29) ---

// newRecorder is the agent's recorder: traces under dir (<agent dir>/recordings), the browser
// extension's events (a browser toolset in tools), the desktop where this OS can record it
// (macOS, with keyframes from the screen toolset in tools), and a trace stopped here (the menu,
// the CLI, voice "stop", its 30 minutes) uploaded to jarvisd through cc (nil until connected:
// then it is kept here only).
func newRecorder(dir string, sw *computer.Switch, activity *computer.Activity, tools node.Multi, cc func() *node.CC, log *slog.Logger) *computer.Recorder {
	sources := map[string]computer.Source{}
	var shots screen.Capturer
	for _, t := range tools {
		switch t := t.(type) {
		case *browser.Tools:
			sources[computer.ModeBrowser] = browserRecording{t}
		case *screen.Tools:
			shots = t.C
		}
	}
	if src := computer.NewDesktopSource(dir, keyframer(shots), log); src != nil {
		sources[computer.ModeDesktop] = src
	}
	return &computer.Recorder{Dir: dir, Switch: sw, Activity: activity, Sources: sources, Log: log,
		Stopped: func(tr computer.Trace) { go uploadRecording(cc(), tr, log) }}
}

// keyframer is a desktop recording's keyframe: the screen as a JPEG (nil without a capturer).
func keyframer(c screen.Capturer) func(context.Context) ([]byte, error) {
	if c == nil {
		return nil
	}
	return func(ctx context.Context) ([]byte, error) {
		if ok, why := c.Available(ctx); !ok {
			return nil, errors.New(why)
		}
		shot, err := c.Capture(ctx, screen.ScopeScreen, "")
		if err != nil {
			return nil, err
		}
		b, _, _, err := screen.Encode(shot.Image)
		return b, err
	}
}

// uploadRecording sends a trace stopped on this computer to jarvisd (POST
// /api/v0/computer/recordings); on a failure the local copy stays.
func uploadRecording(cc *node.CC, tr computer.Trace, log *slog.Logger) {
	if cc == nil {
		log.Warn("recording: not connected to jarvisd; the recording stays on this computer only", "recording", tr.ID)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := cc.UploadRecording(ctx, computer.TraceOutput(tr)); err != nil {
		log.Warn("recording: upload failed; the recording stays on this computer only", "recording", tr.ID, "err", err)
		return
	}
	log.Info("recording: sent to jarvisd", "recording", tr.ID, "events", len(tr.Events))
}

// recordFromHere starts a recording in mode or, with mode "", ends the one going on, uploading
// it: the status icon's "Watch me" / "Stop recording" and `jarvis-agent computer record
// start [--desktop]|stop`. The answer is the recording's id, and on stop its events too
// ("rec_… 12").
func recordFromHere(ctx context.Context, rec *computer.Recorder, mode string, log *slog.Logger) (string, error) {
	if mode != "" {
		id, err := rec.Start(ctx, mode)
		if err != nil {
			log.Warn("recording: couldn't start", "err", err)
		}
		return id, err
	}
	tr, err := rec.End()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s %d", tr.ID, len(tr.Events)), nil
}

// browserRecording is the recorder's browser mode: the extension's record events (piece 28).
type browserRecording struct{ bt *browser.Tools }

func (b browserRecording) Start(ctx context.Context, id string, emit func(json.RawMessage)) error {
	if b.bt.Hub == nil || !b.bt.Hub.Connected() {
		return errors.New("no browser with the Jarvis extension is connected: open your browser and try again")
	}
	b.bt.Hub.SetRecordSink(func(e browser.RecordEvent) {
		if e.RecordingID != id {
			return
		}
		if raw, err := json.Marshal(e); err == nil {
			emit(raw)
		}
	})
	if err := b.bt.RecordStart(ctx, id); err != nil {
		b.bt.Hub.SetRecordSink(nil)
		return err
	}
	return nil
}

func (b browserRecording) Stop(ctx context.Context, id string) error {
	defer b.bt.Hub.SetRecordSink(nil)
	_, err := b.bt.RecordStop(ctx, id)
	return err
}

// islandActivity is the island's state for an activity.
func islandActivity(k computer.Kind) island.State {
	switch k {
	case computer.Controlling:
		return island.Controlling
	case computer.Paused:
		return island.Paused
	case computer.Recording:
		return island.Recording
	}
	return island.Hidden
}

// saveComputer writes agent.json's computer_control key (path: run's --config, or the default
// file).
func saveComputer(path string, on bool) error {
	path, err := settingsPath(path)
	if err != nil {
		return err
	}
	return settings.Update(path, func(s *settings.Settings) error { s.ComputerControl = &on; return nil })
}

// --- jarvis-agent computer snapshot URL: computer control's browser calls, by hand ---
//
// The computer-task executor calls these directly (they are not model tools, so `try` can't
// reach them). This debug command serves the extension itself, like `try`, so it needs no
// running agent holding the browser socket.

// computerSnapshotCmd is `jarvis-agent computer snapshot`; args follow "snapshot".
func computerSnapshotCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("computer snapshot", flag.ExitOnError)
	wait := fs.Duration("wait", 10*time.Second, "how long to wait for the browser extension to connect")
	maxEls := fs.Int("max", browser.DefaultSnapshotMax, "at most this many elements")
	asJSON := fs.Bool("json", false, "print the snapshot as JSON")
	_ = fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("usage: jarvis-agent computer snapshot [--wait 10s] [--max 300] [--json] URL")
	}
	hub, stopBrowser := startBrowser(ctx, logger())
	defer stopBrowser()
	wctx, cancel := context.WithTimeout(ctx, *wait)
	connected := hub.WaitConnected(wctx)
	cancel()
	if !connected {
		return fmt.Errorf("no browser extension connected in %s (if a jarvis-agent is running, it holds browser control: stop it first)", *wait)
	}
	bt := browser.NewTools(hub)
	tab, window, err := bt.OpenBackground(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "opened tab %s in background window %s (left open)\n", tab, window)
	snap, err := settledSnapshot(ctx, bt, tab, *maxEls)
	if err != nil {
		return err
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(snap)
	}
	printSnapshot(os.Stdout, snap)
	return nil
}

// settledSnapshot snapshots tab once the new page has loaded (its first moments are about:blank,
// which the browser doesn't let extensions script, or an empty document).
func settledSnapshot(ctx context.Context, bt *browser.Tools, tab string, max int) (computeruse.Snapshot, error) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		s, err := bt.Snapshot(ctx, tab, max)
		if (err == nil && len(s.Elements) > 0) || time.Now().After(deadline) {
			return s, err
		}
		select {
		case <-ctx.Done():
			return s, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

func printSnapshot(w io.Writer, s computeruse.Snapshot) {
	fmt.Fprintf(w, "gen %d  %s  %q  (%d elements)\n", s.Gen, s.URL, s.Title, len(s.Elements))
	for _, e := range s.Elements {
		var b strings.Builder
		fmt.Fprintf(&b, "%-5s %-10s %q", e.Ref, e.Role, e.Name)
		if e.Type != "" {
			fmt.Fprintf(&b, " type=%s", e.Type)
		}
		if e.Autocomplete != "" {
			fmt.Fprintf(&b, " autocomplete=%s", e.Autocomplete)
		}
		if e.Value != "" {
			fmt.Fprintf(&b, " value=%q", e.Value)
		}
		if len(e.State) > 0 {
			fmt.Fprintf(&b, " [%s]", strings.Join(e.State, " "))
		}
		if e.Frame != 0 {
			fmt.Fprintf(&b, " frame=%d", e.Frame)
		}
		fmt.Fprintln(w, b.String())
	}
}

// --- jarvis-agent computer record --browser: the extension's recorder, by hand ---

// computerRecordCmd is `jarvis-agent computer record --browser`; args follow "record". It
// records the default browser until a line on in (Enter) or ctx ends (^C), printing each event.
func computerRecordCmd(ctx context.Context, args []string, in io.Reader, w io.Writer) error {
	fs := flag.NewFlagSet("computer record", flag.ExitOnError)
	inBrowser := fs.Bool("browser", false, "record the browser (the only mode so far)")
	wait := fs.Duration("wait", 10*time.Second, "how long to wait for the browser extension to connect")
	asJSON := fs.Bool("json", false, "print each event as a JSON line")
	_ = fs.Parse(args)
	if !*inBrowser || fs.NArg() != 0 {
		return errors.New("usage: jarvis-agent computer record --browser [--wait 10s] [--json]")
	}
	hub, stopBrowser := startBrowser(ctx, logger())
	defer stopBrowser()
	wctx, cancel := context.WithTimeout(ctx, *wait)
	connected := hub.WaitConnected(wctx)
	cancel()
	if !connected {
		return fmt.Errorf("no browser extension connected in %s (if a jarvis-agent is running, it holds browser control: stop it first)", *wait)
	}
	enc := json.NewEncoder(w)
	hub.SetRecordSink(func(e browser.RecordEvent) {
		if *asJSON {
			_ = enc.Encode(e)
		} else {
			printRecordEvent(w, e)
		}
	})
	id := fmt.Sprintf("rec_debug_%d", time.Now().Unix())
	bt := browser.NewTools(hub)
	if err := bt.RecordStart(ctx, id); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "recording %s: do the task in the browser, then press Enter (or ^C)\n", id)
	line := make(chan struct{})
	go func() {
		_, _ = bufio.NewReader(in).ReadString('\n')
		close(line)
	}()
	select {
	case <-line:
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	n, err := bt.RecordStop(sctx, id)
	if err != nil {
		return err
	}
	time.Sleep(200 * time.Millisecond) // the last events arrive just before stop's answer; let them print
	fmt.Fprintf(os.Stderr, "stopped %s: %d events\n", id, n)
	return nil
}

func printRecordEvent(w io.Writer, e browser.RecordEvent) {
	var b strings.Builder
	fmt.Fprintf(&b, "%4d %-8s", e.Seq, e.Kind)
	if e.Key != "" {
		fmt.Fprintf(&b, " %s", e.Key)
	}
	if el := e.Element; el != nil {
		fmt.Fprintf(&b, " %s %q", cmp.Or(el.Role, el.Tag), el.Name)
		if el.Type != "" && el.Type != el.Tag {
			fmt.Fprintf(&b, " type=%s", el.Type)
		}
		fmt.Fprintf(&b, " sel=%s", el.Selector)
		if el.Frame != 0 {
			fmt.Fprintf(&b, " frame=%d", el.Frame)
		}
	}
	switch {
	case e.Masked:
		b.WriteString(" masked")
	case e.Value != "":
		fmt.Fprintf(&b, " value=%q", e.Value)
	}
	if e.Mime != "" {
		fmt.Fprintf(&b, " mime=%s", e.Mime)
	}
	fmt.Fprintf(&b, "  %s", e.URL)
	fmt.Fprintln(w, b.String())
}
