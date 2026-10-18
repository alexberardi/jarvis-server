package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/audio"
)

// piScript runs on the Pi: it puts the Pi back when the run ends, however it ends.
//
//go:embed pi.sh
var piScript []byte

// The sandbox services a node reaches, forwarded to the same ports on the Pi's loopback.
var piForwards = []string{"SBX_CONFIG", "SBX_AUTH", "SBX_LOGS", "SBX_CC", "SBX_WHISPER", "SBX_TTS", "SBX_NOTIFICATIONS", "SBX_MQTT"}

// piPorts is the loopback ports of the sandbox's node-facing services. Each must be on
// 127.0.0.1, the dev box's own sandbox: the node on the Pi reaches them through ssh's reverse
// forwards, so its config URL (127.0.0.1) can't lead anywhere else, prod least of all.
func piPorts(env map[string]string) ([]int, error) {
	var ports []int
	for _, k := range piForwards {
		v := env[k]
		if v == "" {
			continue
		}
		host := v
		if u, err := url.Parse(v); err == nil && u.Host != "" {
			host = u.Host
		}
		h, p, err := splitHostPort(host)
		if err != nil || h != "127.0.0.1" {
			return nil, fmt.Errorf("--audio pi runs only against a local sandbox: %s is %s, not 127.0.0.1", k, v)
		}
		ports = append(ports, p)
	}
	if len(ports) == 0 || env["SBX_CONFIG"] == "" {
		return nil, errors.New("--audio pi: the sandbox has no config service")
	}
	return ports, nil
}

func splitHostPort(s string) (string, int, error) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return "", 0, fmt.Errorf("%q has no port", s)
	}
	p, err := strconv.Atoi(s[i+1:])
	if err != nil || p <= 0 || p > 65535 {
		return "", 0, fmt.Errorf("%q: bad port", s)
	}
	return s[:i], p, nil
}

// piRig runs the node on a Pi over ssh (one long `listen` on the HAT, as the pipewire rig on
// virtual devices): each case is played out of the Pi's own speaker with aplay, beside the
// node's output, and the node's mic hears it in the room. What the node plays is teed to a
// file on the Pi (pi.sh's e2e_tee), fetched per case.
type piRig struct {
	l      *lane
	ssh    []string // the ssh command and its options, before the host
	host   string
	runDir string
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  chan string
	exited chan struct{}
	log    string
}

// shq quotes s for the remote shell.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (r *piRig) remote(ctx context.Context, stdin io.Reader, extra []string, script string) *exec.Cmd {
	args := append(append(append([]string{}, r.ssh[1:]...), extra...), r.host, script)
	c := exec.CommandContext(ctx, r.ssh[0], args...)
	c.Stdin = stdin
	return c
}

func (r *piRig) output(ctx context.Context, stdin io.Reader, script string) (string, error) {
	var out, errb bytes.Buffer
	c := r.remote(ctx, stdin, nil, script)
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("ssh %s: %v: %s", r.host, err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// newPiRig copies the node, its identity, the wake models and pi.sh into a fresh directory on
// the Pi and starts `pi.sh serve` there, with the sandbox's ports forwarded. sshCmd is ssh and
// its options; card the HAT's ALSA card (or "auto").
func newPiRig(ctx context.Context, l *lane, env map[string]string, sshCmd []string, host, card string) (*piRig, error) {
	if host == "" {
		return nil, errors.New("--audio pi needs --pi-host (user@host)")
	}
	ports, err := piPorts(env)
	if err != nil {
		return nil, err
	}
	r := &piRig{l: l, ssh: sshCmd, host: host, log: filepath.Join(l.out, "agent.log")}
	out, err := r.output(ctx, nil, `uname -m && mktemp -d "${TMPDIR:-/tmp}/jarvis-e2e-XXXXXXXX"`)
	if err != nil {
		return nil, err
	}
	f := strings.Fields(out)
	if len(f) != 2 || !strings.Contains(f[1], "/jarvis-e2e-") {
		return nil, fmt.Errorf("ssh %s: unexpected %q", host, out)
	}
	r.runDir = f[1]
	// Until serve takes over, the run's directory is ours to remove.
	cleanup := func() {
		r.output(context.WithoutCancel(ctx), nil, "rm -rf "+shq(r.runDir))
	}
	if f[0] != "aarch64" {
		cleanup()
		return nil, fmt.Errorf("%s is %s: the node is built for linux-arm64 (aarch64)", host, f[0])
	}
	bundle, err := piBundle(l)
	if err != nil {
		cleanup()
		return nil, err
	}
	if _, err := r.output(ctx, bytes.NewReader(bundle), "tar -C "+shq(r.runDir)+" -xf -"); err != nil {
		cleanup()
		return nil, err
	}
	wake := ""
	if l.wakeModels != "" {
		wake = r.runDir + "/wake"
	}
	args := listenArgs(r.runDir+"/credentials.json", wake, "e2e_mic", "e2e_node_out", l.agentArgs)
	script := "bash " + shq(r.runDir+"/pi.sh") + " serve " + shq(r.runDir) + " " + shq(card) + " --"
	for _, a := range args {
		script += " " + shq(a)
	}
	extra := []string{"-o", "ExitOnForwardFailure=yes"}
	for _, p := range ports {
		extra = append(extra, "-R", fmt.Sprintf("127.0.0.1:%d:127.0.0.1:%d", p, p))
	}
	pr, pw := io.Pipe()
	r.cmd = r.remote(ctx, nil, extra, script)
	// serve's stdin stays open, with nothing on it, until close: pi.sh puts the Pi back when
	// it closes, so the Pi is restored when voicelane ends, even when it is killed.
	if r.stdin, err = r.cmd.StdinPipe(); err != nil {
		cleanup()
		return nil, err
	}
	logf, err := os.Create(r.log)
	if err != nil {
		cleanup()
		return nil, err
	}
	both := io.MultiWriter(pw, logf)
	r.cmd.Stdout, r.cmd.Stderr = both, both
	r.cmd.WaitDelay = 10 * time.Second
	if err := r.cmd.Start(); err != nil {
		logf.Close()
		cleanup()
		return nil, err
	}
	r.lines, r.exited = make(chan string, 256), make(chan struct{})
	go func() {
		sc := bufio.NewScanner(pr)
		for sc.Scan() {
			r.lines <- sc.Text()
		}
		close(r.lines)
	}()
	go func() { r.cmd.Wait(); pw.Close(); logf.Close(); close(r.exited) }()
	// pi.sh says "ready" once the node is up, or why not (and has put the Pi back); the node
	// says it is listening once its detector and devices are loaded (slower on a Pi than here).
	timeout := time.After(2 * time.Minute)
	ready, listening := false, false
	for !ready || !listening {
		select {
		case line, ok := <-r.lines:
			if !ok {
				<-r.exited
				return nil, fmt.Errorf("the Pi side ended before the node was up: %s", tail(r.log))
			}
			ready = ready || line == "e2e-pi: ready"
			listening = listening || strings.Contains(line, "voice: listening")
		case <-timeout:
			r.close()
			return nil, fmt.Errorf("the node on %s did not come up: %s", host, tail(r.log))
		case <-ctx.Done():
			r.close()
			return nil, ctx.Err()
		}
	}
	return r, nil
}

// piBundle is the run directory's files as a tar: pi.sh, the node, its identity and the wake
// models.
func piBundle(l *lane) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(name, src string, mode int64, body []byte) error {
		if body == nil {
			b, err := os.ReadFile(src)
			if err != nil {
				return err
			}
			body = b
		}
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg, ModTime: time.Now()}); err != nil {
			return err
		}
		_, err := tw.Write(body)
		return err
	}
	if err := add("pi.sh", "", 0o755, piScript); err != nil {
		return nil, err
	}
	if err := add("jarvis-agent", l.agent, 0o755, nil); err != nil {
		return nil, err
	}
	if err := add("credentials.json", l.creds, 0o600, nil); err != nil {
		return nil, err
	}
	if l.wakeModels != "" {
		if err := tw.WriteHeader(&tar.Header{Name: "wake/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			return nil, err
		}
		err := filepath.WalkDir(l.wakeModels, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !d.Type().IsRegular() {
				return err
			}
			rel, _ := filepath.Rel(l.wakeModels, p)
			if strings.Contains(rel, string(filepath.Separator)) {
				return nil
			}
			return add("wake/"+rel, p, 0o644, nil)
		})
		if err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (r *piRig) play(ctx context.Context, in, out, dir string, wait time.Duration) ([]Turn, float64, error) {
	select {
	case <-r.exited:
		return nil, 0, fmt.Errorf("the node on the Pi is gone: %s", tail(r.log))
	default:
	}
	drain(r.lines)
	f, err := os.Open(in)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	o, err := r.output(ctx, f, "bash "+shq(r.runDir+"/pi.sh")+" play "+shq(r.runDir))
	if err != nil {
		return nil, 0, fmt.Errorf("playing on the Pi: %w", err)
	}
	off, err := strconv.ParseInt(strings.TrimSpace(o), 10, 64)
	if err != nil {
		return nil, 0, fmt.Errorf("playing on the Pi: %q", o)
	}
	log := waitTurn(ctx, r.lines, wait, r.l.replyWait)
	time.Sleep(500 * time.Millisecond) // the tee's tail
	os.WriteFile(filepath.Join(dir, "agent.log"), []byte(log), 0o644)
	raw, err := r.output(ctx, nil, "bash "+shq(r.runDir+"/pi.sh")+" fetch "+shq(r.runDir)+" "+strconv.FormatInt(off, 10))
	if err != nil {
		return nil, 0, fmt.Errorf("fetching the reply: %w", err)
	}
	var wav bytes.Buffer
	audio.WritePCM16WAV(&wav, []byte(raw[:len(raw)&^1]), 1, 16000)
	if err := os.WriteFile(out, wav.Bytes(), 0o644); err != nil {
		return nil, 0, err
	}
	turns := parseTurns(log)
	resp := 0.0
	if len(turns) > 0 && turns[0].Said != "" {
		// The tee has no clock, so: the turn less its recording and its reply's playback
		// (speech-to-text, the model and TTS), as with WAV files.
		resp = max(0, turns[0].TookSecs-turns[0].Recorded-turns[0].ReplySecs)
	}
	return turns, resp, nil
}

// close ends serve: pi.sh stops the node, starts what it stopped and removes the run's
// directory. What it says goes to agent.log.
func (r *piRig) close() {
	r.stdin.Close()
	go func() {
		for range r.lines {
		}
	}()
	select {
	case <-r.exited:
	case <-time.After(60 * time.Second):
		r.cmd.Process.Kill()
		<-r.exited
	}
}

// notes is pi.sh's own lines in the run's log: the card, what ran before and after, and what
// was stopped and restored.
func (r *piRig) notes() []string {
	var out []string
	b, _ := os.ReadFile(r.log)
	for _, line := range strings.Split(string(b), "\n") {
		if n, ok := strings.CutPrefix(line, "e2e-pi: "); ok && n != "ready" && n != "done" {
			out = append(out, n)
		}
	}
	return out
}
