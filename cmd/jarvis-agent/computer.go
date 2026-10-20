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
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/input"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/screen"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/computeruse"
)

const computerUsage = "usage: jarvis-agent computer stop|status|on|off|record start [--desktop]|record stop|snapshot URL|record --browser|download URL REF|click X Y"

// computerCmd is `jarvis-agent computer stop|status|on|off|record start [--desktop]|record stop`:
// stop what Jarvis is doing with this computer (a computer task, a recording), as the status
// icon's "Stop Jarvis" does, say what it is doing, turn computer control on or off, as its
// "Computer control" does, or record what the user does in the browser, as its "Record a
// macro…" does (or on the desktop, with --desktop). All ask the running agent over its control
// socket. (`computer snapshot`, `computer record --browser` and `computer download` are
// computerSnapshotCmd, computerRecordCmd and computerDownloadCmd, which main dispatches before
// this.) `click X Y` is a debug command: a left click at global logical X, Y through this
// desktop's input backend.
func computerCmd(args []string, w io.Writer, dial control.Dialer) error {
	if len(args) == 3 && args[0] == "click" {
		return computerClick(args[1:], w)
	}
	cmds := map[string]string{"stop": control.ComputerStop, "status": control.ComputerStatus,
		"on": control.ComputerOn, "off": control.ComputerOff,
		"record start": control.ComputerRecordStart, "record stop": control.ComputerRecordStop,
		"record start --desktop": control.ComputerRecordDesktop}
	cmd := ""
	if len(args) >= 1 && len(args) <= 3 {
		cmd = cmds[strings.Join(args, " ")]
	}
	if cmd == "" {
		return errors.New(computerUsage)
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
// then it is kept here and sent once connected, the user told through note).
func newRecorder(dir string, activity *computer.Activity, tools node.Multi, cc func() *node.CC, note func(string), log *slog.Logger) (*computer.Recorder, *recordingUploads) {
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
	rec := &computer.Recorder{Dir: dir, Activity: activity, Sources: sources, Log: log}
	up := &recordingUploads{rec: rec, cc: cc, note: note, log: log, every: recordingRetryEvery}
	rec.Stopped = up.stopped
	return rec, up
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

// recordingKeptNote tells the user a recording stopped here isn't with jarvisd yet.
const recordingKeptNote = "Recording kept on this computer: Jarvis gets it once connected"

// recordingRefusedNote tells the user jarvisd won't take a recording stopped here.
const recordingRefusedNote = "Jarvis couldn't take the recording: it stays on this computer"

// recordingRetryEvery is how often, while connected, traces not sent yet are tried again.
const recordingRetryEvery = 5 * time.Minute

// recordingUploads sends the traces stopped on this computer to jarvisd (POST
// /api/v0/computer/recordings): at the stop, then those not sent yet (rec.Pending: stopped
// while not connected, or the upload failed) on every connect and every `every` while
// connected, oldest first. jarvisd keeps a recording by its id, so one sent twice is one.
type recordingUploads struct {
	rec   *computer.Recorder
	cc    func() *node.CC // nil while not connected
	note  func(string)    // tells the user (the island); nil for nobody
	log   *slog.Logger
	every time.Duration

	mu sync.Mutex // one sweep at a time
}

// stopped is the recorder's Stopped: it sends tr (and any older ones) and tells the user when
// tr stays here.
func (u *recordingUploads) stopped(tr computer.Trace) {
	go func() {
		retry := true
		if cc := u.cc(); cc != nil {
			var kept bool
			if retry, kept = u.sweep(cc)[tr.ID]; !kept {
				return
			}
		}
		u.log.Warn("recording: not sent to jarvisd", "recording", tr.ID, "sent_later", retry)
		if u.note != nil {
			u.note(map[bool]string{true: recordingKeptNote, false: recordingRefusedNote}[retry])
		}
	}()
}

// serve is part of OnConnected: it sends the traces not sent yet at once and every u.every
// until ctx ends.
func (u *recordingUploads) serve(ctx context.Context, cc *node.CC) {
	for {
		u.sweep(cc)
		select {
		case <-ctx.Done():
			return
		case <-time.After(u.every):
		}
	}
}

// sweep sends the traces not sent yet, oldest first, stopping at the first that doesn't go.
// It returns those it didn't send: true for one tried again later, false for one jarvisd
// refused for good (a bad trace, an id that is another node's), which stays on this computer
// only.
func (u *recordingUploads) sweep(cc *node.CC) map[string]bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	kept := map[string]bool{}
	pending := u.rec.Pending()
	for i, tr := range pending {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err := cc.UploadRecording(ctx, computer.TraceOutput(tr))
		cancel()
		var he *node.HTTPError
		switch {
		case err == nil:
			u.rec.Sent(tr.ID)
			u.log.Info("recording: sent to jarvisd", "recording", tr.ID, "events", len(tr.Events))
		case errors.As(err, &he) && slices.Contains([]int{http.StatusBadRequest, http.StatusConflict,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity}, he.Status):
			u.rec.Sent(tr.ID)
			kept[tr.ID] = false
			u.log.Warn("recording: jarvisd refused it; the recording stays on this computer only", "recording", tr.ID, "err", err)
		default:
			u.log.Warn("recording: upload failed; trying again later", "recording", tr.ID, "err", err)
			for _, tr := range pending[i:] {
				kept[tr.ID] = true
			}
			return kept
		}
	}
	return kept
}

// recordFromHere starts a recording in mode or, with mode "", ends the one going on, uploading
// it: the status icon's "Record a macro…" / "Stop recording" and `jarvis-agent computer record
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

// nativeInput is the desktop's input backend (a variable for tests).
var nativeInput = input.Native

// computerClick clicks at X, Y.
func computerClick(args []string, w io.Writer) error {
	x, errX := strconv.Atoi(args[0])
	y, errY := strconv.Atoi(args[1])
	if errX != nil || errY != nil {
		return errors.New(computerUsage)
	}
	in := nativeInput()
	if in == nil {
		return errors.New("no input backend for this desktop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if ok, why := in.Available(ctx); !ok {
		return fmt.Errorf("%w: %s", input.ErrUnavailable, why)
	}
	if err := in.Click(ctx, x, y, "left", 1); err != nil {
		return err
	}
	fmt.Fprintf(w, "Clicked at %d,%d.\n", x, y)
	return nil
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

// --- jarvis-agent computer snapshot URL, download URL REF: computer control's browser calls, by hand ---
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
	bt, stopBrowser, err := connectedBrowser(ctx, *wait)
	if err != nil {
		return err
	}
	defer stopBrowser()
	tab, err := openTaskWindow(ctx, bt, fs.Arg(0))
	if err != nil {
		return err
	}
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

// computerDownloadCmd is `jarvis-agent computer download`; args follow "download". It opens URL
// in a task window, snapshots it (refs are the snapshot's: take REF from `computer snapshot URL`)
// and downloads what pressing REF starts.
func computerDownloadCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("computer download", flag.ExitOnError)
	wait := fs.Duration("wait", 10*time.Second, "how long to wait for the browser extension to connect")
	_ = fs.Parse(args)
	if fs.NArg() != 2 {
		return errors.New("usage: jarvis-agent computer download [--wait 10s] URL REF")
	}
	bt, stopBrowser, err := connectedBrowser(ctx, *wait)
	if err != nil {
		return err
	}
	defer stopBrowser()
	tab, err := openTaskWindow(ctx, bt, fs.Arg(0))
	if err != nil {
		return err
	}
	snap, err := settledSnapshot(ctx, bt, tab, browser.DefaultSnapshotMax)
	if err != nil {
		return err
	}
	e := snap.Find(fs.Arg(1))
	if e == nil {
		return fmt.Errorf("the page has no %s (it has %d elements; see jarvis-agent computer snapshot %s)", fs.Arg(1), len(snap.Elements), fs.Arg(0))
	}
	fmt.Fprintf(os.Stderr, "pressing %s %s %q\n", e.Ref, e.Role, e.Name)
	r, err := bt.Download(ctx, tab, e.Ref, "")
	if err != nil {
		return err
	}
	fmt.Printf("%s  %d bytes  %s\n", r.Path, r.Bytes, r.MIME)
	return nil
}

// connectedBrowser serves the browser extension itself and waits for it to connect.
func connectedBrowser(ctx context.Context, wait time.Duration) (*browser.Tools, func(), error) {
	hub, stopBrowser := startBrowser(ctx, logger())
	wctx, cancel := context.WithTimeout(ctx, wait)
	connected := hub.WaitConnected(wctx)
	cancel()
	if !connected {
		stopBrowser()
		return nil, nil, fmt.Errorf("no browser extension connected in %s (if a jarvis-agent is running, it holds browser control: stop it first)", wait)
	}
	return browser.NewTools(hub), stopBrowser, nil
}

// openTaskWindow opens url in a background task window, which it leaves open.
func openTaskWindow(ctx context.Context, bt *browser.Tools, url string) (string, error) {
	tab, window, err := bt.OpenBackground(ctx, url)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "opened tab %s in background window %s (left open)\n", tab, window)
	return tab, nil
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
