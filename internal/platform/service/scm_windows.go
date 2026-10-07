package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// New returns the Windows SCM manager. user is not supported on Windows.
func New(user bool, out io.Writer) (Manager, error) {
	if user {
		return nil, errors.New("--user is for Linux; on Windows jarvisd installs as a service")
	}
	return &scm{out: out}, nil
}

// Open returns the manager for an installed service.
func Open(user bool, out io.Writer) (Manager, error) { return New(user, out) }

// InstalledHome is the --home of the installed jarvisd service, "" when none.
func InstalledHome() string { return (&scm{}).InstalledHome() }

// DefaultHome is the service's data directory: %ProgramData%\jarvisd (ID1). It must be
// passed explicitly: under a service account the user's home is a system profile (I0).
func DefaultHome() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, Name)
}

type scm struct{ out io.Writer }

func (*scm) Kind() Kind { return SCM }

func connect() (*mgr.Mgr, error) {
	m, err := mgr.Connect()
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, errors.New("managing the jarvisd service needs an elevated (Run as administrator) prompt")
	}
	return m, err
}

// openQuery opens the service with read-only rights, which a non-elevated user has.
func openQuery() (*mgr.Service, func(), error) {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, nil, err
	}
	name, _ := windows.UTF16PtrFromString(Name)
	sh, err := windows.OpenService(h, name, windows.SERVICE_QUERY_CONFIG|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		windows.CloseServiceHandle(h)
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return nil, nil, ErrNotInstalled
		}
		return nil, nil, err
	}
	return &mgr.Service{Name: Name, Handle: sh}, func() {
		windows.CloseServiceHandle(sh)
		windows.CloseServiceHandle(h)
	}, nil
}

func commandLine(exe string, args ...string) string {
	parts := []string{syscall.EscapeArg(exe)}
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}

func (w *scm) Install(ctx context.Context, o InstallOptions) error {
	if o.User {
		return errors.New("--user is for Linux; on Windows jarvisd installs as a service")
	}
	if o.Home == "" {
		o.Home = DefaultHome()
	}
	if prof := os.Getenv("USERPROFILE"); prof != "" && strings.HasPrefix(strings.ToLower(o.Binary), strings.ToLower(prof)+`\`) {
		return fmt.Errorf("the service account can't read %s (it is in your profile); copy jarvisd.exe to %s first",
			o.Binary, filepath.Join(os.Getenv("ProgramFiles"), Name))
	}
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if err := makeHome(o.Home); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(o.Home, "logs"), 0o700); err != nil {
		return err
	}
	args := []string{"serve", "--home", o.Home}
	cfg := mgr.Config{
		DisplayName:      "Jarvis server (jarvisd)",
		Description:      "Jarvis voice assistant server. Data in " + o.Home + ".",
		StartType:        mgr.StartAutomatic,
		DelayedAutoStart: true, // after the network is up
		ErrorControl:     mgr.ErrorNormal,
		ServiceStartName: WindowsAccount,
		// An unrestricted service SID lets ACLs and firewall rules name NT SERVICE\jarvisd.
		SidType: windows.SERVICE_SID_TYPE_UNRESTRICTED,
	}
	s, err := m.OpenService(Name)
	if err == nil {
		// Reinstall: stop the old one (it holds the exe), then point it at the new command line.
		if err := stopService(ctx, s); err != nil {
			s.Close()
			return err
		}
		cur, err := s.Config()
		if err != nil {
			s.Close()
			return err
		}
		cfg.ServiceType = cur.ServiceType
		cfg.BinaryPathName = commandLine(o.Binary, args...)
		if err := s.UpdateConfig(cfg); err != nil {
			s.Close()
			return err
		}
	} else {
		s, err = m.CreateService(Name, o.Binary, cfg, args...)
		if err != nil {
			return fmt.Errorf("create service: %w", err)
		}
	}
	defer s.Close()
	// Restart after 5 s, 30 s, 60 s; count failures over a day. Non-crash failures (a
	// non-zero exit, including RestartExitCode) trigger them too.
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 60 * time.Second},
	}, 24*60*60); err != nil {
		return fmt.Errorf("recovery actions: %w", err)
	}
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("recovery actions: %w", err)
	}
	// SYSTEM and Administrators full control, the service's virtual account modify, nobody
	// else (00-installers §2.3). The account exists now that the service does.
	if _, err := execRun(ctx, "icacls", o.Home, "/inheritance:r", "/grant:r",
		"*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F", WindowsAccount+":(OI)(CI)M"); err != nil {
		return fmt.Errorf("restrict %s: %w", o.Home, err)
	}
	envFile := filepath.Join(o.Home, "jarvisd.env")
	if _, err := writeEnvTemplate(envFile, 0o600); err != nil {
		return err
	}
	printf(w.out, "registered service %s (runs %s as %s, data in %s, log %s)\n", Name, o.Binary, WindowsAccount, o.Home, LogPath(o.Home))
	if o.NoStart {
		return nil
	}
	return startService(ctx, s, o.Home)
}

func startService(ctx context.Context, s *mgr.Service, home string) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Running {
		return nil
	}
	if err := s.Start(); err != nil {
		return fmt.Errorf("start: %w", err)
	}
	var last svc.Status
	ok := poll(ctx, 2*time.Minute, 250*time.Millisecond, func() bool {
		last, err = s.Query()
		return err == nil && (last.State == svc.Running || last.State == svc.Stopped)
	})
	switch {
	case err != nil:
		return err
	case !ok:
		return fmt.Errorf("jarvisd did not report running within 2 minutes; see %s", LogPath(home))
	case last.State == svc.Stopped:
		return fmt.Errorf("jarvisd stopped during start (exit %d/%d); see %s", last.Win32ExitCode, last.ServiceSpecificExitCode, LogPath(home))
	}
	return nil
}

// stopService asks the service to stop and waits for it: jarvisd drains its engines, and
// the exe stays locked until it is gone.
func stopService(ctx context.Context, s *mgr.Service) error {
	st, err := s.Query()
	if err != nil {
		return err
	}
	if st.State == svc.Stopped {
		return nil
	}
	if st.State != svc.StopPending {
		if _, err := s.Control(svc.Stop); err != nil {
			return fmt.Errorf("stop: %w", err)
		}
	}
	if !poll(ctx, time.Minute, 250*time.Millisecond, func() bool {
		st, err = s.Query()
		return err == nil && st.State == svc.Stopped
	}) {
		return errors.New("jarvisd did not stop within a minute")
	}
	return err
}

// withService opens the installed service with full rights.
func withService(f func(*mgr.Service) error) error {
	m, err := connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return ErrNotInstalled
	}
	defer s.Close()
	return f(s)
}

func (w *scm) Uninstall(ctx context.Context) error {
	home := w.InstalledHome()
	return withService(func(s *mgr.Service) error {
		if err := stopService(ctx, s); err != nil {
			return err
		}
		if err := s.Delete(); err != nil {
			return err
		}
		printf(w.out, "removed service %s; data kept in %s\n", Name, home)
		return nil
	})
}

func (w *scm) Start(ctx context.Context) error {
	home := w.InstalledHome()
	return withService(func(s *mgr.Service) error { return startService(ctx, s, home) })
}

func (w *scm) Stop(ctx context.Context) error {
	return withService(func(s *mgr.Service) error { return stopService(ctx, s) })
}

func (w *scm) Restart(ctx context.Context) error {
	home := w.InstalledHome()
	return withService(func(s *mgr.Service) error {
		if err := stopService(ctx, s); err != nil {
			return err
		}
		return startService(ctx, s, home)
	})
}

var stateNames = map[svc.State]string{
	svc.Stopped: "stopped", svc.StartPending: "start pending", svc.StopPending: "stop pending",
	svc.Running: "running", svc.ContinuePending: "continue pending", svc.PausePending: "pause pending",
	svc.Paused: "paused",
}

func (w *scm) Status(ctx context.Context) (Status, error) {
	st := Status{Kind: SCM, State: "not installed"}
	s, done, err := openQuery()
	if errors.Is(err, ErrNotInstalled) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	defer done()
	st.Installed = true
	if c, err := s.Config(); err == nil {
		st.Home = homeFromArgs(splitCommandLine(c.BinaryPathName))
		st.Detail = "account " + c.ServiceStartName
	}
	q, err := s.Query()
	if err != nil {
		return st, err
	}
	st.State = stateNames[q.State]
	st.Running = q.State == svc.Running
	st.PID = int(q.ProcessId)
	if q.State == svc.Stopped && (q.Win32ExitCode != 0 || q.ServiceSpecificExitCode != 0) {
		st.Detail += fmt.Sprintf(", last exit %d/%d", q.Win32ExitCode, q.ServiceSpecificExitCode)
	}
	return st, nil
}

func (w *scm) InstalledHome() string {
	s, done, err := openQuery()
	if err != nil {
		return ""
	}
	defer done()
	c, err := s.Config()
	if err != nil {
		return ""
	}
	return homeFromArgs(splitCommandLine(c.BinaryPathName))
}

func splitCommandLine(s string) []string {
	args, err := windows.DecomposeCommandLine(s)
	if err != nil {
		return nil
	}
	return args
}

// PurgePlan is the data directory only: the virtual account goes with the service.
func (w *scm) PurgePlan(home string) PurgePlan { return PurgePlan{Home: home} }

func (w *scm) Purge(_ context.Context, p PurgePlan) error { return purgeFiles(p) }
