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
			StopTask: ic.stopTaskFromMenu}})
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
	if !s.Voice && ic.sw.lastErr() != "" {
		s.Status = "Running, couldn't start listening"
	}
	if u := ic.update.Load(); u != nil {
		s.Update = *u
	}
	k, _ := ic.activity.Status()
	s.Activity = string(k)
	return s
}

// activityChanged is the Activity telling the menu.
func (ic *trayIcon) activityChanged(computer.Kind, string) { ic.refresh() }

func (ic *trayIcon) stopTaskFromMenu() {
	ic.log.Info("Stop Jarvis chosen in the menu", "activity", ic.activity.String())
	ic.activity.Stop()
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
	backoff := 2 * time.Second
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
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 2*time.Minute)
	}
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
