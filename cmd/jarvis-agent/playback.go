package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	displaynode "github.com/alexberardi/jarvis-server/internal/agent/display"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/packages"
	"github.com/alexberardi/jarvis-server/internal/agent/playback"
	"github.com/alexberardi/jarvis-server/internal/agent/sidecar"
)

// The node's playback hub (docs/agent/PLAYBACK.md piece 2): its sources are the packages'
// music commands (the sidecar) and, on a display node, the display's own player. Nothing here
// names a service.

// packageSource is one IJarvisMusicCommand, reached through the sidecar in service.
type packageSource struct {
	live *sidecar.Live
	cmd  sidecar.MusicCommand
	name string
}

func (s packageSource) ID() string   { return "pkg:" + s.cmd.Name }
func (s packageSource) Name() string { return s.name }
func (s packageSource) Status(ctx context.Context) (*playback.State, error) {
	raw, err := s.live.PlaybackStatus(ctx, s.cmd.Name)
	if err != nil {
		return nil, err
	}
	return playback.Parse(raw)
}
func (s packageSource) Control(ctx context.Context, action string, args map[string]any) (*playback.State, error) {
	raw, err := s.live.PlaybackControl(ctx, s.cmd.Name, action, args)
	if err != nil {
		return nil, err
	}
	return playback.Parse(raw)
}

// Artwork is the command's playback_artwork(): the cover as bytes.
func (s packageSource) Artwork(ctx context.Context) ([]byte, string, error) {
	return s.live.PlaybackArtwork(ctx, s.cmd.Name)
}

// displaySource is the touch display's mpv (display_media, Cast and Playercast all play there).
type displaySource struct{ e *displaynode.Engine }

func (displaySource) ID() string   { return "display" }
func (displaySource) Name() string { return "Display" }
func (d displaySource) Status(context.Context) (*playback.State, error) {
	return displayState(d.e.GetPlaybackStatus()), nil
}
func (d displaySource) Control(ctx context.Context, action string, args map[string]any) (*playback.State, error) {
	r := displaynode.MediaRequest{Action: action, Destination: "local"}
	switch action {
	case "play":
		r.Action = "resume"
	case "seek":
		r.Seconds, _ = args["position"].(float64)
	}
	if _, err := d.e.ControlMedia(ctx, r); err != nil {
		return nil, err
	}
	return displayState(d.e.GetPlaybackStatus()), nil
}

// displayState maps GetPlaybackStatus: only a controllable mpv that is playing counts.
func displayState(m map[string]any) *playback.State {
	if active, _ := m["active"].(bool); !active {
		return nil
	}
	if ctl, _ := m["controllable"].(bool); !ctl {
		return nil
	}
	st := &playback.State{State: "playing", Actions: []string{"pause", "play", "seek"}}
	if paused, _ := m["paused"].(bool); paused {
		st.State = "paused"
	}
	if p, ok := m["position"].(float64); ok {
		st.Position = &p
	}
	if d, ok := m["duration"].(float64); ok {
		st.Duration = &d
	}
	return st
}

// packageNames names a music command after its package's display_name ("Spotify"), else the
// package or command name.
// shortcut: remembered per package for the agent's life, so a renamed display_name shows after a restart.
func packageNames(live *sidecar.Live, list func() []packages.Installed) func(string) string {
	var mu sync.Mutex
	known := map[string]string{}
	return func(command string) string {
		pkg := ""
		if sc := live.Current(); sc != nil {
			pkg = sc.Package(command)
		}
		if pkg == "" {
			return command
		}
		mu.Lock()
		defer mu.Unlock()
		if name, ok := known[pkg]; ok {
			return name
		}
		name := pkg
		if list != nil {
			for _, in := range list() {
				if in.Manifest.Name == pkg && in.Manifest.DisplayName != "" {
					name = in.Manifest.DisplayName
				}
			}
		}
		known[pkg] = name
		return name
	}
}

// playbackSources lists the sources now: the sidecar's music commands (a package install swaps
// it, so this is read per tick) and the display.
func playbackSources(live *sidecar.Live, display *displaynode.Engine, names func(string) string) func() []playback.Source {
	return func() []playback.Source {
		var out []playback.Source
		if live != nil {
			for _, mc := range live.MusicCommands() {
				out = append(out, packageSource{live: live, cmd: mc, name: names(mc.Name)})
			}
		}
		if display != nil {
			out = append(out, displaySource{display})
		}
		return out
	}
}

// runPlayback ticks the hub: each tick first takes what the packages pushed with report()
// (queued in the sidecar until asked), then checks the sources that are due.
func runPlayback(ctx context.Context, hub *playback.Hub, live *sidecar.Live, log *slog.Logger) {
	t := time.NewTicker(playback.DefaultInterval)
	defer t.Stop()
	for {
		if live != nil {
			reports, err := live.PlaybackReports(ctx)
			if err != nil {
				log.Debug("playback: reports", "err", err)
			}
			for _, r := range reports {
				st, err := playback.Parse(r.State)
				if err != nil {
					log.Warn("playback: bad report", "command", r.Command, "err", err)
					continue
				}
				for _, s := range hub.Sources() {
					if s.ID() == "pkg:"+r.Command {
						hub.Report(s, st)
					}
				}
			}
		}
		hub.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// logPlayback logs each change of what the hub shows.
func logPlayback(log *slog.Logger) func(*playback.Now) {
	return func(n *playback.Now) {
		if n == nil {
			log.Info("playback: nothing playing")
			return
		}
		b, _ := json.Marshal(n.State)
		log.Info("playback: now", "source", n.Source, "state", string(b))
	}
}

// playbackReporter sends what the hub shows to jarvisd (P12): each change at most once a
// second, the latest winning; while something plays, again every playbackResend even with no
// change (jarvisd drops a report not refreshed within 30 s, P14); and nil once when it stops.
type playbackReporter struct {
	cc   func() *node.CC
	art  func(context.Context) ([]byte, string, bool) // the hub's Artwork; nil: no covers
	log  *slog.Logger
	now  func() time.Time
	wake chan struct{}

	// The cover jarvisd has (only run's goroutine touches these): coverKey is the track it was
	// asked for, cover its key in reports, "" when the source had none.
	coverKey, cover string

	mu       sync.Mutex
	latest   *playback.Now
	dirty    bool
	lastSent time.Time
}

const (
	playbackMinGap = time.Second
	playbackResend = 10 * time.Second
)

func newPlaybackReporter(cc func() *node.CC, art func(context.Context) ([]byte, string, bool), log *slog.Logger) *playbackReporter {
	return &playbackReporter{cc: cc, art: art, log: log, now: time.Now, wake: make(chan struct{}, 1)}
}

// update is the hub's OnChange.
func (p *playbackReporter) update(n *playback.Now) {
	p.mu.Lock()
	p.latest, p.dirty = n, true
	p.mu.Unlock()
	p.poke()
}

// force sends the current state on the next pass even if nothing changed: a control from
// jarvisd waits for the node's read-back.
func (p *playbackReporter) force() {
	p.mu.Lock()
	p.dirty = true
	p.mu.Unlock()
	p.poke()
}

func (p *playbackReporter) poke() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// due is what to send now, if anything.
func (p *playbackReporter) due() (*playback.Now, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	since := p.now().Sub(p.lastSent)
	switch {
	case p.dirty && since >= playbackMinGap:
	case !p.dirty && p.latest != nil && since >= playbackResend:
	default:
		return nil, false
	}
	p.dirty, p.lastSent = false, p.now()
	return p.latest, true
}

func (p *playbackReporter) run(ctx context.Context) {
	t := time.NewTicker(playbackMinGap)
	defer t.Stop()
	for {
		if n, ok := p.due(); ok {
			p.send(ctx, n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-p.wake:
		}
	}
}

func (p *playbackReporter) send(ctx context.Context, n *playback.Now) {
	cc := p.cc()
	if cc == nil {
		p.force() // not connected: try again on the next pass
		return
	}
	body := map[string]any{"now": n}
	if c := p.uploadCover(ctx, cc, n); c != "" {
		body["cover"] = c
	}
	if err := cc.Do(ctx, "POST", "/api/v0/node/playback", body, nil); err != nil {
		p.log.Debug("playback: report failed", "err", err)
		p.force()
	}
}

// uploadCover sends the shown track's cover to jarvisd once per track and returns its key for
// the report, "" without one. A failed upload is tried again on the next send.
func (p *playbackReporter) uploadCover(ctx context.Context, cc *node.CC, n *playback.Now) string {
	track := playback.TrackKey(n)
	if p.art == nil || track == "" {
		return ""
	}
	if track == p.coverKey {
		return p.cover
	}
	sum := sha256.Sum256([]byte(track))
	key := hex.EncodeToString(sum[:8])
	data, mime, ok := p.art(ctx)
	if ok {
		if err := cc.UploadPlaybackCover(ctx, key, mime, data); err != nil {
			p.log.Debug("playback: cover upload failed", "err", err)
			return ""
		}
	} else {
		key = ""
	}
	p.coverKey, p.cover = track, key
	return key
}

// playbackVerb is jarvisd's playback_control command: a control from the app, sent to the
// shown source. One that arrives after its expires_at (queued while the node was away) is
// dropped, so a pause never plays out minutes later.
func playbackVerb(hub *playback.Hub, rep *playbackReporter, now func() time.Time, log *slog.Logger) func(context.Context, *node.CC, map[string]any) {
	return func(ctx context.Context, _ *node.CC, d map[string]any) {
		if exp, _ := d["expires_at"].(string); exp != "" {
			if at, err := time.Parse(time.RFC3339, exp); err == nil && now().After(at) {
				log.Info("playback: dropping an expired control", "action", d["action"])
				return
			}
		}
		action, _ := d["action"].(string)
		args := map[string]any{}
		if v, ok := d["volume"].(float64); ok {
			args["volume"] = max(0, min(100, int(v)))
		}
		if v, ok := d["position"].(float64); ok {
			args["position"] = v
		}
		if _, err := hub.Control(ctx, action, args); err != nil {
			log.Info("playback: control failed", "action", action, "err", err)
		}
		rep.force() // the app waits for the read-back, changed or not
	}
}
