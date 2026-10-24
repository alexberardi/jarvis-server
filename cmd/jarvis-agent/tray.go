package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/tray"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// svcWebUI is the discovery name of the server's web UI (the admin / member web app).
const svcWebUI = "jarvis-admin"

// trayIcon is run's status icon (internal/agent/tray): the agent's state, the voice toggle,
// "Open Jarvis Automation" once the server's web UI is known, and Quit.
type trayIcon struct {
	off       bool // --no-tray, or agent.json's tray: false
	log       *slog.Logger
	sw        *voiceSwitch
	h         *host.Host
	configURL string
	quit      func()
	// setup opens the setup page (`jarvis-agent setup`, a process of its own).
	setup func()
	// signOut signs this computer out (node.Runner.SignOut); nil hides the item.
	signOut func()
	// mainLoop: macOS's AppKit loop runs (the status item lives in it).
	mainLoop bool
	// activity is what Jarvis is doing with the computer: the menu's line and Stop item.
	activity *computer.Activity
	// computer is computer control's switch (the menu's "Computer control"); nil hides it.
	computer *computer.Switch
	// recorder records what the user does (the menu's "Record a macro…"); nil hides it.
	recorder *computer.Recorder
	// reached says the agent has reached its server (node.Runner.Reached); while it hasn't,
	// the menu says what it is waiting for and offers "Retry now". nil: always reached.
	reached func() bool
	// retryNow tries the server again at once (node.Runner.RetryNow).
	retryNow func()
	// webUI wakes findWebUI's own retry loop (the server has just answered, or "Retry now"):
	// without it "Open Jarvis Automation" can be up to two minutes behind the server. Buffered
	// 1; nil leaves the loop to its backoff.
	webUI chan struct{}
	// webUIEvery is findWebUI's first wait between tries (it doubles up to two minutes); 0 is
	// two seconds. Tests set it long to show that the wake-up doesn't wait for it.
	webUIEvery time.Duration

	t      atomic.Pointer[tray.Tray]
	update atomic.Pointer[string] // the self-update's status line
}

// setUpdate shows the self-update's status line in the menu.
func (ic *trayIcon) setUpdate(text string) {
	ic.update.Store(&text)
	ic.log.Info("update: " + text)
	if t := ic.t.Load(); t != nil {
		t.Update(func(s *tray.State) { s.Update = text })
	}
}

// show puts the icon up (where this desktop can show one) and returns what takes it down.
func (ic *trayIcon) show(ctx context.Context) func() {
	if ic.off {
		ic.log.Info("tray: hidden by the settings (tray: false or --no-tray)")
		return func() {}
	}
	t, err := tray.Start(tray.Options{Log: ic.log, State: ic.state(), MainLoop: ic.mainLoop,
		Actions: tray.Actions{SetVoice: ic.sw.Set, Open: ic.open, Setup: ic.setup, Quit: ic.quitFromMenu, SignOut: ic.signOutFromMenu,
			StopTask: ic.stopTaskFromMenu, SetComputer: ic.setComputerFromMenu, Record: ic.recordFromMenu,
			RetryNow: ic.retryNowFromMenu}})
	if err != nil {
		ic.log.Info("tray: no status icon", "reason", err)
		return func() {}
	}
	ic.t.Store(t)
	t.Update(func(s *tray.State) { *s = ic.withWebUI(ic.state(), s.OpenURL) }) // a change while starting
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); ic.findWebUI(ctx) }()
	return func() {
		cancel()
		<-done
		ic.t.Store(nil)
		t.Close()
	}
}

func (ic *trayIcon) withWebUI(s tray.State, u string) tray.State { s.OpenURL = u; return s }

// state is what the menu shows, the web UI aside.
func (ic *trayIcon) state() tray.State {
	s := tray.State{Status: "Running", VoiceAvailable: voice.Supported, Voice: ic.sw.On(), Setup: ic.setup != nil,
		SignOut: ic.signOut != nil}
	switch {
	case ic.reached != nil && !ic.reached():
		// The server isn't there yet (it may be started after the agent): say so rather than
		// claim the agent runs, since nothing works until it answers (#585).
		s.Waiting, s.Status = true, tray.WaitingFor(ic.configURL)
	case !s.Voice && ic.sw.lastErr() != "":
		s.Status = "Running, couldn't start listening"
	}
	if u := ic.update.Load(); u != nil {
		s.Update = *u
	}
	k, _ := ic.activity.Status()
	s.Activity = string(k)
	s.StopHotkey = island.StopHotkey()
	s.ComputerAvailable, s.Computer = ic.computer != nil, ic.computer.On()
	s.Record = ic.recorder != nil
	if s.Record {
		_, s.RecordDesktop = ic.recorder.Sources[computer.ModeDesktop]
	}
	return s
}

// setComputerFromMenu is "Computer control": off also stops a task going on.
func (ic *trayIcon) setComputerFromMenu(on bool) {
	ic.log.Info("Computer control chosen in the menu", "on", on, "activity", ic.activity.String())
	if err := ic.computer.Set(on); err != nil {
		ic.log.Warn("computer control: couldn't save the choice; it lasts until the agent restarts", "err", err)
	}
}

// activityChanged is the Activity telling the menu.
func (ic *trayIcon) activityChanged(computer.Kind, string) { ic.refresh() }

func (ic *trayIcon) stopTaskFromMenu() {
	ic.log.Info("Stop Jarvis chosen in the menu", "activity", ic.activity.String())
	ic.activity.Stop()
}

// recordFromMenu is "Record a macro…" (computer.ModeBrowser), "Record the desktop…"
// (computer.ModeDesktop) and "Stop recording" (""): a stopped trace goes to jarvisd.
func (ic *trayIcon) recordFromMenu(mode string) {
	ic.log.Info("recording chosen in the menu", "mode", mode, "activity", ic.activity.String())
	if ic.recorder != nil {
		_, _ = recordFromHere(context.Background(), ic.recorder, mode, ic.log)
	}
}

// voiceChanged is the voice switch telling the menu.
func (ic *trayIcon) voiceChanged(bool) { ic.refresh() }

// refresh shows ic.state() in the menu.
func (ic *trayIcon) refresh() {
	if t := ic.t.Load(); t != nil {
		t.Update(func(s *tray.State) { *s = ic.withWebUI(ic.state(), s.OpenURL) })
	}
}

func (ic *trayIcon) quitFromMenu() {
	ic.log.Info("Quit chosen in the menu: stopping until the next login or start")
	ic.quit()
}

func (ic *trayIcon) signOutFromMenu() {
	ic.log.Info("Sign out chosen in the menu: removing this computer from Jarvis Automation")
	if ic.signOut != nil {
		ic.signOut()
	}
}

// retryNowFromMenu is "Retry now": try the server again without waiting for the backoff.
func (ic *trayIcon) retryNowFromMenu() {
	ic.log.Info("Retry now chosen in the menu", "url", ic.configURL)
	if ic.retryNow != nil {
		ic.retryNow()
	}
	ic.lookForWebUI()
}

// lookForWebUI asks findWebUI to try the server again now rather than after its backoff.
func (ic *trayIcon) lookForWebUI() {
	select {
	case ic.webUI <- struct{}{}:
	default: // no loop listening, or one is already due
	}
}

func (ic *trayIcon) open(u string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ic.h.Open(ctx, u); err != nil {
		ic.log.Warn("tray: couldn't open the web UI", "url", u, "err", err)
	}
}

// findWebUI asks jarvisd for its web UI's address (retrying while it can't be reached) and adds
// "Open Jarvis Automation" once known.
func (ic *trayIcon) findWebUI(ctx context.Context) {
	hc := &http.Client{Timeout: 10 * time.Second}
	backoff := ic.webUIBackoff()
	for {
		if svcs, err := node.Discover(ctx, hc, ic.configURL); err == nil {
			if u := webUIURL(svcs); u != "" {
				if t := ic.t.Load(); t != nil {
					t.Update(func(s *tray.State) { s.OpenURL = u })
				}
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ic.webUI: // the server has just answered: look now
			backoff = ic.webUIBackoff()
		case <-time.After(backoff):
			backoff = min(2*backoff, 2*time.Minute)
		}
	}
}

// webUIBackoff is findWebUI's first wait between tries.
func (ic *trayIcon) webUIBackoff() time.Duration {
	if ic.webUIEvery > 0 {
		return ic.webUIEvery
	}
	return 2 * time.Second
}

// webUIURL is the web UI's http(s) address from discovery, or "".
func webUIURL(svcs node.Services) string {
	u, err := url.Parse(svcs[svcWebUI])
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return ""
	}
	return u.String()
}

// trayStatus is the `status` line: hidden by the settings, or whether this desktop shows it.
func trayStatus() string {
	if p, err := settings.DefaultPath(); err == nil {
		if s, _, err := settings.Load(p); err == nil && s.Tray != nil && !*s.Tray {
			return "hidden (agent.json tray: false; `jarvis-agent config unset tray` shows it again)"
		}
	}
	return tray.Probe()
}
