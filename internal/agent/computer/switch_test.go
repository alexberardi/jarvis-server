package computer

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestSwitchOffRefusesEveryAction(t *testing.T) {
	s := NewSwitch(false, nil)
	if s.On() {
		t.Fatal("a new switch is on")
	}
	for _, what := range []string{TargetBrowser, TargetDesktop} {
		err := s.Allow(what)
		if !errors.Is(err, ErrOff) {
			t.Fatalf("%s with the switch off: %v, want ErrOff", what, err)
		}
		if !strings.Contains(err.Error(), `"Computer control"`) {
			t.Fatalf("the error doesn't say where to turn it on: %q", err)
		}
	}
	if err := s.Set(true); err != nil {
		t.Fatal(err)
	}
	for _, what := range []string{TargetBrowser, TargetDesktop} {
		if err := s.Allow(what); err != nil {
			t.Fatalf("%s with the switch on: %v", what, err)
		}
	}
}

func TestNilSwitchIsOff(t *testing.T) {
	var s *Switch
	if s.On() || !errors.Is(s.Allow(TargetBrowser), ErrOff) {
		t.Fatal("a nil switch allows computer actions")
	}
}

func TestSwitchSavesAndTellsWatchers(t *testing.T) {
	var saved, seen []bool
	s := NewSwitch(false, nil)
	s.Persist = func(on bool) error { saved = append(saved, on); return nil }
	s.Watch(func(on bool) { seen = append(seen, on) })
	_ = s.Set(true)
	_ = s.Set(false)
	if len(saved) != 2 || !saved[0] || saved[1] {
		t.Fatalf("saved %v", saved)
	}
	if len(seen) != 2 || !seen[0] || seen[1] {
		t.Fatalf("watchers saw %v", seen)
	}
}

func TestSwitchSaveFailureStillSwitches(t *testing.T) {
	s := NewSwitch(false, nil)
	s.Persist = func(bool) error { return errors.New("disk full") }
	if err := s.Set(true); err == nil {
		t.Fatal("the save error is not reported")
	}
	if !s.On() {
		t.Fatal("a failed save left the switch off: it lasts until the agent restarts")
	}
}

func TestTurningOffStopsTheTask(t *testing.T) {
	a := &Activity{}
	s := NewSwitch(true, a)
	stopped := 0
	remove := a.OnStop(func() { stopped++ })
	defer remove()
	a.Set(Controlling, "Ordering the blue mug")
	_ = s.Set(true) // already on: nothing stops
	if stopped != 0 {
		t.Fatal("turning it on stopped the task")
	}
	_ = s.Set(false)
	if stopped != 1 {
		t.Fatalf("turning it off mid-task stopped %d runs, want 1", stopped)
	}
	if k, _ := a.Status(); k != Idle {
		t.Fatalf("activity after off: %q", k)
	}
	if !errors.Is(s.Allow(TargetBrowser), ErrOff) {
		t.Fatal("the task's next action is allowed after off")
	}
}

// computer_switch is jarvisd's "enable/disable computer control" by voice: the menu's own path
// (Set: agent.json, the tray, the heartbeat), answered with the new state.
func TestSwitchTool(t *testing.T) {
	a := &Activity{}
	s := NewSwitch(false, a)
	var saved []bool
	s.Persist = func(on bool) error { saved = append(saved, on); return nil }
	tools := &SwitchTools{S: s}
	if defs := tools.Available(context.Background()); len(defs) != 1 {
		t.Fatalf("%d tools", len(defs))
	}
	out, ok := tools.Call(context.Background(), "computer_switch", map[string]any{"on": true}, node.Request{})
	if !ok || out["success"] != true || out["computer_control"] != "on" || !s.On() {
		t.Fatalf("on: %v", out)
	}
	stopped := 0
	defer a.OnStop(func() { stopped++ })()
	a.Set(Controlling, "Ordering the blue mug")
	out, _ = tools.Call(context.Background(), "computer_switch", map[string]any{"on": false}, node.Request{})
	if out["computer_control"] != "off" || s.On() || stopped != 1 {
		t.Fatalf("off: %v, stopped %d", out, stopped)
	}
	if len(saved) != 2 {
		t.Fatalf("saved %v", saved)
	}
	if out, _ := tools.Call(context.Background(), "computer_switch", map[string]any{}, node.Request{}); out["success"] != false {
		t.Fatalf("no on: %v", out)
	}
	if _, ok := tools.Call(context.Background(), "computer_act", nil, node.Request{}); ok {
		t.Fatal("handled another tool")
	}
}
