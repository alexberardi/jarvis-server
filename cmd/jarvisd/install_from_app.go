package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/qrterm"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
	"github.com/alexberardi/jarvis-server/internal/update"
)

// `jarvisd install-from-app` (01-no-cli-plan item 1.10) is the one privileged call every GUI
// installer makes, so the installers stay thin (IQ3: the logic is Go, tested on the CI
// runners):
//
//	macOS app   do shell script "<bundle>/jarvisd install-from-app --run-as <user>"
//	            with administrator privileges; the app opens the URL it prints
//	Windows     Inno Setup [Run] "{app}\jarvisd.exe" install-from-app --url-file <tmp file>
//	            [--no-firewall]; the Finish page opens the URL from the file
//	.deb        postinst: /usr/lib/jarvisd/jarvisd install-from-app --qr --allow-no-service
//
// It puts this binary where the updater expects it (/usr/local/bin, %ProgramFiles%\jarvisd),
// then has *that* binary run `service install` + `doctor --fix` + `setup-link --url`. It is
// idempotent: run again it changes nothing and prints the same link.

// appInstallOptions are install-from-app's flags.
type appInstallOptions struct {
	runAs          string // macOS: the account the LaunchDaemon runs as
	bin            string // where the binary goes (default: per OS)
	noFirewall     bool   // skip doctor --fix (the installer's "let devices on my network reach it" box is off)
	urlFile        string // also write the URL to this file (Inno Setup can't read stdout)
	qr             bool   // print the setup message and a QR code on stderr (the .deb over SSH)
	allowNoService bool   // no service manager (a container): place the binary and stop there
	linkWait       time.Duration
}

func parseInstallFromAppArgs(args []string, stderr io.Writer) (appInstallOptions, error) {
	fs := flag.NewFlagSet("install-from-app", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o appInstallOptions
	fs.StringVar(&o.runAs, "run-as", "", "macOS: the account the LaunchDaemon runs as (default: $SUDO_USER)")
	fs.StringVar(&o.bin, "bin", "", "where to put jarvisd (default: "+defaultAppBinary()+")")
	fs.BoolVar(&o.noFirewall, "no-firewall", false, "don't open the firewall for the local network (doctor --fix)")
	fs.StringVar(&o.urlFile, "url-file", "", "also write the URL to this file")
	fs.BoolVar(&o.qr, "qr", false, "print the setup message and a QR code of the link on stderr")
	fs.BoolVar(&o.allowNoService, "allow-no-service", false, "without a service manager, install the binary and stop there")
	fs.DurationVar(&o.linkWait, "link-wait", time.Minute, "how long to wait for jarvisd to write its setup link")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() > 0 {
		return o, fmt.Errorf("install-from-app: unexpected argument %q", fs.Arg(0))
	}
	if o.bin != "" && !filepath.IsAbs(o.bin) {
		return o, fmt.Errorf("install-from-app: --bin must be an absolute path")
	}
	return o, nil
}

// defaultAppBinary is where the service runs jarvisd from (00-installers §2.0): a directory
// only an administrator can write, which the updater's privileged helper swaps in.
func defaultAppBinary() string {
	if runtime.GOOS == "windows" {
		pf := os.Getenv("ProgramFiles")
		if pf == "" {
			pf = `C:\Program Files`
		}
		return filepath.Join(pf, service.Name, service.Name+".exe")
	}
	return "/usr/local/bin/jarvisd"
}

// runInstallFromApp is `jarvisd install-from-app`. Progress goes to stderr; stdout gets only
// the URL to open (the setup link, or the admin URL once set up).
func runInstallFromApp(ctx context.Context, flagHome string, args []string, stdout, stderr io.Writer) error {
	o, err := parseInstallFromAppArgs(args, stderr)
	if err != nil {
		return err
	}
	if !doctor.Elevated() {
		return errors.New("install-from-app installs a system service: run it as root (sudo) or from an Administrator prompt")
	}
	self, err := selfExe()
	if err != nil {
		return err
	}
	dst := o.bin
	if dst == "" {
		dst = defaultAppBinary()
	}
	if !sameFile(self, dst) {
		// The bundle, package or download copy: put a binary at dst, then let that one finish,
		// so the service definition is the one the running version writes.
		if err := placeBinary(ctx, self, dst, version, binaryVersion, snapshotInstalled, stderr); err != nil {
			return err
		}
		return handOver(ctx, dst, flagHome, args, stdout, stderr)
	}
	return finishAppInstall(ctx, flagHome, dst, o, stdout, stderr)
}

// sameFile reports whether a and b name the same file (b may not exist).
func sameFile(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	sa, err := os.Stat(a)
	if err != nil {
		return false
	}
	sb, err := os.Stat(b)
	return err == nil && os.SameFile(sa, sb)
}

// placeBinary puts src at dst unless dst already holds this version or a newer one (a
// self-updated install, or a .deb whose package copy went stale: the newer binary stays).
// Replacing an older one keeps it as jarvisd.prev and snapshots the databases first, as an
// upgrade does; the service install that follows restarts jarvisd on the new binary.
func placeBinary(ctx context.Context, src, dst, srcVersion string, versionOf func(context.Context, string) (string, error),
	snapshot func(context.Context, string, io.Writer), w io.Writer) error {
	if _, err := os.Stat(dst); err == nil {
		have, err := versionOf(ctx, dst)
		if err != nil {
			return fmt.Errorf("an unusable jarvisd is in the way at %s (%v); remove it and try again", dst, err)
		}
		cur, okCur := update.ParseVersion(have)
		next, okNext := update.ParseVersion(srcVersion)
		switch {
		case have == srcVersion:
			fmt.Fprintf(w, "jarvisd %s is already at %s\n", have, dst)
			return nil
		case okCur && okNext && cur.Compare(next) > 0:
			fmt.Fprintf(w, "keeping jarvisd %s at %s (newer than this installer's %s)\n", have, dst, srcVersion)
			return nil
		}
		snapshot(ctx, have, w)
		p := update.Paths{Exe: dst}
		// A rename works on a running binary everywhere (Windows can't overwrite one).
		if err := os.Rename(dst, p.Prev()); err != nil {
			return fmt.Errorf("keep the old jarvisd as %s: %w", p.Prev(), err)
		}
		fmt.Fprintf(w, "replacing jarvisd %s at %s (kept as %s)\n", have, dst, p.Prev())
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := copyExecutable(src, dst); err != nil {
		return err
	}
	fmt.Fprintf(w, "installed jarvisd %s at %s\n", srcVersion, dst)
	return nil
}

// snapshotInstalled snapshots the installed service's databases before its binary is
// replaced (best effort, as `jarvisd upgrade` does): a newer version may migrate them.
func snapshotInstalled(ctx context.Context, have string, w io.Writer) {
	home := service.InstalledHome()
	if home == "" {
		return
	}
	dbs, _ := update.DatabaseFiles(home)
	for _, db := range dbs {
		if snap, err := update.Snapshot(ctx, home, db, update.Paths{Home: home}.BackupsDir(), have); err != nil {
			fmt.Fprintf(w, "warning: no snapshot of %s: %v\n", db, err)
		} else {
			fmt.Fprintf(w, "snapshot of %s: %s\n", filepath.Base(db), snap)
		}
	}
}

// copyExecutable copies src to dst (mode 0755) through a temporary file beside it.
func copyExecutable(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".new"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o755)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// handOver runs `dst install-from-app` with the same arguments: dst is the binary the service
// will run, and the one that should write its definition.
func handOver(ctx context.Context, dst, flagHome string, args []string, stdout, stderr io.Writer) error {
	argv := []string{"install-from-app"}
	if flagHome != "" {
		argv = append([]string{"--home", flagHome}, argv...)
	}
	cmd := exec.CommandContext(ctx, dst, append(argv, args...)...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s install-from-app: %w", dst, err)
	}
	return nil
}

// finishAppInstall runs in the binary at its final place: service install (waits for health),
// doctor --fix unless declined, then the setup link.
func finishAppInstall(ctx context.Context, flagHome, bin string, o appInstallOptions, stdout, stderr io.Writer) error {
	m, err := service.New(false, stderr)
	if err != nil {
		if o.allowNoService {
			fmt.Fprintf(stderr, "jarvisd is installed at %s, but %v\n", bin, err)
			return nil
		}
		return err
	}
	home := flagHome
	if home != "" {
		if home, err = filepath.Abs(home); err != nil {
			return err
		}
	} else {
		home = m.InstalledHome()
	}
	if m.InstalledHome() == "" {
		// A fresh install: refuse ports another program holds, before registering anything
		// (the doctor's message is what the installer shows).
		if c := portsHeld(ctx); c != nil {
			msg := c.Detail
			if c.Fix != "" {
				msg += "\n" + c.Fix
			}
			return errors.New(msg)
		}
	}
	if cur, err := checkService(ctx, m); err == nil && upToDate(ctx, cur, home, o.runAs) {
		// A re-run: this version already runs healthy from an up-to-date definition.
		fmt.Fprintf(stderr, "jarvisd %s is already installed and running\n", version)
	} else {
		opts := service.InstallOptions{Binary: bin, Home: home, RunAs: o.runAs}
		if err := m.Install(ctx, opts); err != nil {
			return err
		}
	}
	st, err := waitHealthy(ctx, m, 2*time.Minute)
	printStatus(stderr, st)
	if err != nil {
		return err
	}
	if err := bootstrap(st.Home, false, stderr); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !o.noFirewall {
		checks := doctor.Run(ctx, doctor.Options{Ports: doctorPorts(cfg), Interfaces: cfg.MDNSInterfaces, Home: cfg.Home})
		if n, err := doctor.Apply(ctx, checks, nil, stderr); err != nil {
			fmt.Fprintf(stderr, "warning: the firewall fix failed (%v); run %s\n", err, elevated("jarvisd doctor --fix"))
		} else if n > 0 {
			fmt.Fprintf(stderr, "opened the firewall for the local network (%d command(s))\n", n)
		}
	}
	url, msg, err := waitSetupLink(ctx, cfg, o.linkWait, 500*time.Millisecond)
	if err != nil {
		return err
	}
	if o.urlFile != "" {
		if err := os.WriteFile(o.urlFile, []byte(url+"\n"), 0o600); err != nil {
			return err
		}
	}
	if o.qr {
		fmt.Fprint(stderr, "\n"+msg+"\nOr scan this with a phone on the same network:\n\n")
		_ = qrterm.Write(stderr, url, qrStyleFor(os.Stderr))
	}
	fmt.Fprintln(stdout, url)
	return nil
}

// upToDate reports whether the installed service needs nothing from a re-run: running, healthy,
// its definition current, on the home asked for, and answering as this version (so a binary
// replaced a moment ago still gets its restart), as the account asked for (--run-as, macOS).
func upToDate(ctx context.Context, st serviceStatus, home, runAs string) bool {
	if !st.Installed || !st.Running || !st.Healthy || st.Stale || (runAs != "" && runAs != st.Account) || (home != "" && home != st.Home) {
		return false
	}
	cfg, err := config.Load() // checkService bootstrapped the installed home
	if err != nil {
		return false
	}
	host := cfg.Host
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return runningVersion(ctx, "http://"+net.JoinHostPort(host, strconv.Itoa(cfg.Ports[config.ListenerAdmin]))+"/health") == version
}

// runningVersion is the version the admin listener's /health reports, "" when it can't say.
func runningVersion(ctx context.Context, url string) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var h struct {
		Version string `json:"version"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&h) != nil {
		return ""
	}
	return h.Version
}

// portsHeld is the doctor's failed "ports" check with the default (or environment's) ports,
// nil when jarvisd's ports are free or answer as jarvisd.
func portsHeld(ctx context.Context) *doctor.Check {
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Config{Ports: config.DefaultPorts}
	}
	for _, c := range doctor.Run(ctx, doctor.Options{Ports: doctorPorts(cfg), Interfaces: cfg.MDNSInterfaces}) {
		if c.Name == "ports" && c.Status == doctor.Fail {
			return &c
		}
	}
	return nil
}

// waitSetupLink waits for the URL the installer should open: the setup link once the new
// service has written its token (it can answer /health a moment before), or the admin URL when
// an admin account already exists (a reinstall). After wait it settles for whatever
// setup-link says then.
func waitSetupLink(ctx context.Context, cfg config.Config, wait, every time.Duration) (string, string, error) {
	deadline := time.Now().Add(wait)
	for {
		if b, err := os.ReadFile(authmod.SetupTokenPath(cfg.Home)); err == nil && strings.TrimSpace(string(b)) != "" {
			return setupLinkMessage(cfg)
		}
		if adminExists(cfg.DBPath()) || !time.Now().Before(deadline) {
			return setupLinkMessage(cfg)
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(every):
		}
	}
}
