//go:build unix

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Linux system-service layout (00-installers §2.0).
const (
	SystemHome    = "/var/lib/jarvisd"
	systemUnit    = "/etc/systemd/system/jarvisd.service"
	systemEnvDir  = "/etc/jarvisd"
	unitName      = Name + ".service"
	gpuGroupsHint = "video,render"
)

// systemd manages jarvisd.service as a system unit or, with user set, a --user unit.
type systemd struct {
	user bool
	out  io.Writer
	run  runFunc

	unitPath string // where the unit file lives
	envDir   string // system mode: /etc/jarvisd

	euid        func() int
	lookupUser  func(string) (*user.User, error)
	lookupGroup func(string) (*user.Group, error)
	chown       func(string, int, int) error
	current     func() (*user.User, error)
	getenv      func(string) string
}

func newSystemd(userMode bool, out io.Writer) *systemd {
	s := &systemd{
		user: userMode, out: out, run: execRun,
		unitPath: systemUnit, envDir: systemEnvDir,
		euid: os.Geteuid, lookupUser: user.Lookup, lookupGroup: user.LookupGroup,
		chown: os.Lchown, current: user.Current, getenv: os.Getenv,
	}
	if userMode {
		s.unitPath = userUnitPath(os.Getenv, os.UserHomeDir)
	}
	return s
}

// userUnitPath is $XDG_CONFIG_HOME/systemd/user/jarvisd.service (default ~/.config).
func userUnitPath(getenv func(string) string, home func() (string, error)) string {
	cfg := getenv("XDG_CONFIG_HOME")
	if cfg == "" {
		h, _ := home()
		cfg = filepath.Join(h, ".config")
	}
	return filepath.Join(cfg, "systemd", "user", unitName)
}

func (s *systemd) Kind() Kind { return Systemd }

func (s *systemd) systemctl(ctx context.Context, args ...string) ([]byte, error) {
	if s.user {
		args = append([]string{"--user"}, args...)
	}
	return s.run(ctx, "systemctl", args...)
}

// unsafeSystemPaths are where a system unit's binary must not live: ProtectHome=yes hides
// /home, /root and /run/user, and PrivateTmp=yes replaces /tmp and /var/tmp.
var unsafeSystemPaths = []string{"/home/", "/root/", "/run/user/", "/tmp/", "/var/tmp/"}

func (s *systemd) Install(ctx context.Context, o InstallOptions) error {
	if s.user {
		return s.installUser(ctx, o)
	}
	if s.euid() != 0 {
		return errors.New("a system service needs root: run `sudo jarvisd service install`, or `jarvisd service install --user` for your account only")
	}
	if o.Home == "" {
		o.Home = SystemHome
	}
	for _, p := range unsafeSystemPaths {
		if strings.HasPrefix(o.Binary, p) {
			return fmt.Errorf("the service can't run %s (the unit hides %s); install the binary first, e.g. `sudo install -m 755 %s /usr/local/bin/jarvisd`", o.Binary, strings.TrimSuffix(p, "/"), o.Binary)
		}
	}
	u, err := s.ensureAccount(ctx, o.Home)
	if err != nil {
		return err
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)

	if err := makeHome(o.Home); err != nil {
		return err
	}
	// A reinstall over a home made by another account (or by hand) hands it to jarvisd.
	if err := filepath.WalkDir(o.Home, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return s.chown(p, uid, gid)
	}); err != nil {
		return fmt.Errorf("chown %s: %w", o.Home, err)
	}
	if err := os.MkdirAll(s.envDir, 0o750); err != nil {
		return err
	}
	if err := os.Chmod(s.envDir, 0o750); err != nil {
		return err
	}
	if err := s.chown(s.envDir, 0, gid); err != nil {
		return err
	}
	envFile := filepath.Join(s.envDir, "jarvisd.env")
	if wrote, err := writeEnvTemplate(envFile, 0o640); err != nil {
		return err
	} else if wrote {
		if err := s.chown(envFile, 0, gid); err != nil {
			return err
		}
	}
	unit, err := RenderSystemd(Unit{Binary: o.Binary, Home: o.Home})
	if err != nil {
		return err
	}
	if err := writeFile(s.unitPath, unit, 0o644); err != nil {
		return err
	}
	printf(s.out, "wrote %s (runs %s as %s, data in %s, env file %s)\n", s.unitPath, o.Binary, Name, o.Home, envFile)
	return s.enable(ctx, o.NoStart)
}

// ensureAccount creates the jarvisd system user (with its own group) if missing and adds
// it to the GPU device groups that exist (AMD/Intel and Vulkan need /dev/dri/renderD*).
func (s *systemd) ensureAccount(ctx context.Context, home string) (*user.User, error) {
	if _, err := s.lookupUser(Name); err != nil {
		shell := "/usr/sbin/nologin"
		for _, sh := range []string{"/usr/sbin/nologin", "/sbin/nologin", "/bin/false"} {
			if _, err := os.Stat(sh); err == nil {
				shell = sh
				break
			}
		}
		if _, err := s.run(ctx, "useradd", "--system", "--user-group", "--home-dir", home, "--no-create-home",
			"--shell", shell, "--comment", "Jarvis server", Name); err != nil {
			return nil, err
		}
		printf(s.out, "created system user %s\n", Name)
	}
	var groups []string
	for _, g := range strings.Split(gpuGroupsHint, ",") {
		if _, err := s.lookupGroup(g); err == nil {
			groups = append(groups, g)
		}
	}
	if len(groups) > 0 {
		if _, err := s.run(ctx, "usermod", "--append", "--groups", strings.Join(groups, ","), Name); err != nil {
			return nil, err
		}
	}
	return s.lookupUser(Name)
}

func (s *systemd) installUser(ctx context.Context, o InstallOptions) error {
	if s.euid() == 0 {
		return errors.New("--user installs a service for your own account; run it without sudo")
	}
	if o.Home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		o.Home = filepath.Join(h, ".jarvisd")
	}
	if err := makeHome(o.Home); err != nil {
		return err
	}
	envFile := filepath.Join(o.Home, "jarvisd.env")
	if _, err := writeEnvTemplate(envFile, 0o600); err != nil {
		return err
	}
	unit, err := RenderSystemd(Unit{Binary: o.Binary, Home: o.Home, User: true})
	if err != nil {
		return err
	}
	if err := writeFile(s.unitPath, unit, 0o644); err != nil {
		return err
	}
	printf(s.out, "wrote %s (data in %s, env file %s)\n", s.unitPath, o.Home, envFile)
	if err := s.enable(ctx, o.NoStart); err != nil {
		return err
	}
	// Without lingering, the user manager (and jarvisd) stops at logout and starts at login.
	name := s.getenv("USER")
	if u, err := s.current(); err == nil {
		name = u.Username
	}
	if _, err := s.run(ctx, "loginctl", "enable-linger", name); err != nil {
		printf(s.out, "warning: could not keep jarvisd running while you are logged out (%v).\n"+
			"  Run once: sudo loginctl enable-linger %s\n", err, name)
	}
	return nil
}

func (s *systemd) enable(ctx context.Context, noStart bool) error {
	if _, err := s.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	if _, err := s.systemctl(ctx, "enable", unitName); err != nil {
		return err
	}
	if noStart {
		return nil
	}
	// restart, not start: a reinstall must pick up the new unit and binary.
	_, err := s.systemctl(ctx, "restart", unitName)
	return err
}

func (s *systemd) Uninstall(ctx context.Context) error {
	if _, err := os.Stat(s.unitPath); errors.Is(err, fs.ErrNotExist) {
		return ErrNotInstalled
	}
	home := s.InstalledHome()
	if _, err := s.systemctl(ctx, "disable", "--now", unitName); err != nil {
		printf(s.out, "warning: %v\n", err)
	}
	if err := os.Remove(s.unitPath); err != nil {
		return err
	}
	if _, err := s.systemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	_, _ = s.systemctl(ctx, "reset-failed", unitName)
	printf(s.out, "removed %s; data kept in %s", s.unitPath, home)
	if !s.user {
		printf(s.out, " and %s (the %s account is kept too)", s.envDir, Name)
	}
	printf(s.out, "\n")
	return nil
}

func (s *systemd) Start(ctx context.Context) error   { return s.ctl(ctx, "start") }
func (s *systemd) Stop(ctx context.Context) error    { return s.ctl(ctx, "stop") }
func (s *systemd) Restart(ctx context.Context) error { return s.ctl(ctx, "restart") }

func (s *systemd) ctl(ctx context.Context, verb string) error {
	if _, err := os.Stat(s.unitPath); errors.Is(err, fs.ErrNotExist) {
		return ErrNotInstalled
	}
	_, err := s.systemctl(ctx, verb, unitName)
	return err
}

func (s *systemd) Status(ctx context.Context) (Status, error) {
	st := Status{Kind: Systemd, State: "not installed", Home: s.InstalledHome()}
	if _, err := os.Stat(s.unitPath); errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	st.Installed = true
	out, err := s.systemctl(ctx, "show", unitName, "-p", "ActiveState", "-p", "SubState", "-p", "MainPID", "-p", "NRestarts")
	if err != nil {
		return st, err
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	st.State = props["ActiveState"]
	if sub := props["SubState"]; sub != "" {
		st.State += " (" + sub + ")"
	}
	st.Running = props["ActiveState"] == "active"
	st.PID, _ = strconv.Atoi(props["MainPID"])
	mode := "system unit"
	if s.user {
		mode = "user unit"
	}
	st.Detail = fmt.Sprintf("%s %s, restarts %s", mode, s.unitPath, props["NRestarts"])
	return st, nil
}

func (s *systemd) InstalledHome() string {
	b, err := os.ReadFile(s.unitPath)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart="); ok {
			return homeFromArgs(splitUnitArgs(v))
		}
	}
	return ""
}

func (s *systemd) PurgePlan(home string) PurgePlan {
	p := PurgePlan{Home: home}
	if !s.user {
		p.Extra = []string{s.envDir}
		if _, err := s.lookupUser(Name); err == nil {
			p.Account = Name
		}
	}
	return p
}

func (s *systemd) Purge(ctx context.Context, p PurgePlan) error {
	if err := purgeFiles(p); err != nil {
		return err
	}
	if p.Account != "" {
		if _, err := s.run(ctx, "userdel", p.Account); err != nil {
			return err
		}
	}
	return nil
}
