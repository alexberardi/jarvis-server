package firstrun

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// Deps is everything the page touches on this computer and the network, so tests swap it.
type Deps struct {
	Log *slog.Logger
	Now func() time.Time
	// HTTP talks to jarvisd (and probes servers). Default: 10 s timeout.
	HTTP *http.Client
	// GOOS is the computer's OS (runtime.GOOS); the page shows the steps that apply.
	GOOS string
	// ComputerName is how this computer is shown in Jarvis Automation (the hostname).
	ComputerName string
	// Icon is the shield (PNG) the page shows.
	Icon []byte

	// Creds is this computer's sign-in to Jarvis, if it has one.
	Creds func() (node.Creds, bool)
	// SaveCreds keeps a new sign-in (node.SaveCreds at the default path).
	SaveCreds func(node.Creds) error
	// AfterJoin runs once the computer is added: register the browsers' native host and start
	// the agent. Its error is shown, but the computer stays added.
	AfterJoin func(ctx context.Context) error

	// Browse looks for servers on the network (mDNS) until ctx ends, reporting each config
	// service URL it finds with a name for it. Nil: no network search.
	Browse func(ctx context.Context, found func(url, name string))
	// Loopback is the config service URL to try on this computer first. Default
	// http://127.0.0.1:7700.
	Loopback string
	// ConfigPort is the config service's port, tried on a typed address's host when that address
	// isn't the config service (the web app's). Default node.DefaultConfigPort.
	ConfigPort string
	// BrowseFor bounds one network search. Default 4 s.
	BrowseFor time.Duration
	// WaitPairing sleeps between pairing polls (node.SleepCtx).
	WaitPairing func(ctx context.Context, d time.Duration) error

	// Voice is the "Listen for Hey Jarvis" switch; nil hides it (Windows, for now).
	Voice *Voice
	// Mic opens the microphone for the level meter; nil hides the meter.
	Mic MicOpener
	// Permissions are the macOS privacy grants, in the order shown; empty elsewhere.
	Permissions []Permission
	// Portal is the GNOME/KDE "allow typing" step; nil where it doesn't apply.
	Portal *Portal
	// OpenURL opens a link outside the browser (System Settings panes).
	OpenURL func(ctx context.Context, url string) error
	// Remove takes the agent off this computer (uninstall, with its data).
	Remove func(ctx context.Context) error
	// RemoveNote is what the page says once removed ("drag the app to the Trash" on macOS).
	RemoveNote string
	// Stores are the browser extension's store pages.
	Stores Stores
}

// Voice is the wake-word switch: the same setting as the status icon's menu.
type Voice struct {
	// On reports the saved choice.
	On func() bool
	// Set saves the choice and tells the running agent.
	Set func(on bool) error
}

// Permission is one macOS privacy grant the agent needs.
type Permission struct {
	ID    string // "accessibility", "microphone", "automation", "screen"
	Label string // "Accessibility"
	Why   string // what it's for, in plain words
	// Status is "granted", "denied", "not_asked" or "unknown". It never prompts.
	Status func() string
	// Ask shows macOS's own prompt (when it hasn't been answered yet). Optional.
	Ask func()
	// Pane is the System Settings link for it.
	Pane string
}

// Permission statuses.
const (
	Granted  = "granted"
	Denied   = "denied"
	NotAsked = "not_asked"
	Unknown  = "unknown"
)

// Portal is GNOME/KDE's one-time "allow typing" dialog (`jarvis-agent dictation allow`).
type Portal struct {
	// Status: allowed, available (a dialog can be shown), and why not.
	Status func(ctx context.Context) (allowed, available bool, why string)
	// Allow shows the desktop's dialog and waits for the answer.
	Allow func(ctx context.Context) error
}

func (d Deps) withDefaults() Deps {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.HTTP == nil {
		d.HTTP = &http.Client{Timeout: 10 * time.Second}
	}
	if d.Loopback == "" {
		d.Loopback = "http://127.0.0.1:7700"
	}
	if d.ConfigPort == "" {
		d.ConfigPort = node.DefaultConfigPort
	}
	if d.BrowseFor <= 0 {
		d.BrowseFor = 4 * time.Second
	}
	if d.WaitPairing == nil {
		d.WaitPairing = node.SleepCtx
	}
	if d.Creds == nil {
		d.Creds = func() (node.Creds, bool) { return node.Creds{}, false }
	}
	if d.Stores == (Stores{}) {
		d.Stores = DefaultStores
	}
	return d
}
