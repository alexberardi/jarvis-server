package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
)

// kept is what `run --keep` left running, in <run>/kept.json: enough to use it by hand (piece 6
// checks the extension in the suite browser) and for `ccsuite down --run DIR` to take it away.
type kept struct {
	Root        string      `json:"root"`
	Sandbox     string      `json:"sandbox,omitempty"`
	Sites       string      `json:"sites,omitempty"`
	Session     string      `json:"session,omitempty"` // wlroots, x11
	RuntimeDir  string      `json:"xdg_runtime_dir,omitempty"`
	Env         []string    `json:"env,omitempty"` // the session's display variables
	ControlSock string      `json:"control_sock,omitempty"`
	BrowserSock string      `json:"browser_sock,omitempty"`
	Agent       string      `json:"agent,omitempty"` // the jarvis-agent binary
	Groups      []keptGroup `json:"groups"`          // stopped in this order
}

type keptGroup struct {
	Name string `json:"name"`
	PGID int    `json:"pgid"`
	Argv string `json:"argv0"` // the leader's, so down never signals a group that reused the id
}

// keepAll leaves the run's world running past ccsuite's exit. Every process already has its own
// group and its output in a log file, so only the browser needs help: Chromium quits when its
// DevTools pipe closes, so a holder (in the browser's group) keeps both ends open.
func (e *Env) keepAll() {
	k := kept{Root: e.Root, Sites: e.Sites}
	if e.sandboxUp {
		k.Sandbox = e.Name
	}
	add := func(name string, p *proc) {
		if p != nil && p.alive() {
			k.Groups = append(k.Groups, keptGroup{name, p.cmd.Process.Pid, p.cmd.Args[0]})
		}
	}
	if d := e.desk; d != nil {
		if d.browser != nil && d.browser.alive() && d.cdpW != nil {
			hold := exec.Command("sleep", "infinity")
			hold.ExtraFiles = []*os.File{d.cdpW, d.cdpR}
			joinGroup(hold, d.browser.cmd.Process.Pid)
			if err := hold.Start(); err != nil {
				e.logf("keep: the browser's pipe holder: %v (the browser ends with ccsuite)", err)
			} else {
				hold.Process.Release()
			}
		}
		add("browser", d.browser)
		add("notes", d.notes)
		add("agent", d.agent)
		add("session-"+d.kind, d.session)
		k.Session, k.RuntimeDir = d.kind, d.rt
		for _, v := range d.env {
			for _, pre := range []string{"WAYLAND_DISPLAY=", "SWAYSOCK=", "DISPLAY=", "DBUS_SESSION_BUS_ADDRESS="} {
				if strings.HasPrefix(v, pre) {
					k.Env = append(k.Env, v)
				}
			}
		}
		k.ControlSock, k.BrowserSock = filepath.Join(d.rt, "control.sock"), filepath.Join(d.rt, "browser.sock")
		k.Agent = e.path("bin", "jarvis-agent")
	}
	add("ccsites", e.sitesProc)
	b, _ := json.MarshalIndent(k, "", "  ")
	f := e.path("kept.json")
	if err := os.WriteFile(f, append(b, '\n'), 0o644); err != nil {
		e.logf("keep: %v", err)
	}
	e.logf("kept running (%s):", f)
	if k.Sandbox != "" {
		e.logf("  sandbox      %s (.sandbox/%s/sandbox.env)", k.Sandbox, k.Sandbox)
	}
	if k.Sites != "" {
		e.logf("  sites        %s", k.Sites)
	}
	if k.Session != "" {
		e.logf("  session      %s, XDG_RUNTIME_DIR=%s %s", k.Session, k.RuntimeDir, strings.Join(k.Env, " "))
		e.logf("  agent        control %s, browser %s", k.ControlSock, k.BrowserSock)
		e.logf("  status       HOME=%s %s=%s %s browser status", e.path("agent-home"), browser.SocketEnv, k.BrowserSock, k.Agent)
	}
	for _, g := range k.Groups {
		e.logf("  %-12s process group %d", g.Name, g.PGID)
	}
	e.logf("take it down: go run ./tools/ccsuite down --run %s", e.Dir)
}

// downCmd is `ccsuite down --run DIR`: stops what `run --keep` left running and purges its
// sandbox.
func downCmd(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("ccsuite down", flag.ContinueOnError)
	dir := fs.String("run", "", "the kept run's directory (it has kept.json)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dir == "" || fs.NArg() > 0 {
		return errors.New("usage: ccsuite down --run DIR")
	}
	f := filepath.Join(*dir, "kept.json")
	b, err := os.ReadFile(f)
	if err != nil {
		return err
	}
	var k kept
	if err := json.Unmarshal(b, &k); err != nil {
		return fmt.Errorf("%s: %w", f, err)
	}
	var live []keptGroup
	for _, g := range k.Groups {
		if !groupAlive(g.PGID) {
			continue
		}
		if !leaderIs(g.PGID, g.Argv) {
			fmt.Fprintf(stdout, "ccsuite: %s: process group %d is no longer ours, left alone\n", g.Name, g.PGID)
			continue
		}
		killGroup(g.PGID, false)
		live = append(live, g)
	}
	deadline := time.Now().Add(5 * time.Second)
	for _, g := range live {
		for groupAlive(g.PGID) && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		if groupAlive(g.PGID) {
			killGroup(g.PGID, true)
		}
		fmt.Fprintf(stdout, "ccsuite: %s stopped\n", g.Name)
	}
	if k.RuntimeDir != "" {
		os.RemoveAll(k.RuntimeDir)
	}
	if k.Sandbox != "" {
		purgeSandbox(k.Root, k.Sandbox, stdout)
	}
	return os.Remove(f)
}

// leaderIs tells whether process pid, if it is still there, still runs argv0's program, by name
// (Linux: /proc's command line, as Chromium may retitle itself; macOS: the kernel's command name,
// which it cuts to 16 bytes; elsewhere, or when the leader is gone and only its group is left,
// yes).
func leaderIs(pid int, argv0 string) bool {
	name := filepath.Base(argv0)
	if b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && len(b) > 0 {
		return strings.Contains(string(b), name)
	}
	if comm := procComm(pid); comm != "" {
		return strings.HasPrefix(name, comm)
	}
	return true
}
