package builtins

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/node/nodetest"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/sdkhost"
	"github.com/alexberardi/jarvis-server/internal/agent/timers"
)

// No built-in carries out ordinary speech on the node (nodetest's corpus): room talk a follow-up
// window hears, and sentences that only share a command's words, go to the model.
func TestBuiltinsLeaveOrdinarySpeechToTheModel(t *testing.T) {
	st, err := nodesettings.OpenStore(t.TempDir(), "n1")
	if err != nil {
		t.Fatal(err)
	}
	noCC := func() *node.CC { return nil }
	audio := &sdkhost.Audio{Path: filepath.Join(t.TempDir(), "v.json"), Run: func(context.Context, string, ...string) (string, error) {
		return "Volume: front-left: 1 / 50% / 0 dB", nil
	}}
	steps := &stepTools{calls: map[string]map[string]any{}, outs: map[string]map[string]any{}}
	for name, d := range map[string]node.Director{
		"routine":         &Routine{Store: st, Tools: steps, CC: noCC},
		"timers":          &Timers{Service: &timers.Service{Path: filepath.Join(t.TempDir(), "timers.json")}, Store: st},
		"todo":            &Todo{Store: st},
		"shopping":        &Shopping{Store: st},
		"reminder":        &Reminder{Store: st},
		"calculate":       &Calculate{Store: st},
		"convert":         &Convert{Store: st},
		"joke":            &Joke{CC: noCC, Store: st},
		"control_node":    &ControlNode{Audio: audio, Store: st},
		"check_alerts":    &CheckAlerts{Alerts: &sdkhost.Alerts{}, CC: noCC, Store: st},
		"export_todo":     &ExportTodo{Store: st, CC: noCC},
		"export_shopping": &ExportShopping{Store: st, CC: noCC},
	} {
		t.Run(name, func(t *testing.T) { nodetest.NeverDirect(t, d) })
	}
}

// Volume up and down are the whole request: said on their own (with "a bit", "please", the
// wake word) they run on the node; inside a longer sentence they're room talk.
func TestVolumeStepsAreTheWholeRequest(t *testing.T) {
	for said, want := range map[string]string{
		"turn it down": "volume_down", "Turn it down a notch.": "volume_down", "Jarvis, louder please": "volume_up",
		"volume up a bit": "volume_up", "OK, quieter.": "volume_down", "turn the volume down a little": "volume_down",
		"Crank it up!": "volume_up", "make it louder": "volume_up", "make it quieter please": "volume_down",
	} {
		m, ok := ControlNodeMatch(said)
		if !ok || m.Args["action"] != want {
			t.Errorf("%q: %+v %v, want %s", said, m, ok, want)
		}
	}
	for _, said := range []string{"Speak louder, grandma can't hear you.", "Can you be quieter, the baby's asleep?",
		"Turn it down a notch, you're yelling.", "the music got louder"} {
		if m, ok := ControlNodeMatch(said); ok {
			t.Errorf("%q matched %+v", said, m)
		}
	}
}
