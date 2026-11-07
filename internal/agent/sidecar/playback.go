package sidecar

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
)

// The playback sources of docs/agent/PLAYBACK.md: the packages' IJarvisMusicCommand commands.
// States travel as the SDK's PlaybackState.to_dict(); a JSON null (a nil RawMessage here) is
// "nothing playing". The playback hub parses them.

// MusicCommand is one IJarvisMusicCommand: its command name and the player process it runs.
type MusicCommand struct {
	Name, Binary string
}

// Report is a state a command pushed with report() since the last PlaybackReports.
type Report struct {
	Command string          `json:"command_name"`
	State   json.RawMessage `json:"state"`
}

// MusicCommands are the music commands, read once at start.
func (s *Sidecar) MusicCommands() []MusicCommand { return s.music }

type playbackReply struct {
	State json.RawMessage `json:"state"`
	Error *string         `json:"error"`
}

func (r playbackReply) result() (json.RawMessage, error) {
	if r.Error != nil {
		return nil, errors.New(*r.Error)
	}
	if string(r.State) == "null" {
		return nil, nil
	}
	return r.State, nil
}

// PlaybackStatus is the command's playback_status(): its state, or nil when nothing plays.
func (s *Sidecar) PlaybackStatus(_ context.Context, command string) (json.RawMessage, error) {
	var r playbackReply
	if err := s.roundTrip("playback_status", map[string]any{"command_name": command}, &r); err != nil {
		return nil, err
	}
	return r.result()
}

// PlaybackControl runs one action and returns the state the command read back afterwards.
func (s *Sidecar) PlaybackControl(_ context.Context, command, action string, args map[string]any) (json.RawMessage, error) {
	if args == nil {
		args = map[string]any{}
	}
	var r playbackReply
	if err := s.roundTrip("playback_control", map[string]any{"command_name": command, "action": action, "args": args}, &r); err != nil {
		return nil, err
	}
	return r.result()
}

// PlaybackArtwork is the current track's cover: its bytes and type, or nil when it has none.
func (s *Sidecar) PlaybackArtwork(_ context.Context, command string) ([]byte, string, error) {
	var r struct {
		Mime  string  `json:"mime"`
		Data  string  `json:"data"`
		Error *string `json:"error"`
	}
	if err := s.roundTrip("playback_artwork", map[string]any{"command_name": command}, &r); err != nil {
		return nil, "", err
	}
	if r.Error != nil {
		return nil, "", errors.New(*r.Error)
	}
	if r.Data == "" {
		return nil, "", nil
	}
	b, err := base64.StdEncoding.DecodeString(r.Data)
	if err != nil {
		return nil, "", err
	}
	return b, r.Mime, nil
}

// PlaybackReports drains what the commands pushed with report() since the last call.
func (s *Sidecar) PlaybackReports(_ context.Context) ([]Report, error) {
	var r struct {
		Reports []Report `json:"reports"`
	}
	if err := s.roundTrip("playback_reports", nil, &r); err != nil {
		return nil, err
	}
	for i := range r.Reports {
		if string(r.Reports[i].State) == "null" {
			r.Reports[i].State = nil
		}
	}
	return r.Reports, nil
}

// MusicCommands are the music commands of the sidecar in service; none without one.
func (l *Live) MusicCommands() []MusicCommand {
	if sc := l.Current(); sc != nil {
		return sc.MusicCommands()
	}
	return nil
}

// PlaybackStatus is Sidecar.PlaybackStatus on the sidecar in service.
func (l *Live) PlaybackStatus(ctx context.Context, command string) (json.RawMessage, error) {
	if sc := l.Current(); sc != nil {
		return sc.PlaybackStatus(ctx, command)
	}
	return nil, ErrNoPackages
}

// PlaybackControl is Sidecar.PlaybackControl on the sidecar in service.
func (l *Live) PlaybackControl(ctx context.Context, command, action string, args map[string]any) (json.RawMessage, error) {
	if sc := l.Current(); sc != nil {
		return sc.PlaybackControl(ctx, command, action, args)
	}
	return nil, ErrNoPackages
}

// PlaybackArtwork is Sidecar.PlaybackArtwork on the sidecar in service.
func (l *Live) PlaybackArtwork(ctx context.Context, command string) ([]byte, string, error) {
	if sc := l.Current(); sc != nil {
		return sc.PlaybackArtwork(ctx, command)
	}
	return nil, "", ErrNoPackages
}

// PlaybackReports is Sidecar.PlaybackReports on the sidecar in service; none without one.
func (l *Live) PlaybackReports(ctx context.Context) ([]Report, error) {
	if sc := l.Current(); sc != nil {
		return sc.PlaybackReports(ctx)
	}
	return nil, nil
}
