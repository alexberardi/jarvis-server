package sidecar

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"time"
)

// daemonStopWait is how long Stop waits for a daemon to end once its input is closed.
var daemonStopWait = 5 * time.Second

// Daemon is one package daemon (an IJarvisDaemon in daemons/<name>/daemon.py) in a Python
// process of its own, so its run(), which holds a connection open, never blocks the node's
// commands. Unlike a commands sidecar the agent asks it nothing: the child asks (storage and
// the node's backends), several requests at once, and each is answered as it completes.
type Daemon struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	dir   string
	done  chan struct{}
	err   error // why it ended, once done is closed (nil: it was told to and did)

	wmu sync.Mutex // one answer line at a time
}

// StartDaemon runs the daemon at path rel of package pkg, installed at root, with python (the
// packages' venv). It returns once the process has started; Done closes when it has ended.
func StartDaemon(python, root, pkg, rel string, log *slog.Logger, o Options) (*Daemon, error) {
	cmd, dir, err := child(python, root, o, "--daemon", pkg, rel)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Daemon, error) {
		os.RemoveAll(dir)
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail(err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fail(err)
	}
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	d := &Daemon{cmd: cmd, stdin: stdin, dir: dir, done: make(chan struct{})}
	logged := make(chan struct{})
	go func() { // the child's stderr is its log
		defer close(logged)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			log.Info("daemon", "package", pkg, "line", sc.Text())
		}
	}()
	// Only answerTo is used: what a request reads is the store's and the host's own state.
	ans := &Sidecar{log: log, store: o.Storage, host: o.Host, now: time.Now}
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			var q reply
			if json.Unmarshal(sc.Bytes(), &q) != nil || q.From != "child" {
				continue
			}
			go func() {
				b, err := ans.answerTo(q)
				if err != nil {
					return
				}
				d.wmu.Lock()
				defer d.wmu.Unlock()
				_, _ = d.stdin.Write(b) // a child that has gone doesn't read it
			}()
		}
		select { // its last lines, bounded: a process it left behind may hold the pipe
		case <-logged:
		case <-time.After(2 * time.Second):
		}
		d.err = cmd.Wait()
		os.RemoveAll(dir)
		close(d.done)
	}()
	return d, nil
}

// Done closes when the process has ended.
func (d *Daemon) Done() <-chan struct{} { return d.done }

// Err is why the process ended (after Done): nil when it stopped because it was told to.
func (d *Daemon) Err() error { return d.err }

// Stop closes the daemon's input, which cancels its run(), and waits for it to end; one that
// hasn't after daemonStopWait is killed.
func (d *Daemon) Stop() {
	d.wmu.Lock()
	_ = d.stdin.Close()
	d.wmu.Unlock()
	select {
	case <-d.done:
	case <-time.After(daemonStopWait):
		_ = d.cmd.Process.Kill()
		<-d.done
	}
}
