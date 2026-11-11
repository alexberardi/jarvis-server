package voice

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// recDirector types anything said as "type …" and, as a SteeringDirector, scrolls on "scroll
// down"; it records which entry each transcript reached.
type recDirector struct {
	mu    sync.Mutex
	heard []string
}

func (d *recDirector) Available(context.Context) []node.ToolDef { return nil }
func (d *recDirector) Call(context.Context, string, map[string]any, node.Request) (map[string]any, bool) {
	return nil, false
}

func (d *recDirector) Direct(ctx context.Context, said string) (map[string]any, bool) {
	d.note("direct: " + said)
	if strings.HasPrefix(strings.ToLower(said), "type ") || strings.HasPrefix(strings.ToLower(said), "scroll ") {
		return map[string]any{"success": true, node.IslandOnlyKey: true}, true
	}
	return nil, false
}

func (d *recDirector) DirectSteering(ctx context.Context, said string) (map[string]any, bool) {
	d.note("steering: " + said)
	if strings.HasPrefix(strings.ToLower(said), "scroll ") {
		return map[string]any{"success": true, node.IslandOnlyKey: true}, true
	}
	return nil, false
}

func (d *recDirector) note(s string)  { d.mu.Lock(); d.heard = append(d.heard, s); d.mu.Unlock() }
func (d *recDirector) list() []string { d.mu.Lock(); defer d.mu.Unlock(); return slices.Clone(d.heard) }

// Right after Jarvis spoke, a follow-up that would change something goes to command-center's
// gate (is this speech for me?) before anything runs; steering steps stay on the node
// (decision 2026-11-10-follow-up-shortcuts-pass-the-gate).
func TestFollowUpAfterAReplySteersOnlyOnTheNode(t *testing.T) {
	for _, tc := range []struct {
		said, want string
		toCC       bool
	}{
		{"Type two diabetes runs in our family.", "steering: Type two diabetes runs in our family.", true},
		{"Scroll down.", "steering: Scroll down.", false},
	} {
		t.Run(tc.said, func(t *testing.T) {
			srv := newServer()
			srv.text, srv.texts, srv.speakPCM = "What's the weather?", []string{tc.said}, pcmOf(300)
			mic := oneWake()
			r := newRig(t, mic, srv)
			d := &recDirector{}
			r.p.Tools = node.Multi{d}
			answer(r, mic, utterance)
			stop := r.run(t)
			tr := r.turn(t)
			_ = stop()
			heard := d.list()
			if !slices.Contains(heard, tc.want) || slices.Contains(heard, "direct: "+tc.said) {
				t.Fatalf("director heard %q", heard)
			}
			if len(tr.FollowUps) == 0 {
				t.Fatalf("no follow-up: %+v", tr)
			}
			toCC := slices.Contains(srv.callList(), "command") // the follow-up's blocking /voice/command
			if toCC != tc.toCC {
				t.Fatalf("follow-up %+v, calls %v: want command-center %v", tr.FollowUps[0], srv.callList(), tc.toCC)
			}
		})
	}
}

// A process said step by step (no reply between the steps) stays on the node: the user is
// steering, and nothing was spoken that room talk could be answering.
func TestFollowUpAfterASilentStepStaysDirect(t *testing.T) {
	srv := newServer()
	srv.text, srv.texts = "Type hello.", []string{"Type world."}
	mic := oneWake()
	r := newRig(t, mic, srv)
	d := &recDirector{}
	r.p.Tools = node.Multi{d}
	answer(r, mic, utterance)
	stop := r.run(t)
	r.turn(t)
	_ = stop()
	if heard := d.list(); !slices.Equal(heard, []string{"direct: Type hello.", "direct: Type world."}) {
		t.Fatalf("director heard %q", heard)
	}
	if slices.Contains(srv.callList(), "command") || slices.Contains(srv.callList(), "stream") {
		t.Fatalf("command-center was asked: %v", srv.callList())
	}
}
