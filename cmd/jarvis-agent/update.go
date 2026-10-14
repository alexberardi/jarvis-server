package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/control"
	"github.com/alexberardi/jarvis-server/internal/agent/install"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/selfupdate"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// Self-update (01-no-cli-plan 1.7, DESIGN §13 "Self-update"): the agent follows its jarvisd's
// version. Environment, for tests and CI only; neither can weaken the signature check:
//
//	JARVIS_AGENT_RELEASE_BASE   a flat release directory (URL or path) instead of GitHub
//	JARVIS_UPDATE_API           another GitHub API base (as jarvisd's)
//	JARVIS_AGENT_UPDATE_GATE    how long a new version has to come up healthy (default 2m)
const (
	envReleaseBase = "JARVIS_AGENT_RELEASE_BASE"
	envUpdateAPI   = "JARVIS_UPDATE_API"
	envUpdateGate  = "JARVIS_AGENT_UPDATE_GATE"
)

// crashForTest, set with -ldflags "-X main.crashForTest=1", makes `run` fail at once: the
// agent-upgrade e2e's release that never comes up (as jarvisd's own crashForTest).
var crashForTest string

// newUpdater is the self-update for this agent: its executable (and macOS app), its version,
// the release source, the keys built into this binary.
func newUpdater(stateDir string, log *slog.Logger) (*selfupdate.Updater, error) {
	exe, err := agentExe()
	if err != nil {
		return nil, err
	}
	p := selfupdate.Paths{Dir: stateDir}
	if stateDir == "" {
		if p, err = selfupdate.DefaultPaths(); err != nil {
			return nil, err
		}
	}
	t := selfupdate.Target{GOOS: runtime.GOOS, Exe: exe}
	if runtime.GOOS == "darwin" {
		t.Bundle = install.BundleOf(exe)
	}
	return &selfupdate.Updater{
		Paths: p, Target: t, Current: version, Log: log,
		Source: update.Source{Base: os.Getenv(envReleaseBase), APIBase: os.Getenv(envUpdateAPI), UserAgent: "jarvis-agent/" + version},
	}, nil
}

// updateGate is how long `update --finish` waits for the new version.
func updateGate() time.Duration {
	if d, err := time.ParseDuration(os.Getenv(envUpdateGate)); err == nil && d > 0 {
		return d
	}
	return selfupdate.DefaultGate
}

// finishAgent is the running agent as `update --finish` drives it.
func finishAgent(ctx context.Context, u *selfupdate.Updater, w io.Writer) (selfupdate.Agent, error) {
	e, err := installEnv()
	if err != nil {
		return selfupdate.Agent{}, err
	}
	s, err := installSys(w)
	if err != nil {
		return selfupdate.Agent{}, err
	}
	s.Announce = nil
	return selfupdate.Agent{
		Running: control.Send(controlDial, control.Ping) == nil,
		Restart: func(ctx context.Context) error {
			if u.Target.Bundle != "" {
				// The new app's URL scheme and name, for LaunchServices (best effort).
				_ = exec.CommandContext(ctx, "/System/Library/Frameworks/CoreServices.framework/Frameworks/LaunchServices.framework/Support/lsregister",
					"-f", u.Target.Bundle).Run()
			}
			return install.Restart(ctx, e, s)
		},
		Health: func() (string, error) {
			r, err := control.Ask(controlDial, control.Health)
			return r.Version, err
		},
		Pause:    func() { time.Sleep(selfupdate.GatePoll) },
		Attempts: int(updateGate() / selfupdate.GatePoll),
		CheckInstalled: func(tag string) error {
			return update.CheckProgramVersion(ctx, u.Target.Exe, tag, update.AgentProgram)
		},
	}, nil
}

func updateCmd(ctx context.Context, args []string, w io.Writer) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(w)
	check := fs.Bool("check", false, "only say whether an update is available")
	target := fs.String("version", "", "install this release (default: the version of the jarvisd this computer is signed in to)")
	force := fs.Bool("force", false, "let a development build update itself to a release")
	finish := fs.Bool("finish", false, "internal: install a staged update and restart the agent")
	stateDir := fs.String("state-dir", "", "internal: the update's state directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	log := logger()
	u, err := newUpdater(*stateDir, log)
	if err != nil {
		return err
	}
	if *finish {
		return finishCmd(ctx, u, w)
	}
	server := ""
	if *target == "" || *check {
		if server, err = serverVersion(ctx); err != nil && *target == "" {
			return fmt.Errorf("%w; name a release with --version", err)
		}
	}
	if *target == "" {
		*target = server
	}
	if *check {
		return printCheck(w, u, server, *target, *force)
	}
	if !selfupdate.IsRelease(*target) {
		return fmt.Errorf("the server's version %q is not a release: nothing to follow (--version names one)", *target)
	}
	u.Progress = cliProgress(w)
	m, err := u.Stage(ctx, selfupdate.StageOptions{Target: *target, By: selfupdate.ByCLI, Force: *force})
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "Verified %s (signed SHA256SUMS, checksum, `version`). Installing...\n", m.Asset)
	return finishCmd(ctx, u, w)
}

// finishCmd is `update --finish`: swap, restart, wait, roll back if needed.
func finishCmd(ctx context.Context, u *selfupdate.Updater, w io.Writer) error {
	a, err := finishAgent(ctx, u, w)
	if err != nil {
		return err
	}
	r := u.Finish(ctx, a)
	switch r.Outcome {
	case selfupdate.OutcomeSucceeded:
		if a.Running {
			fmt.Fprintf(w, "Updated to %s; the agent is running it.\n", r.To)
		} else {
			fmt.Fprintf(w, "Updated to %s. It starts at the next login, or now with `jarvis-agent start`.\n", r.To)
		}
		return nil
	case selfupdate.OutcomeRolledBack:
		return fmt.Errorf("the update to %s was rolled back to %s: %s", r.To, r.From, r.Reason)
	}
	return fmt.Errorf("not updated: %s", r.Reason)
}

func printCheck(w io.Writer, u *selfupdate.Updater, server, target string, force bool) error {
	fmt.Fprintf(w, "jarvis-agent: %s\n", u.Current)
	if server != "" {
		fmt.Fprintf(w, "jarvisd:      %s\n", server)
	}
	switch err := u.Allowed(target, force); {
	case target == "":
		fmt.Fprintln(w, "update:       unknown (jarvisd unreachable; --version names a release)")
	case err == nil:
		fmt.Fprintf(w, "update:       %s is available (jarvis-agent update installs it)\n", target)
	case errors.Is(err, selfupdate.ErrNotNewer):
		fmt.Fprintln(w, "update:       up to date")
	default:
		fmt.Fprintf(w, "update:       %v\n", err)
	}
	if s, ok := autoUpdateSetting(); ok && !s {
		fmt.Fprintln(w, "automatic:    off (agent.json auto_update)")
	}
	return nil
}

// autoUpdateSetting is agent.json's auto_update, when set.
func autoUpdateSetting() (bool, bool) {
	p, err := settings.DefaultPath()
	if err != nil {
		return false, false
	}
	s, _, err := settings.Load(p)
	if err != nil || s.AutoUpdate == nil {
		return false, false
	}
	return *s.AutoUpdate, true
}

// serverVersion asks jarvisd for its version with a heartbeat that takes no work (is_busy).
func serverVersion(ctx context.Context) (string, error) {
	path, err := credsPath("")
	if err != nil {
		return "", err
	}
	c, err := node.LoadCreds(path)
	if err != nil {
		return "", errors.New("this computer isn't signed in to Jarvis Automation, so there is no server to follow")
	}
	hc := &http.Client{Timeout: 15 * time.Second}
	svcs, err := node.Discover(ctx, hc, c.ConfigURL)
	if err != nil {
		return "", err
	}
	ccURL, err := svcs.Need(node.SvcCommandCenter)
	if err != nil {
		return "", err
	}
	r, err := node.NewCC(ccURL, c).Beat(ctx, node.Beat{Version: version, InstallMode: installMode, NodeKind: nodeKind, Busy: true})
	if err != nil {
		return "", err
	}
	if r.ServerVersion == "" {
		return "", errors.New("jarvisd doesn't say its version (it predates agent self-update)")
	}
	return r.ServerVersion, nil
}

func cliProgress(w io.Writer) func(string, int64) {
	last := ""
	return func(step string, done int64) {
		if step != last {
			fmt.Fprintf(w, "%s...\n", strings.ToUpper(step[:1])+step[1:])
			last = step
		}
	}
}

// --- the running agent's side ---

// startUpdate is the Checker's Start: stage in this process, then hand over to `update --finish`
// in a process the supervisor won't stop with this one.
func startUpdate(u *selfupdate.Updater, status func(string), log *slog.Logger) func(context.Context, selfupdate.StageOptions) error {
	return func(ctx context.Context, o selfupdate.StageOptions) error {
		status("Downloading " + o.Target + "…")
		m, err := u.Stage(ctx, o)
		if err != nil {
			return err
		}
		status("Update available (" + m.To + "), restarting…")
		if err := handOver(u, log); err != nil {
			_ = os.Remove(u.Paths.MarkerPath())
			return fmt.Errorf("couldn't start the installer: %w", err)
		}
		return nil
	}
}

// handOver starts `update --finish` outside this agent's lifetime: under systemd a transient
// user unit (a child in the agent's own unit would be stopped with it when the unit restarts),
// otherwise a detached process (its own session; launchd stops only the job's process group).
func handOver(u *selfupdate.Updater, log *slog.Logger) error {
	args := []string{u.Target.Exe, "update", "--finish", "--state-dir", u.Paths.Dir}
	if runtime.GOOS == "linux" && os.Getenv("INVOCATION_ID") != "" {
		if sr, err := exec.LookPath("systemd-run"); err == nil {
			cmd := []string{"--user", "--collect", "--quiet", "--unit=jarvis-agent-update-" + strconv.FormatInt(time.Now().Unix(), 10)}
			for _, kv := range forwardEnv() {
				cmd = append(cmd, "--setenv="+kv)
			}
			out, err := exec.Command(sr, append(append(cmd, "--"), args...)...).CombinedOutput()
			if err == nil {
				log.Info("update: handed over to a transient systemd unit")
				return nil
			}
			log.Warn("update: systemd-run failed; starting the installer detached", "err", err, "out", strings.TrimSpace(string(out)))
		}
	}
	return install.NativeSys(nil, u.Paths.LogPath()).Spawn(args...)
}

// forwardEnv is what `update --finish` needs from this environment in a transient unit: the
// session (to restart the agent outside systemd if it must) and the agent's own overrides.
func forwardEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(k, "XDG_"), strings.HasPrefix(k, "JARVIS_"),
			k == "DISPLAY", k == "WAYLAND_DISPLAY", k == "DBUS_SESSION_BUS_ADDRESS", k == "HYPRLAND_INSTANCE_SIGNATURE",
			k == "SWAYSOCK", k == "PATH", k == "HOME":
			out = append(out, kv)
		}
	}
	return out
}

// followServer sets up the running agent's self-update: it reads what the last update left
// (the status line, a failure to report), and acts on each heartbeat answer (an update request,
// the server's version) and daily, on its own goroutine until ctx ends.
func followServer(ctx context.Context, r *node.Runner, auto bool, status func(string), log *slog.Logger) {
	u, err := newUpdater("", log)
	if err != nil {
		log.Info("update: self-update off", "reason", err)
		return
	}
	ck := &selfupdate.Checker{Current: version, Auto: func() bool { return auto }, Status: status, Log: log,
		Start: startUpdate(u, status, log)}
	ck.Done = func() {
		if res, err := selfupdate.ReadResult(u.Paths); err == nil && res != nil {
			res.Reported = true
			_ = selfupdate.WriteResult(u.Paths, res)
		}
	}
	m, _ := selfupdate.ReadMarker(u.Paths)
	res, _ := selfupdate.ReadResult(u.Paths)
	ck.Startup(m, res)
	if !selfupdate.IsRelease(version) {
		log.Info("update: a development build never updates itself (jarvis-agent update --force does)", "version", version)
	}
	r.OnBeat = func(cc *node.CC, b node.BeatReply) {
		var p *selfupdate.Pending
		if b.PendingUpdate != nil {
			p = &selfupdate.Pending{TaskID: b.PendingUpdate.TaskID, Target: b.PendingUpdate.TargetVersion}
		}
		ck.OnBeat(b.ServerVersion, p, cc.ReportUpdateFailed)
	}
	go ck.Run(ctx)
}
