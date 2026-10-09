package service

import (
	"context"
	"os"
	"sync"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// IsWindowsService reports whether the SCM started this process.
func IsWindowsService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// RunWindowsService answers the SCM and runs fn until it returns. Stop and Shutdown cancel
// fn's context (the same graceful path as SIGTERM elsewhere), Ready(ctx) inside fn reports
// Running, and fn's error becomes the service's exit code (scmExit): RestartExitCode or 1 as
// a service-specific code, so the recovery actions restart jarvisd, or 0 (a stop through the
// SCM, or the admin Stop button), which the SCM records as a clean stop and leaves alone.
func RunWindowsService(fn func(ctx context.Context) error) error {
	h := &handler{fn: fn}
	if err := svc.Run(Name, h); err != nil {
		return err
	}
	return h.err
}

// RunHelperService answers the SCM for the updater service (ID11): Running at once, then fn
// (Stop cancels its context); fn's error becomes the exit code.
func RunHelperService(fn func(ctx context.Context) error) error {
	h := &handler{fn: func(ctx context.Context) error {
		_ = Ready(ctx)
		return fn(ctx)
	}}
	if err := svc.Run(HelperName, h); err != nil {
		return err
	}
	return h.err
}

type handler struct {
	fn  func(ctx context.Context) error
	err error
}

func (h *handler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	s <- svc.Status{State: svc.StartPending, WaitHint: 120_000}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	stopping := false
	ready := func() {
		mu.Lock()
		defer mu.Unlock()
		if !stopping {
			s <- svc.Status{State: svc.Running, Accepts: accepts}
		}
	}
	done := make(chan error, 1)
	go func() { done <- h.fn(WithReady(ctx, ready)) }()
	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				mu.Lock()
				stopping = true
				s <- svc.Status{State: svc.StopPending, WaitHint: 45_000}
				mu.Unlock()
				cancel()
			}
		case err := <-done:
			h.err = err
			return scmExit(err)
		}
	}
}

// RedirectStderr sends this process's stderr (Go's os.Stderr and the Windows standard
// error handle, which a crash trace uses) to the log file: under the SCM stderr goes
// nowhere.
func RedirectStderr(path string) error {
	f, err := OpenLog(path)
	if err != nil {
		return err
	}
	if err := windows.SetStdHandle(windows.STD_ERROR_HANDLE, windows.Handle(f.Fd())); err != nil {
		f.Close()
		return err
	}
	os.Stderr = f
	return nil
}
