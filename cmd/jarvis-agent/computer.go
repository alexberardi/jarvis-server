package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
)

// computerCmd is `jarvis-agent computer stop|status|on|off`: stop what Jarvis is doing with
// this computer (a computer task, a recording), as the status icon's "Stop Jarvis" does, say
// what it is doing, or turn computer control on or off, as its "Computer control" does. All
// ask the running agent over its control socket.
func computerCmd(args []string, w io.Writer, dial control.Dialer) error {
	cmds := map[string]string{"stop": control.ComputerStop, "status": control.ComputerStatus,
		"on": control.ComputerOn, "off": control.ComputerOff}
	cmd := ""
	if len(args) == 1 {
		cmd = cmds[args[0]]
	}
	if cmd == "" {
		return errors.New("usage: jarvis-agent computer stop|status|on|off")
	}
	r, err := control.Ask(dial, cmd)
	if err != nil {
		return err
	}
	switch {
	case cmd == control.ComputerOn:
		fmt.Fprintln(w, "Computer control is on: Jarvis may use this computer.")
	case cmd == control.ComputerOff:
		fmt.Fprintln(w, "Computer control is off: Jarvis won't use this computer.")
	case cmd == control.ComputerStop && r.Answer == "stopped":
		fmt.Fprintln(w, "Stopped.")
	case cmd == control.ComputerStop:
		fmt.Fprintln(w, "Jarvis wasn't using this computer.")
	case r.Answer == "idle" || r.Answer == "":
		fmt.Fprintln(w, "Jarvis isn't using this computer.")
	default:
		// "controlling: Ordering the blue mug" (computer.Activity's String).
		kind, text, _ := strings.Cut(r.Answer, ": ")
		line := map[string]string{string(computer.Controlling): "Jarvis is controlling this computer",
			string(computer.Paused): "Jarvis is paused", string(computer.Recording): "Jarvis is recording"}[kind]
		if line == "" {
			line = r.Answer
		} else if text != "" {
			line += ": " + text
		}
		fmt.Fprintln(w, line)
	}
	return nil
}

// islandActivity is the island's state for an activity.
func islandActivity(k computer.Kind) island.State {
	switch k {
	case computer.Controlling:
		return island.Controlling
	case computer.Paused:
		return island.Paused
	case computer.Recording:
		return island.Recording
	}
	return island.Hidden
}

// saveComputer writes agent.json's computer_control key (path: run's --config, or the default
// file).
func saveComputer(path string, on bool) error {
	path, err := settingsPath(path)
	if err != nil {
		return err
	}
	return settings.Update(path, func(s *settings.Settings) error { s.ComputerControl = &on; return nil })
}
