package main

import (
	"slices"
	"strconv"
	"strings"
)

// regroupEnv marks a `run` that newgrp already started, so it never tries again.
const regroupEnv = "JARVIS_AGENT_NEWGRP"

// shouldRegroup: the user is in the input group (gid, from the group database) but this
// process isn't (its session or systemd user manager started before usermod, and a logout
// doesn't refresh the user manager), and newgrp hasn't been tried. Only for a member: for
// anyone else newgrp asks for a group password.
func shouldRegroup(gid string, userGids []string, procGids []int, guard string) bool {
	n, err := strconv.Atoi(gid)
	if guard != "" || err != nil || !slices.Contains(userGids, gid) {
		return false
	}
	return !slices.Contains(procGids, n)
}

// regroupScript is what newgrp's shell runs: this `run` again, under the input group, with
// SHELL as it was (newgrp is given /bin/sh).
func regroupScript(exe string, args []string, shell string, shellSet bool) string {
	var b strings.Builder
	if shellSet {
		b.WriteString("SHELL=" + shQuote(shell) + "; export SHELL; ")
	} else {
		b.WriteString("unset SHELL; ")
	}
	b.WriteString("exec " + shQuote(exe))
	for _, a := range args {
		b.WriteString(" " + shQuote(a))
	}
	return b.String() + "\n"
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
