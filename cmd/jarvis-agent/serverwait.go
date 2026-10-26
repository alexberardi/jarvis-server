package main

import (
	"log/slog"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/tray"
)

// connectedNote is the island's line once the server answers after the agent waited for it.
const connectedNote = "Connected to the Jarvis server"

// serverWait tells the user what the agent is waiting for while it can't reach its server:
// the agent may be started before the server, and until the server answers nothing works —
// listening included, since the pipeline starts on the connection (voiceSwitch.Serve). Before
// this the menu said "Running" and the island showed nothing, so starting the server after the
// agent looked like a hung agent (#585).
//
// It is node.Runner's OnWaiting/OnReached: the status icon's menu is drawn again (its header
// says what is waited for, and it offers "Retry now"), and the island says it in a line once —
// the menu is the place that says it for as long as it lasts, so a server that stays away
// (a laptop away from home) doesn't keep a pill on the screen.
type serverWait struct {
	// refresh draws the status icon's menu again (trayIcon.refresh); note shows a line on the
	// island (nil where there is none).
	refresh func()
	note    func(text string)
	log     *slog.Logger

	mu   sync.Mutex
	told bool // the user has been told the agent is waiting
}

// waiting is node.Runner.OnWaiting: jarvisd didn't answer, the next try is in next.
func (w *serverWait) waiting(url string, _ error, next time.Duration) {
	w.mu.Lock()
	first := !w.told
	w.told = true
	w.mu.Unlock()
	if !first {
		return // the menu says it for as long as it lasts; the log has every try
	}
	if w.refresh != nil {
		w.refresh() // the menu's header and "Retry now"
	}
	if w.note != nil {
		w.note(tray.WaitingFor(url))
	}
	w.log.Info("waiting for the Jarvis server", "url", url, "next_try_in", next)
}

// reached is node.Runner.OnReached: the server answered, so the menu goes back to "Running",
// and the island says so if the user was told the agent was waiting.
func (w *serverWait) reached(url string) {
	w.mu.Lock()
	told := w.told
	w.told = false
	w.mu.Unlock()
	if w.refresh != nil {
		w.refresh()
	}
	if told {
		w.log.Info("the Jarvis server answered; carrying on", "url", url)
		if w.note != nil {
			w.note(connectedNote)
		}
	}
}

// voiceWaiting is the voice switch's: listening was turned on before the server was reached,
// so there is nothing to listen with yet. The menu says the same under the toggle.
func (w *serverWait) voiceWaiting() {
	if w.note != nil {
		w.note(tray.VoiceWaitText)
	}
}
