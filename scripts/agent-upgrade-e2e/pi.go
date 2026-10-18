package main

// The -pi lane: the agent installed as install-pi.sh lays the Pi node out (under a scratch
// JARVIS_PI_ROOT: /opt/jarvis-agent/jarvis-agent, which the agent's user owns, and the system
// unit's file), supervised by a fake systemd. The fake's tools are this program under their
// names on the agent's PATH, talking to this process over HTTP:
//
//	sudo         allows exactly the sudoers line install-pi.sh writes (-n /usr/bin/systemctl
//	             restart jarvis-agent.service) and refuses anything else, as sudo -n does
//	systemctl    restart jarvis-agent.service, only through that sudo (the agent's user isn't root)
//	systemd-run  --user only: a transient unit with only the --setenv environment
//
// The unit is Restart=always (RestartSec 1 s): a restart stops the agent's process group, the
// way systemd stops the unit's cgroup, and starts `run --profile pi` again.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/install"
)

// envFakeSystemd is the fake systemd's URL in the agent's environment (JARVIS_: forwarded into
// systemd-run's transient unit, as the agent forwards its own overrides).
const envFakeSystemd = "JARVIS_E2E_FAKE_SYSTEMD"

// sudoersLine is what install-pi.sh's sudoers lets the node's user run (install.PiRestart).
var sudoersLine = install.PiRestart[1:]

// fakeTool runs this program as one of the fake systemd's tools when it was started under that
// name; ok is false when it wasn't.
func fakeTool() (code int, ok bool) {
	name := filepath.Base(os.Args[0])
	if name != "sudo" && name != "systemctl" && name != "systemd-run" {
		return 0, false
	}
	body, _ := json.Marshal(toolCall{Tool: name, Args: os.Args[1:]})
	resp, err := http.Post(os.Getenv(envFakeSystemd)+"/call", "application/json", bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "fake %s: %v\n", name, err)
		return 1, true
	}
	defer resp.Body.Close()
	var r toolReply
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		fmt.Fprintf(os.Stderr, "fake %s: %v\n", name, err)
		return 1, true
	}
	fmt.Fprint(os.Stderr, r.Out)
	return r.Code, true
}

type toolCall struct {
	Tool string   `json:"tool"`
	Args []string `json:"args"`
}

type toolReply struct {
	Code int    `json:"code"`
	Out  string `json:"out,omitempty"`
}

type fakeSystemd struct {
	e       *e2e
	url     string
	bin     string // the tools
	journal string // the unit's output
	runLog  string // the transient units' output (`update --finish`)
	srv     *http.Server

	mu      sync.Mutex
	calls   []string
	cur     *exec.Cmd
	done    chan struct{}
	gen     int // the current start; a waiter of an older one doesn't restart anything
	stopped bool
	starts  int
}

func newFakeSystemd(e *e2e) *fakeSystemd {
	self, err := os.Executable()
	must(err)
	s := &fakeSystemd{e: e, bin: filepath.Join(e.work, "fakebin"),
		journal: filepath.Join(e.work, "jarvis-agent.service.log"), runLog: filepath.Join(e.work, "jarvis-agent-update.log")}
	must(os.MkdirAll(s.bin, 0o755))
	for _, t := range []string{"sudo", "systemctl", "systemd-run"} {
		must(os.Symlink(self, filepath.Join(s.bin, t)))
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	must(err)
	s.url = "http://" + ln.Addr().String()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /call", func(w http.ResponseWriter, r *http.Request) {
		var c toolCall
		_ = json.NewDecoder(r.Body).Decode(&c)
		writeJSON(w, s.call(c))
	})
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s
}

func (s *fakeSystemd) record(line string) {
	s.mu.Lock()
	s.calls = append(s.calls, line)
	s.mu.Unlock()
}

// Calls is every tool call so far, as "tool args…" (refused ones end in " REFUSED").
func (s *fakeSystemd) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *fakeSystemd) call(c toolCall) toolReply {
	line := strings.TrimSpace(c.Tool + " " + strings.Join(c.Args, " "))
	switch c.Tool {
	case "sudo":
		if !slices.Equal(c.Args, sudoersLine) {
			s.record(line + " REFUSED")
			return toolReply{Code: 1, Out: "sudo: a password is required\n"}
		}
		s.record(line)
		s.restart()
		return toolReply{}
	case "systemctl":
		// Not root: systemd refuses to manage a system unit (polkit would ask for a password).
		s.record(line + " REFUSED")
		return toolReply{Code: 1, Out: "Failed to " + strings.Join(c.Args, " ") + ": Access denied\n"}
	case "systemd-run":
		return s.run(line, c.Args)
	}
	return toolReply{Code: 1, Out: "unknown tool\n"}
}

// run is systemd-run --user: the command after "--", in its own session (outside the unit),
// with only the --setenv environment.
func (s *fakeSystemd) run(line string, args []string) toolReply {
	i := slices.Index(args, "--")
	if len(args) == 0 || args[0] != "--user" || i < 0 || i == len(args)-1 {
		s.record(line + " REFUSED")
		return toolReply{Code: 1, Out: "fake systemd-run: only --user [options] -- command\n"}
	}
	var env []string
	for _, a := range args[:i] {
		if kv, ok := strings.CutPrefix(a, "--setenv="); ok {
			env = append(env, kv)
		}
	}
	s.record(line)
	out, err := os.OpenFile(s.runLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return toolReply{Code: 1, Out: err.Error() + "\n"}
	}
	defer out.Close()
	c := exec.Command(args[i+1], args[i+2:]...)
	c.Env, c.Stdout, c.Stderr = env, out, out
	detach(c)
	if err := c.Start(); err != nil {
		return toolReply{Code: 1, Out: err.Error() + "\n"}
	}
	go func() { _ = c.Wait() }()
	return toolReply{}
}

// Start starts the unit (systemctl start, at boot).
func (s *fakeSystemd) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.start()
}

// start runs the unit's ExecStart; s.mu is held.
func (s *fakeSystemd) start() {
	out, err := os.OpenFile(s.journal, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	must(err)
	c := exec.Command(s.e.exe, "run", "--profile", "pi")
	c.Env, c.Stdout, c.Stderr = s.e.env, out, out
	detach(c)
	must(c.Start())
	_ = out.Close()
	s.gen++
	s.starts++
	gen, done := s.gen, make(chan struct{})
	s.cur, s.done = c, done
	go func() {
		_ = c.Wait()
		close(done)
		time.Sleep(time.Second) // RestartSec
		s.mu.Lock()
		defer s.mu.Unlock()
		if !s.stopped && s.gen == gen {
			s.start() // Restart=always
		}
	}()
}

// stop stops the running agent's process group and waits (TimeoutStopSec, then a kill); s.mu is
// held.
func (s *fakeSystemd) stop() {
	if s.cur == nil {
		return
	}
	s.gen++
	terminate(s.cur)
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		_ = s.cur.Process.Kill()
		<-s.done
	}
	s.cur = nil
}

func (s *fakeSystemd) restart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stop()
	s.start()
}

// Starts is how many times the unit started the agent.
func (s *fakeSystemd) Starts() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.starts
}

func (s *fakeSystemd) Close() {
	s.mu.Lock()
	s.stopped = true
	s.stop()
	s.mu.Unlock()
	_ = s.srv.Close()
}

// installPi lays the Pi node out under a scratch JARVIS_PI_ROOT, as install-pi.sh does.
func (e *e2e) installPi() {
	root := filepath.Join(e.work, "pi")
	e.exe = filepath.Join(root+install.PiDir, "jarvis-agent")
	must(os.MkdirAll(filepath.Dir(e.exe), 0o755))
	must(copyFile(filepath.Join(e.buildDir(vN), exeName()), e.exe, 0o755))
	unit := root + install.PiUnitFile
	must(os.MkdirAll(filepath.Dir(unit), 0o755))
	must(os.WriteFile(unit, []byte("[Service]\nExecStart="+install.PiDir+"/jarvis-agent run --profile pi\nRestart=always\n"), 0o644))
	e.sd = newFakeSystemd(e)
	e.env = append(e.env, install.PiRootEnv+"="+root, envFakeSystemd+"="+e.sd.url,
		"PATH="+e.sd.bin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"INVOCATION_ID=e2e-pi-unit") // the agent runs under a (system) unit
}

// checkPiCalls checks how the Pi node was restarted: only through sudoers' systemctl restart,
// the installer through systemd-run --user, and nothing refused.
func (e *e2e) checkPiCalls() {
	calls := e.sd.Calls()
	restart := "sudo " + strings.Join(sudoersLine, " ")
	var restarts, runs int
	for _, c := range calls {
		switch {
		case strings.HasSuffix(c, " REFUSED"):
			fail("the fake systemd refused %q (calls: %q)", c, calls)
		case c == restart:
			restarts++
		case strings.HasPrefix(c, "systemd-run --user ") && strings.Contains(c, " -- "+e.exe+" update --finish "):
			runs++
		default:
			fail("unexpected call %q (calls: %q)", c, calls)
		}
	}
	// The update to vNext and the one rolled back: each handed over once. Restarts: the
	// harness's own `jarvis-agent restart` before steps 2, 3 and 4, the installer's into vNext
	// and vBroken, and the rollback's into vNext again.
	if runs != 2 || restarts != 6 {
		fail("%d systemd-run handovers and %d unit restarts (calls: %q)", runs, restarts, calls)
	}
	say("pi: %d unit restarts through sudo, %d handovers through systemd-run --user, %d starts by the unit", restarts, runs, e.sd.Starts())
}
