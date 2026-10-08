package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// EnvUpdateAPI replaces the GitHub API base for `jarvisd upgrade` and the admin's apply (the
// CI upgrade job serves fake releases). It can't weaken anything: the signature is still
// checked against the keys built into the binary.
const EnvUpdateAPI = "JARVIS_UPDATE_API"

// EnvReleaseBase points `jarvisd upgrade` at a flat release directory (a URL or a local path
// holding SHA256SUMS, SHA256SUMS.minisig and the archives) instead of the GitHub API. The install
// scripts pass their --base-url through it when a re-run hands the upgrade over. Like
// EnvUpdateAPI it can't weaken anything: the signature is checked with the built-in keys.
const EnvReleaseBase = "JARVISD_RELEASE_BASE"

// EnvGateTimeout bounds the post-upgrade health gate (a Go duration; default 2 minutes).
const EnvGateTimeout = "JARVIS_UPGRADE_GATE_TIMEOUT"

func updateSource() update.Source {
	return update.Source{APIBase: os.Getenv(EnvUpdateAPI), UserAgent: "jarvisd/" + version}
}

// selfExe is this executable with symlinks resolved: the path the service definition runs.
func selfExe() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if exe, err = filepath.Abs(exe); err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	return exe, nil
}

// runUpgrade is `jarvisd upgrade` (AD5, ID10): install a signed release with rollback.
func runUpgrade(ctx context.Context, flagHome string, args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	fs.SetOutput(stdout)
	target := fs.String("version", "", "install this release tag (default: the newest)")
	check := fs.Bool("check", false, "only report whether a newer release exists")
	allowOlder := fs.Bool("allow-older", false, "allow --version to name an older (or the same) release")
	bin := fs.String("bin", "", "the jarvisd binary to replace (default: this one)")
	user := fs.Bool("user", false, "Linux: the service is a systemd --user unit")
	rollback := fs.Bool("rollback", false, "roll back the last upgrade (restore jarvisd.prev; also the database snapshot when the upgrade has not passed its health check yet and migrations ran)")
	prestart := fs.Bool("prestart", false, "internal: the service's privileged pre-start step")
	verifyDir := fs.String("verify-dir", "", "check a release directory (SHA256SUMS, its signature, the archives) against this build's keys and version, then exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *verifyDir != "" {
		keys, err := update.TrustedKeys()
		if err != nil {
			return err
		}
		names, err := update.VerifyDir(*verifyDir, version, keys)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s signature ok (%s); verified %s\n", update.SumsName, version, strings.Join(names, ", "))
		return nil
	}
	if err := bootstrap(flagHome, false, os.Stderr); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	exe := *bin
	if exe == "" {
		if exe, err = selfExe(); err != nil {
			return err
		}
	} else if exe, err = filepath.Abs(exe); err != nil {
		return err
	}
	paths := update.Paths{Home: cfg.Home, Exe: exe}

	if *prestart {
		// Never fails the start: report and let serve run.
		action, m, err := update.PreStart(ctx, paths, version)
		switch {
		case err != nil:
			fmt.Fprintln(os.Stderr, "jarvisd upgrade --prestart:", err)
		case action == "swapped":
			fmt.Fprintf(os.Stderr, "jarvisd upgrade --prestart: installed %s (was %s)\n", m.To, m.From)
		case action == "rolled_back":
			fmt.Fprintf(os.Stderr, "jarvisd upgrade --prestart: rolled back %s to %s\n", m.To, m.From)
		}
		return nil
	}

	current := version
	if self, _ := selfExe(); filepath.Clean(self) != filepath.Clean(exe) {
		if current, err = binaryVersion(ctx, exe); err != nil {
			return err
		}
	}
	src := updateSource()
	src.Base = os.Getenv(EnvReleaseBase)

	if *check {
		plan, err := update.Resolve(ctx, update.StageOptions{Current: current, Target: *target, AllowOlder: true, Source: src})
		if err != nil {
			if errors.Is(err, update.ErrUpToDate) {
				fmt.Fprintf(stdout, "jarvisd %s is up to date\n", current)
				return nil
			}
			return err
		}
		fmt.Fprintf(stdout, "jarvisd %s is installed; %s is available (%s)\n", current, plan.Release.Tag, plan.Archive.Name)
		return nil
	}

	mgr, st := installedService(ctx, *user)

	if *rollback {
		fwOK := runtime.GOOS == "darwin" && len(firewallFixes(ctx, cfg)) == 0
		const reason = "rolled back by hand (jarvisd upgrade --rollback)"
		var back string // the version being restored, for the closing line
		if m, err := update.ReadMarker(paths); err != nil {
			return err
		} else if m == nil {
			// The upgrade already passed its gate: put the previous binary back only.
			back, _ = binaryVersion(ctx, paths.Prev())
			if _, err := update.RestorePrevious(paths, back, current, reason); err != nil {
				return err
			}
			fmt.Fprint(stdout, restoredNote(paths.Prev(), paths.BackupsDir()))
		} else {
			res, err := update.Rollback(ctx, paths, reason)
			if err != nil {
				return err
			}
			back = res.From
			fmt.Fprintf(stdout, "rolled back %s to %s (database restored: %v)\n", res.To, res.From, res.DBRestored)
		}
		if _, err := os.Stat(paths.RolledBack()); err == nil {
			fmt.Fprintf(stdout, "the binary rolled back from is kept as %s\n", paths.RolledBack())
		}
		if mgr == nil || !st.Installed {
			fmt.Fprintln(stdout, "start jarvisd again to run it")
			return nil
		}
		fmt.Fprintln(stdout, "restarting the service; waiting for it to answer its health check")
		err := mgr.Restart(ctx)
		refirewall(ctx, cfg, fwOK, stdout)
		if err != nil {
			return err
		}
		err = reportHealthy(ctx, mgr, gateTimeout(), back, logHint(st.Kind, paths.Home), stdout)
		if err == nil {
			doctorAfter(ctx, exe, stdout)
		}
		return endWithAdmin(cfg, err, stdout)
	}

	if !update.CanWrite(exe) {
		return fmt.Errorf("can't replace %s: no write access to %s; run %s", exe, filepath.Dir(exe), elevated("jarvisd upgrade"))
	}
	if st.Installed && st.Home != "" && filepath.Clean(st.Home) != filepath.Clean(cfg.Home) {
		return fmt.Errorf("the installed service uses --home %s, not %s; pass --home %s", st.Home, cfg.Home, st.Home)
	}
	supervised := st.Installed && st.Running
	if !supervised && probeHealth(ctx, healthURL(cfg.Home)) == "" {
		// Unsupervised and running: stop before swapping (nothing would restart it, and the
		// health gate needs the next start).
		return errors.New("jarvisd is running outside a service manager; stop it first (Ctrl-C), then run `jarvisd upgrade` again")
	}

	m, err := update.Stage(ctx, update.StageOptions{
		Paths: paths, Current: current, Target: *target, AllowOlder: *allowOlder, Source: src, By: "cli",
		Progress: progressPrinter(stdout),
	})
	if err != nil {
		if errors.Is(err, update.ErrUpToDate) {
			fmt.Fprintln(stdout, err)
			return nil
		}
		return err
	}
	for f, s := range m.Snapshots {
		fmt.Fprintf(stdout, "snapshot of %s: %s\n", filepath.Base(f), s)
	}
	fwOK := runtime.GOOS == "darwin" && len(firewallFixes(ctx, cfg)) == 0
	fmt.Fprintf(stdout, "installing %s over %s (previous kept as %s)\n", m.To, exe, paths.Prev())
	if _, err := update.Swap(ctx, paths, update.SwapOptions{}); err != nil {
		_ = update.Abort(paths, err.Error())
		return err
	}
	if !st.Installed {
		fmt.Fprintf(stdout, "installed %s. Start jarvisd (jarvisd serve); if %s doesn't come up healthy it rolls back by itself.\n", m.To, m.To)
		refirewall(ctx, cfg, fwOK, stdout)
		return nil
	}
	err = restartAfter(ctx, mgr, st, paths, stdout)
	refirewall(ctx, cfg, fwOK, stdout) // after a rollback too: jarvisd.prev was copied, not renamed
	if err == nil {
		doctorAfter(ctx, exe, stdout)
	}
	return endWithAdmin(cfg, err, stdout)
}

// restoredNote is what a manual rollback after a passed gate says about the database, which it
// leaves alone. A10d V3: it used to say "if the newer version migrated it, jarvisd refuses to
// start", but rc3 → rc2 started fine although rc3 had migrated the database: the guard only
// checks modules the restored version has, so a module it lacks (recipes) keeps its tables
// and data for the next upgrade.
func restoredNote(prev, backups string) string {
	return fmt.Sprintf("restored %s; the database is unchanged. Tables of modules this version doesn't have stay "+
		"for the next upgrade; if the newer version migrated a module it does have, jarvisd refuses to start "+
		"and names it: then restore a snapshot from %s\n", prev, backups)
}

// doctorAfter runs the doctor of the binary now installed at exe and prints its failed
// checks. It runs that binary rather than checking in process because the ports to check are
// the new version's: on a script upgrade this process is the old one (install.sh execs the
// installed jarvisd). A10d V1: rc3 added the recipes listener (7030) and the upgrade said "up
// and healthy" while ufw still dropped 7030 from the LAN. A doctor that can't run or answer
// prints nothing; the upgrade itself is done.
func doctorAfter(ctx context.Context, exe string, stdout io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, exe, "doctor", "--json").Output() // exits 1 on a failed check
	reportDoctor(out, stdout)
}

// reportDoctor prints the failed checks of `jarvisd doctor --json` output, in the doctor's own
// format, and how to apply a firewall fix.
func reportDoctor(out []byte, stdout io.Writer) {
	var checks []doctor.Check
	if err := json.Unmarshal(out, &checks); err != nil {
		return
	}
	var fixable bool
	header := false
	for _, c := range checks {
		if c.Status != doctor.Fail {
			continue
		}
		if !header {
			fmt.Fprintln(stdout, "jarvisd doctor found problems:")
			header = true
		}
		fmt.Fprintf(stdout, "FAIL  %s: %s\n", c.Name, c.Detail)
		if c.Fix != "" {
			fmt.Fprintf(stdout, "      fix:\n        %s\n", strings.ReplaceAll(c.Fix, "\n", "\n        "))
		}
		fixable = fixable || len(c.FixCmds) > 0
	}
	if fixable {
		fmt.Fprintf(stdout, "%s applies the firewall fix\n", elevated("jarvisd doctor --fix"))
	}
}

// endWithAdmin closes a successful upgrade or rollback of the installed service the way a
// fresh install ends (A10c U6): with `jarvisd setup-link`'s line, the admin URL (or the setup
// link while no admin account exists). install.sh execs `jarvisd upgrade` for an upgrade,
// so this is that script's last word too. err (the upgrade failed) is returned unchanged.
func endWithAdmin(cfg config.Config, err error, stdout io.Writer) error {
	if err != nil {
		return err
	}
	_ = printSetupLink(cfg, stdout) // only an unreadable token file; the upgrade itself is done
	return nil
}

// firewallFixes are the doctor's firewall checks that have a fix to run, on macOS only: every
// build is ad-hoc signed with a different signature and socketfilterfw keys its allow entry on
// it, so a swapped binary may be blocked although its path is listed. Other firewalls match
// ports or the program path, which a swap doesn't change.
func firewallFixes(ctx context.Context, cfg config.Config) []doctor.Check {
	if runtime.GOOS != "darwin" {
		return nil
	}
	var fixes []doctor.Check
	for _, c := range doctor.Run(ctx, doctor.Options{Ports: doctorPorts(cfg), Interfaces: cfg.MDNSInterfaces, Home: cfg.Home}) {
		if len(c.FixCmds) > 0 {
			fixes = append(fixes, c)
		}
	}
	return fixes
}

// refirewall re-admits the swapped binary to the macOS application firewall when the binary it
// replaced was admitted (wasOK: no firewall fix pending before the swap; a fix the operator
// declined at install stays declined). With root it runs the doctor's firewall fix; otherwise
// it prints the command.
func refirewall(ctx context.Context, cfg config.Config, wasOK bool, stdout io.Writer) {
	if !wasOK {
		return
	}
	fixes := firewallFixes(ctx, cfg)
	if len(fixes) == 0 {
		return
	}
	if !doctor.Elevated() {
		fmt.Fprintln(stdout, "the firewall no longer admits the new binary; to let nodes and phones reach it, run:")
		for _, c := range fixes {
			fmt.Fprintf(stdout, "  %s\n", strings.ReplaceAll(c.Fix, "\n", "\n  "))
		}
		return
	}
	fmt.Fprintln(stdout, "re-admitting the new binary to the application firewall")
	if _, err := doctor.Apply(ctx, fixes, nil, stdout); err != nil {
		fmt.Fprintf(stdout, "the firewall fix failed (%v); run `sudo jarvisd doctor --fix`\n", err)
	}
}

// restartAfter restarts the installed service and waits for the upgrade's outcome. If the
// new version never reports one (it can't even start far enough to count its attempts), the
// CLI rolls back itself.
func restartAfter(ctx context.Context, mgr service.Manager, st service.Status, p update.Paths, stdout io.Writer) error {
	if mgr == nil || !st.Installed {
		fmt.Fprintln(stdout, "restart jarvisd to run it")
		return nil
	}
	fmt.Fprintln(stdout, "restarting the service; waiting for the new version to pass its health check")
	if err := mgr.Restart(ctx); err != nil {
		// A new version that fails its first start makes the restart itself fail (systemd
		// waits for READY); the supervisor keeps retrying and the start count rolls it back.
		fmt.Fprintln(stdout, "the first start failed; waiting for the retries or the rollback:", err)
	}
	wait := update.MaxFailedStarts*(gateTimeout()+15*time.Second) + time.Minute
	err := waitOutcome(ctx, p, wait, st.Kind, stdout)
	if !errors.Is(err, errNoOutcome) {
		return err
	}
	fmt.Fprintf(stdout, "no result after %s; rolling back\n", wait)
	if serr := mgr.Stop(ctx); serr != nil {
		fmt.Fprintln(stdout, "stopping the service failed:", serr)
	}
	if rerr := update.RequestRollback(p, "no health result within "+wait.String()); rerr != nil {
		return rerr
	}
	res, rerr := update.Rollback(ctx, p, "")
	if rerr != nil {
		return rerr
	}
	if serr := mgr.Start(ctx); serr != nil {
		return serr
	}
	return fmt.Errorf("%s never became healthy and was rolled back to %s (database restored: %v); see %s",
		res.To, res.From, res.DBRestored, logHint(st.Kind, p.Home))
}

var errNoOutcome = errors.New("no upgrade outcome")

// reportHealthy waits for the restarted service to run and answer /health, and says so; an
// error naming the log when it doesn't within wait. version is what was started ("" unknown).
func reportHealthy(ctx context.Context, mgr service.Manager, wait time.Duration, version, logs string, stdout io.Writer) error {
	name := "jarvisd"
	if version != "" {
		name += " " + version
	}
	if _, err := waitHealthy(ctx, mgr, wait); err != nil {
		return fmt.Errorf("%s didn't come up healthy within %s: %w; see %s", name, wait, err, logs)
	}
	fmt.Fprintf(stdout, "%s is up and healthy\n", name)
	return nil
}

// waitOutcome waits for the restarted service to clear the upgrade marker and reports the
// result (succeeded, rolled back); errNoOutcome when the wait runs out.
func waitOutcome(ctx context.Context, p update.Paths, wait time.Duration, kind service.Kind, stdout io.Writer) error {
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		m, err := update.ReadMarker(p)
		if err == nil && m == nil {
			res, _ := update.ReadResult(p)
			if res == nil {
				return nil
			}
			switch res.Outcome {
			case update.ResultSucceeded:
				fmt.Fprintf(stdout, "jarvisd %s is up and healthy\n", res.To)
				return nil
			case update.ResultRolledBack:
				return fmt.Errorf("%s failed (%s) and was rolled back to %s (database restored: %v); see %s",
					res.To, res.Reason, res.From, res.DBRestored, logHint(kind, p.Home))
			default:
				return fmt.Errorf("upgrade to %s failed: %s", res.To, res.Reason)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errNoOutcome
}

// logHint says where the service's log is.
func logHint(kind service.Kind, home string) string {
	if kind == service.Systemd {
		return "journalctl -u jarvisd"
	}
	return service.LogPath(home)
}

// installedService opens the installed service manager, if any.
func installedService(ctx context.Context, user bool) (service.Manager, service.Status) {
	mgr, err := service.Open(user, io.Discard)
	if err != nil {
		return nil, service.Status{}
	}
	st, err := mgr.Status(ctx)
	if err != nil {
		return nil, service.Status{}
	}
	return mgr, st
}

func binaryVersion(ctx context.Context, exe string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "version").Output()
	if err != nil {
		return "", fmt.Errorf("%s version: %w", exe, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// elevated is cmd run with administrator rights on this OS.
func elevated(cmd string) string {
	if runtime.GOOS == "windows" {
		return "`" + cmd + "` from an elevated (Administrator) PowerShell"
	}
	return "`sudo " + cmd + "`"
}

func gateTimeout() time.Duration {
	if d, err := time.ParseDuration(os.Getenv(EnvGateTimeout)); err == nil && d > 0 {
		return d
	}
	return 2 * time.Minute
}

// --- serve's side: pending file work, the start count and the health gate ---

// upgradeStart runs before serve opens the database. restart means this process must exit
// for the supervisor to start the binary now in place.
func upgradeStart(ctx context.Context, log *slog.Logger, p update.Paths, supervised bool) (gate *update.Marker, restart bool, err error) {
	action, m, perr := update.PreStart(ctx, p, version)
	switch {
	case perr != nil && errors.Is(perr, update.ErrNeedPrivilege):
		log.Warn("upgrade: a pending step needs administrator rights", "err", perr, "fix", elevated("jarvisd upgrade"))
	case perr != nil:
		log.Error("upgrade: pending step failed", "err", perr)
	case action == "swapped" && m.To != version:
		log.Info("upgrade: installed the new binary; restarting into it", "from", m.From, "to", m.To)
		if supervised {
			return nil, true, nil
		}
		log.Warn("upgrade: restart jarvisd to run the new version", "to", m.To)
		return nil, false, nil
	case action == "rolled_back":
		log.Error("upgrade: rolled back", "from", m.To, "to", m.From, "reason", m.Reason)
		if supervised {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("%s was rolled back to %s; start jarvisd again", m.To, m.From)
	}
	gm, err := update.BeginStart(p, version)
	if errors.Is(err, update.ErrRollback) {
		log.Error("upgrade: the new version keeps failing; rolling back", "from", gm.To, "to", gm.From, "attempts", gm.Attempts)
		if update.CanWrite(p.Exe) {
			if _, rerr := update.Rollback(ctx, p, ""); rerr != nil {
				return nil, false, rerr
			}
			if supervised {
				return nil, true, nil
			}
			return nil, false, fmt.Errorf("%s was rolled back to %s; start jarvisd again", gm.To, gm.From)
		}
		if os.Getenv(service.EnvUpgradeHelper) == "1" && supervised {
			return nil, true, nil // the privileged pre-start rolls back
		}
		log.Error("upgrade: can't roll back without administrator rights", "fix", elevated("jarvisd upgrade --rollback"))
		return nil, false, nil
	}
	if err != nil {
		log.Error("upgrade: reading the upgrade state failed", "err", err)
		return nil, false, nil
	}
	if gm != nil {
		log.Info("upgrade: health gate", "from", gm.From, "to", gm.To, "attempt", gm.Attempts, "timeout", gateTimeout())
	}
	return gm, false, nil
}

// healthy reports whether GET /health answers 200 at a bound listener address.
func healthy(ctx context.Context, addr string) bool {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// progressPrinter prints staging progress: each step once, a download every 10% of its size,
// and, when the size is unknown (a server that doesn't say), every 10 MB downloaded so a
// download from any release base shows it is moving (A10 F24).
func progressPrinter(w io.Writer) func(update.Progress) {
	step, mark := "", int64(-1)
	return func(p update.Progress) {
		if p.Step != step {
			step, mark = p.Step, -1
			if p.Total == 0 && p.Done == 0 {
				fmt.Fprintln(w, p.Step)
				mark = 0 // the next line is at 10 MB
				return
			}
		}
		switch {
		case p.Total > 0:
			if pct := min(p.Done*100/p.Total, 100) / 10 * 10; pct > mark {
				mark = pct
				fmt.Fprintf(w, "%s %d%%\n", p.Step, pct)
			}
		case p.Done > 0:
			if mb := p.Done >> 20 / 10 * 10; mb > mark {
				mark = mb
				fmt.Fprintf(w, "%s %d MB\n", p.Step, mb)
			}
		}
	}
}
