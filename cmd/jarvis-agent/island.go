package main

import (
	"context"
	"errors"
	"flag"
	"math"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
)

// islandCue shows the voice pipeline's state on the island.
type islandCue struct{ i *island.Island }

func (c islandCue) SetState(cue voice.Cue, window time.Duration) { c.i.Set(islandState(cue), window) }
func (c islandCue) Level(v float32)                              { c.i.Level(v) }

func islandState(c voice.Cue) island.State {
	switch c {
	case voice.CueListening:
		return island.Listening
	case voice.CueThinking:
		return island.Thinking
	case voice.CueSpeaking:
		return island.Speaking
	case voice.CueFollowUp:
		return island.FollowUp
	case voice.CueError:
		return island.Error
	}
	return island.Hidden
}

// islandDemo (not in the usage: a development aid) plays the island's states with a
// synthetic voice level, or holds one state, without a microphone or a server.
//
//	jarvis-agent island-demo                       # one turn: listen, think, speak, follow-up, ...
//	jarvis-agent island-demo --hold follow_up --for 20s
func islandDemo(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("island-demo", flag.ExitOnError)
	hold := fs.String("hold", "", "show only this state: listening, thinking, speaking, follow_up, error")
	dur := fs.Duration("for", 10*time.Second, "with --hold: for how long")
	_ = fs.Parse(args)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	log := logger()
	return island.Run(ctx, log, func(i *island.Island) error {
		if i == nil {
			return errors.New("this desktop can't show the island (see the log line above)")
		}
		step := func(s island.State, window, d time.Duration, voiced bool) bool {
			i.Set(s, window)
			end := time.Now().Add(d)
			for t := 0.0; time.Now().Before(end); t += 0.08 {
				if voiced {
					// Syllables: a 4 Hz envelope between a murmur and loud speech.
					i.Level(float32(0.02 + 0.18*math.Abs(math.Sin(t*math.Pi*2))))
				}
				select {
				case <-ctx.Done():
					return false
				case <-time.After(80 * time.Millisecond):
				}
			}
			return true
		}
		if *hold != "" {
			states := map[string]island.State{"listening": island.Listening, "thinking": island.Thinking,
				"speaking": island.Speaking, "follow_up": island.FollowUp, "error": island.Error}
			s, ok := states[*hold]
			if !ok {
				return errors.New("--hold: listening, thinking, speaking, follow_up or error")
			}
			step(s, *dur, *dur, s == island.Listening || s == island.Speaking)
			i.Set(island.Hidden, 0)
			time.Sleep(500 * time.Millisecond)
			return nil
		}
		for _, st := range []struct {
			s      island.State
			window time.Duration
			d      time.Duration
			voiced bool
		}{
			{island.Listening, 0, 2500 * time.Millisecond, true},
			{island.Thinking, 0, 1500 * time.Millisecond, false},
			{island.Speaking, 0, 2500 * time.Millisecond, true},
			{island.FollowUp, 8 * time.Second, 3 * time.Second, false},
			{island.Listening, 0, 1500 * time.Millisecond, true},
			{island.Thinking, 0, time.Second, false},
			{island.FollowUp, 3 * time.Second, 3 * time.Second, false},
			{island.Hidden, 0, 1500 * time.Millisecond, false},
			{island.Error, 0, 2 * time.Second, false},
		} {
			if !step(st.s, st.window, st.d, st.voiced) {
				break
			}
		}
		return nil
	})
}
