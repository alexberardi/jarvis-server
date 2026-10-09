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
	// lingerMark (user mode) records that install turned lingering on, so uninstall turns it
	// off again only then. It lives in the user's config dir, beside the unit, not in the
	// data home (which --purge deletes and a --home can move).
	lingerMark string

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
		s.lingerMark = lingerMarkPath(os.Getenv, os.UserHomeDir)
	}
	return s
}

// userConfigDir is $XDG_CONFIG_HOME (default ~/.config).
func userConfigDir(getenv func(string) string, home func() (string, error)) string {
	if cfg := getenv("XDG_CONFIG_HOME"); cfg != "" {
		return cfg
	}
	h, _ := home()
	return filepath.Join(h, ".config")
}

// userUnitPath is $XDG_CONFIG_HOME/systemd/user/jarvisd.service (default ~/.config).
func userUnitPath(getenv func(string) string, home func() (string, error)) string {
	return filepath.Join(userConfigDir(getenv, home), "systemd", "user", unitName)
}

// lingerMarkPath is $XDG_CONFIG_HOME/jarvisd/linger-enabled: present when `service install
// --user` turned lingering on (it was off).
func lingerMarkPath(getenv func(string) string, home func() (string, error)) string {
	return filepath.Join(userConfigDir(getenv, home), Name, "linger-enabled")
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
	name := s.userName()
	was := s.linger(ctx, name)
	if _, err := s.run(ctx, "loginctl", "enable-linger", name); err != nil {
		printf(s.out, "warning: could not keep jarvisd running while you are logged out (%v).\n"+
			"  Run once: sudo loginctl enable-linger %s\n", err, name)
		return nil
	}
	if was == "no" { // remember it was us, for uninstall (an earlier mark is kept on reinstall)
		if err := os.MkdirAll(filepath.Dir(s.lingerMark), 0o700); err == nil {
			err = os.WriteFile(s.lingerMark, []byte(name+"\n"), 0o600)
		}
		if err != nil {
			printf(s.out, "warning: could not record that lingering was turned on (%v); "+
				"`jarvisd service uninstall --user` will leave it on\n", err)
		}
	}
	return nil
}

// userName is the account a --user unit runs as.
func (s *systemd) userName() string {
	if u, err := s.current(); err == nil {
		return u.Username
	}
	return s.getenv("USER")
}

// linger is logind's Linger property for name: "yes", "no", or "" when unknown.
func (s *systemd) linger(ctx context.Context, name string) string {
	out, err := s.run(ctx, "loginctl", "show-user", name, "-p", "Linger", "--value")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// undoLinger turns lingering off when install turned it on (the mark is there), and drops the
// mark. Lingering that was on before jarvisd is left alone.
func (s *systemd) undoLinger(ctx context.Context) {
	if s.lingerMark == "" {
		return
	}
	if _, err := os.Stat(s.lingerMark); err != nil {
		return
	}
	name := s.userName()
	if _, err := s.run(ctx, "loginctl", "disable-linger", name); err != nil {
		printf(s.out, "warning: could not turn lingering back off (%v); run: loginctl disable-linger %s\n", err, name)
		return
	}
	_ = os.Remove(s.lingerMark)
	_ = os.Remove(filepath.Dir(s.lingerMark)) // only if empty
	printf(s.out, "turned lingering off again (service install had turned it on)\n")
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
	if s.user {
		s.undoLinger(ctx)
	}
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
	st.StaleReason = s.staleness()
	st.Stale = st.StaleReason != ""
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
	if b, err := os.ReadFile(s.unitPath); err == nil && strings.Contains(string(b), " upgrade --prestart ") {
		st.UpgradeHelper = "root ExecStartPre (upgrade --prestart)"
	} else if !s.user {
		st.UpgradeHelper = "none: run `sudo jarvisd service install` again to add it"
	}
	return st, nil
}

func (s *systemd) InstalledHome() string {
	_, home, _ := s.installed()
	return home
}

// installed reads the binary, home and account (User=) of the installed unit.
func (s *systemd) installed() (binary, home, account string) {
	b, err := os.ReadFile(s.unitPath)
	if err != nil {
		return "", "", ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "ExecStart="); ok {
			if args := splitUnitArgs(v); len(args) > 0 {
				binary, home = args[0], homeFromArgs(args)
			}
		}
		if v, ok := strings.CutPrefix(line, "User="); ok {
			account = v
		}
	}
	return binary, home, account
}

// staleness compares the installed unit with the one this build renders for the same binary,
// home and account: "" when they match, else why `service install` should run again (an
// older version wrote it: its ExecStartPre lacked --owner, or the unit had no helper at all).
func (s *systemd) staleness() string {
	have, err := os.ReadFile(s.unitPath)
	if err != nil {
		return ""
	}
	binary, home, account := s.installed()
	if binary == "" || home == "" {
		return "the unit has no ExecStart this version can read; " + staleNote
	}
	want, err := RenderSystemd(Unit{Binary: binary, Home: home, User: s.user, Account: account})
	if err != nil || string(have) == string(want) {
		return ""
	}
	note := staleNote
	if s.user {
		note = "run `jarvisd service install --user` again (it keeps the data and home)"
	}
	if !s.user && !strings.Contains(string(have), " upgrade --prestart ") {
		return "the unit has no upgrade helper (an older version wrote it); " + note
	}
	if !unitStopReady(have) {
		return "the unit restarts jarvisd after an admin stop (an older version wrote it); " + note
	}
	return "the unit differs from what this version writes (an older version wrote it); " + note
}

// stopBlocker says why the admin Stop button can't keep jarvisd stopped under this unit ("" when
// it can) and the command that fixes it: a unit an older version wrote has Restart=always
// without RestartPreventExitStatus=, so exiting would only restart jarvisd. It reads the unit
// file as `service status` does (the service account can read it).
func (s *systemd) stopBlocker() (reason, command string) {
	unit, err := os.ReadFile(s.unitPath)
	if err != nil {
		return "jarvisd can't read its unit " + s.unitPath + ", so it can't tell whether systemd would start it " +
			"again right away. Stop it on the server.", stopCommand(Systemd, s.user)
	}
	if !unitStopReady(unit) {
		return "The installed unit was written by an older version and would start jarvisd again right away. " +
			"Update it first (this restarts jarvisd), then Stop works.", InstallCommand(Systemd, s.user)
	}
	return "", ""
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
