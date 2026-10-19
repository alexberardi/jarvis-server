package main

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/island"
)

// computerCmd is `jarvis-agent computer stop|status`: stop what Jarvis is doing with this
// computer (a computer task, a recording), as the status icon's "Stop Jarvis" does, or say
// what it is doing. Both ask the running agent over its control socket.
func computerCmd(args []string, w io.Writer, dial control.Dialer) error {
	if len(args) != 1 || (args[0] != "stop" && args[0] != "status") {
		return errors.New("usage: jarvis-agent computer stop|status")
	}
	cmd := control.ComputerStatus
	if args[0] == "stop" {
		cmd = control.ComputerStop
	}
	r, err := control.Ask(dial, cmd)
	if err != nil {
		return err
	}
	switch {
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
