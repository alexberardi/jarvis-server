package main

import (
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// regroupInput replaces this `run` with one under the input group when the user was added to it
// after this process's session started (`sudo usermod -aG input $USER`, then `jarvis-agent
// restart`: no logout). util-linux newgrp (setuid) makes input the primary gid, which is enough:
// evdev.Access opens the devices. The PID stays, so systemd keeps supervising it. If newgrp is
// missing or doesn't work here, the agent carries on without the group.
func regroupInput(log *slog.Logger) {
	g, err := user.LookupGroup("input")
	if err != nil {
		return
	}
	u, err := user.Current()
	if err != nil {
		return
	}
	ids, err := u.GroupIds()
	if err != nil {
		return
	}
	have, _ := syscall.Getgroups()
	have = append(have, syscall.Getgid())
	if !shouldRegroup(g.Gid, ids, have, os.Getenv(regroupEnv)) {
		return
	}
	newgrp, err := exec.LookPath("newgrp")
	if err != nil {
		log.Warn("in the input group, but this process isn't and newgrp is missing; desktop recording can't read the keyboard", "err", err)
		return
	}
	// A dry run first: a newgrp that starts but can't switch would exit, and systemd would
	// start this again and again.
	env := append(os.Environ(), regroupEnv+"=1", "SHELL=/bin/sh")
	try := exec.Command(newgrp, "input")
	try.Stdin, try.Env = strings.NewReader("id -g\n"), env
	if out, err := try.Output(); err != nil || strings.TrimSpace(string(out)) != g.Gid {
		log.Warn("in the input group, but newgrp input doesn't work here; desktop recording can't read the keyboard", "err", err)
		return
	}
	exe, err := agentExe()
	if err != nil {
		return
	}
	shell, shellSet := os.LookupEnv("SHELL")
	script := regroupScript(exe, os.Args[1:], shell, shellSet)
	r, w, err := os.Pipe()
	if err != nil {
		return
	}
	defer r.Close()
	_, err = w.WriteString(script) // a few hundred bytes: fits the pipe's buffer
	w.Close()
	if err != nil {
		return
	}
	stdin, err := unix.Dup(0)
	if err != nil || unix.Dup2(int(r.Fd()), 0) != nil {
		return
	}
	log.Info("joining the input group through newgrp (desktop recording reads the keyboard)")
	err = syscall.Exec(newgrp, []string{"newgrp", "input"}, env)
	_ = unix.Dup2(stdin, 0) // exec failed: stdin back
	unix.Close(stdin)
	log.Warn("newgrp input failed; desktop recording can't read the keyboard", "err", err)
}
