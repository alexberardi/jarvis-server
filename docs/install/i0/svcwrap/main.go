//go:build windows

// Command svcwrap is an I0 spike helper, not product code. jarvisd does not answer the Windows
// Service Control Manager yet (that is I1), so this wrapper does: it reports Running to the SCM,
// runs jarvisd as a child with the variables from an env file, sends its output to a log file,
// and kills it on Stop/Shutdown. It exists only to observe how jarvisd behaves as a service
// under a virtual account (NT SERVICE\jarvisd) in session 0.
//
//	svcwrap.exe <envfile> <exe> [args...]
//
// The env file holds KEY=VALUE lines; JARVIS_HOME must be one of them (logs go to
// <JARVIS_HOME>\logs\jarvisd.log).
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
)

type wrapper struct {
	envFile string
	argv    []string
}

func readEnv(path string) ([]string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	var env []string
	home := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		env = append(env, line)
		if k, v, _ := strings.Cut(line, "="); k == "JARVIS_HOME" {
			home = v
		}
	}
	if home == "" {
		return nil, "", fmt.Errorf("%s: JARVIS_HOME not set", path)
	}
	return env, home, sc.Err()
}

func (w *wrapper) start() (*exec.Cmd, *os.File, error) {
	env, home, err := readEnv(w.envFile)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Join(home, "logs"), 0o700); err != nil {
		return nil, nil, err
	}
	log, err := os.OpenFile(filepath.Join(home, "logs", "jarvisd.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, nil, err
	}
	fmt.Fprintf(log, "svcwrap: starting %q at %s\n", w.argv, time.Now().Format(time.RFC3339))
	cmd := exec.Command(w.argv[0], w.argv[1:]...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, nil, err
	}
	return cmd, log, nil
}

// Execute implements svc.Handler.
func (w *wrapper) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	cmd, log, err := w.start()
	if err != nil {
		return true, 1
	}
	defer log.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-exited:
			fmt.Fprintf(log, "svcwrap: child exited: %v\n", err)
			return true, 2
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending}
				fmt.Fprintf(log, "svcwrap: stop requested; killing pid %d (no graceful signal on Windows)\n", cmd.Process.Pid)
				_ = cmd.Process.Kill()
				<-exited
				return false, 0
			}
		}
	}
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: svcwrap <envfile> <exe> [args...]")
		os.Exit(2)
	}
	w := &wrapper{envFile: os.Args[1], argv: os.Args[2:]}
	isSvc, err := svc.IsWindowsService()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !isSvc { // foreground, for debugging
		cmd, log, err := w.start()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer log.Close()
		_ = cmd.Wait()
		return
	}
	if err := svc.Run("jarvisd", w); err != nil {
		os.Exit(1)
	}
}
