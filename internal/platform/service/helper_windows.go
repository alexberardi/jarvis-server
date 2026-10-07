package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// The Windows updater (ID11): a LocalSystem service, manual start, that runs
// `jarvisd.exe upgrade --helper --home <home>`. Its security descriptor lets jarvisd's own
// service SID query and start it and nothing else, so the unprivileged service asks for the
// privileged step by starting it (StartHelper); the helper swaps the binary, then restarts
// jarvisd through the SCM.

func helperArgs(home string) []string { return []string{"upgrade", "--helper", "--home", home} }

// installHelper creates (or reconfigures) the updater service and sets its permissions. The
// jarvisd service must exist: its virtual account's SID is looked up.
func installHelper(m *mgr.Mgr, binary, home string, out io.Writer) error {
	cfg := mgr.Config{
		DisplayName:      "Jarvis server updater (jarvisd)",
		Description:      "Installs a jarvisd update that jarvisd downloaded and verified; started on demand by jarvisd.",
		StartType:        mgr.StartManual,
		ErrorControl:     mgr.ErrorNormal,
		ServiceStartName: "LocalSystem",
	}
	s, err := m.OpenService(HelperName)
	if err == nil {
		cur, err := s.Config()
		if err != nil {
			s.Close()
			return err
		}
		cfg.ServiceType = cur.ServiceType
		cfg.BinaryPathName = commandLine(binary, helperArgs(home)...)
		if err := s.UpdateConfig(cfg); err != nil {
			s.Close()
			return fmt.Errorf("update %s: %w", HelperName, err)
		}
	} else if s, err = m.CreateService(HelperName, binary, cfg, helperArgs(home)...); err != nil {
		return fmt.Errorf("create service %s: %w", HelperName, err)
	}
	defer s.Close()
	sid, _, _, err := windows.LookupSID("", WindowsAccount)
	if err != nil {
		return fmt.Errorf("look up %s: %w", WindowsAccount, err)
	}
	sd, err := windows.SecurityDescriptorFromString(HelperSDDL(sid.String()))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if err := windows.SetSecurityInfo(s.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("set the permissions of %s: %w", HelperName, err)
	}
	printf(out, "registered service %s (the self-update helper, runs as LocalSystem on demand, log %s)\n", HelperName, HelperLogPath(binary))
	return nil
}

// uninstallHelper stops and deletes the updater service, if it exists.
func uninstallHelper(ctx context.Context, m *mgr.Mgr) error {
	s, err := m.OpenService(HelperName)
	if err != nil {
		return nil
	}
	defer s.Close()
	if err := stopService(ctx, s); err != nil {
		return err
	}
	return s.Delete()
}

// helperStatus describes the updater service.
func helperStatus() string {
	s, done, err := openQueryName(HelperName)
	if err != nil {
		return "none: run `jarvisd service install` again (elevated) to add it"
	}
	defer done()
	q, err := s.Query()
	if err != nil {
		return HelperName
	}
	return HelperName + " (" + stateNames[q.State] + ", manual start, runs as LocalSystem)"
}

// DetectHelper reports the updater service when this process may start it (the jarvisd
// service's SID may; so may an administrator).
func DetectHelper() Helper {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return HelperNone
	}
	defer windows.CloseServiceHandle(h)
	name, _ := windows.UTF16PtrFromString(HelperName)
	sh, err := windows.OpenService(h, name, windows.SERVICE_START|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return HelperNone
	}
	windows.CloseServiceHandle(sh)
	return HelperSCM
}

// StartHelper starts the updater service (jarvisd asks for the privileged step). Already
// running is fine: it looks for pending work again before it exits.
func StartHelper(context.Context) error {
	h, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(h)
	name, _ := windows.UTF16PtrFromString(HelperName)
	sh, err := windows.OpenService(h, name, windows.SERVICE_START|windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return fmt.Errorf("open %s: %w", HelperName, err)
	}
	defer windows.CloseServiceHandle(sh)
	if err := windows.StartService(sh, 0, nil); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("start %s: %w", HelperName, err)
	}
	return nil
}

// RestartFromHelper stops jarvisd (it may be starting and not yet accept a stop: then wait
// for it to exit by itself) and starts it again, from the LocalSystem helper.
func RestartFromHelper(ctx context.Context) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(Name)
	if err != nil {
		return err
	}
	defer s.Close()
	if st, err := s.Query(); err == nil && st.State != svc.Stopped && st.State != svc.StopPending {
		_, _ = s.Control(svc.Stop)
	}
	if !poll(ctx, 90*time.Second, 250*time.Millisecond, func() bool {
		st, err := s.Query()
		return err == nil && st.State == svc.Stopped
	}) {
		return errors.New("jarvisd did not stop within 90 s")
	}
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("start jarvisd: %w", err)
	}
	return nil
}
