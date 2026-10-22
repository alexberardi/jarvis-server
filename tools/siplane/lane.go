package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

// readLaneEnv reads the lane.env scripts/sip-lane writes.
func readLaneEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no SIP lane (%s): scripts/sandbox up --voice --sip", path)
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	kvs, err := config.ParseEnv(f)
	if err != nil {
		return nil, err
	}
	env := map[string]string{}
	for _, kv := range kvs {
		env[kv[0]] = kv[1]
	}
	return env, nil
}

// voice is the sandbox's TTS and STT, reached as its seeded node (node auth, as a Pi).
type voice struct {
	cc, id, key string
	http        *http.Client
}

func (v *voice) Speak(ctx context.Context, text string) ([]byte, error) {
	b, _ := json.Marshal(map[string]string{"text": text})
	return v.post(ctx, "/api/v0/media/tts/speak", "application/json", b)
}

func (v *voice) Transcribe(ctx context.Context, wav []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "heard.wav")
	if err != nil {
		return "", err
	}
	fw.Write(wav)
	mw.Close()
	rb, err := v.post(ctx, "/api/v0/media/whisper/transcribe", mw.FormDataContentType(), body.Bytes())
	if err != nil {
		return "", err
	}
	var t struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(rb, &t); err != nil {
		return "", fmt.Errorf("transcribe: %w", err)
	}
	return strings.TrimSpace(t.Text), nil
}

func (v *voice) post(ctx context.Context, path, contentType string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.cc+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-API-Key", v.id+":"+v.key)
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		s := strings.TrimSpace(string(rb))
		if len(s) > 300 {
			s = s[:300]
		}
		return nil, fmt.Errorf("POST %s: %d %s", path, resp.StatusCode, s)
	}
	return rb, nil
}

// lane is a running SIP lane (scripts/sip-lane up) and the sandbox voice that speaks and
// hears for its far ends.
type lane struct {
	env   map[string]string
	root  string
	voice *voice
	out   string // --out: heard audio, transcripts and PBX dumps go here; "" = nowhere
}

// docker runs a docker command; its combined output is in the error when it fails.
func (l *lane) docker(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	b, err := cmd.CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(b))
		if len(s) > 1500 {
			s = "…" + s[len(s)-1500:]
		}
		return string(b), fmt.Errorf("docker %s: %w: %s", strings.Join(args[:min(len(args), 3)], " "), err, s)
	}
	return string(b), nil
}

// cli is an Asterisk CLI command on the lane's PBX.
func (l *lane) cli(ctx context.Context, command string) (string, error) {
	return l.docker(ctx, "exec", l.env["SIP_LANE_ASTERISK"], "asterisk", "-rx", command)
}

// sipp runs one SIPp scenario of test/sip/sipp from addr (an address on loopback) against
// the PBX; it fails unless every call of the scenario passed.
// A failure says what SIPp received (its message trace, start lines) and why it gave up.
func (l *lane) sipp(ctx context.Context, xml, addr string, args ...string) error {
	trace := filepath.Join(l.env["SIP_LANE_DIR"], "sipp")
	os.RemoveAll(trace)
	if err := os.MkdirAll(trace, 0o777); err != nil {
		return err
	}
	os.Chmod(trace, 0o777) // whoever the container runs as writes here
	nonce := time.Now().UnixNano()
	a := []string{"run", "--rm", "--network", "host", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		"-w", "/trace", "-v", trace + ":/trace", "-v", filepath.Join(l.root, "test", "sip", "sipp") + ":/sipp:ro",
		l.env["SIP_LANE_FAREND_IMAGE"], "sipp", l.env["SIP_LANE_SIP"], "-sf", "/sipp/" + xml,
		"-i", addr, "-p", l.env["SIP_LANE_SIPP_PORT"], "-mp", l.env["SIP_LANE_SIPP_MEDIA_PORT"],
		"-nostdin", "-timeout", "30s", "-timeout_error", "-max_retrans", "3", "-trace_msg", "-trace_err",
		// SIPp's default Call-ID is call number-pid@ip, and its pid in a container is always 1:
		// the next run would reuse the last one's Call-ID, and Asterisk takes its INVITE for a
		// retransmission of the finished dialog and drops it.
		// The scenarios put [run] in their branches and tags for the same reason.
		"-cid_str", fmt.Sprintf("%%u-%d@%%s", nonce), "-key", "run", strconv.FormatInt(nonce, 36)}
	a = append(a, args...)
	if !hasArg(args, "-m") {
		a = append(a, "-m", "1")
	}
	if _, err := l.docker(ctx, a...); err != nil {
		return fmt.Errorf("SIPp %s failed; it received: %s; errors: %s", xml, sippTrace(trace, "_messages.log"), sippTrace(trace, "_errors.log"))
	}
	return nil
}

// sippTrace sums up SIPp's trace file of that suffix: the start line of each message for
// the message log, the last lines for the error log.
func sippTrace(dir, suffix string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "*"+suffix))
	var lines []string
	for _, f := range files {
		b, _ := os.ReadFile(f)
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			switch {
			case line == "" || strings.HasPrefix(line, "---"):
			case suffix == "_errors.log":
				lines = append(lines, line)
			case strings.HasPrefix(line, "SIP/2.0 ") || strings.Contains(line, " sip:") && strings.HasSuffix(line, "SIP/2.0"):
				lines = append(lines, line)
			}
		}
	}
	if suffix == "_errors.log" && len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	if len(lines) == 0 {
		return "(nothing)"
	}
	return strings.Join(lines, " | ")
}

func hasArg(args []string, a string) bool {
	for _, x := range args {
		if x == a {
			return true
		}
	}
	return false
}

// uri is a SIP URI for a number or extension on the lane's PBX.
func (l *lane) uri(number string) string { return "sip:" + number + "@" + l.env["SIP_LANE_SIP"] }

// party is a baresip far end: it plays /w/say.wav into each call and records the call's
// audio to /w/heard.wav (its directory under the lane's).
type party struct {
	name, container, ctrl, dir string
}

func (l *lane) party(name string) party {
	k := "SIP_LANE_" + strings.ToUpper(name)
	return party{name: name, container: l.env[k+"_CONTAINER"], ctrl: l.env[k+"_CTRL"], dir: filepath.Join(l.env["SIP_LANE_DIR"], name)}
}

// soxPad is sox's arguments for 8 kHz mono 16-bit audio of exactly total seconds: lead
// seconds of silence, the speech, silence to the end. baresip hangs up when its source
// ends, so total is also how long the call lasts on this side.
func soxPad(in, out string, lead, total float64) []string {
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	return []string{in, "-r", "8000", "-c", "1", "-b", "16", out, "pad", f(lead), f(total), "trim", "0", f(total)}
}

// say makes the party say text in its next call (silence when text is ""), and forgets what
// it heard last.
func (l *lane) say(ctx context.Context, p party, text string, lead, total float64) error {
	if _, err := l.docker(ctx, "exec", p.container, "rm", "-f", "/w/heard.wav", "/w/heard16.wav"); err != nil {
		return err
	}
	if text == "" {
		_, err := l.docker(ctx, "exec", p.container, "sox", "-n", "-r", "8000", "-c", "1", "-b", "16", "/w/say.wav", "trim", "0", strconv.FormatFloat(total, 'f', -1, 64))
		return err
	}
	wav, err := l.voice.Speak(ctx, text)
	if err != nil {
		return fmt.Errorf("TTS: %w (is the sandbox up with --voice?)", err)
	}
	if err := os.WriteFile(filepath.Join(p.dir, "tts.wav"), wav, 0o666); err != nil {
		return err
	}
	_, err = l.docker(ctx, append([]string{"exec", p.container, "sox"}, soxPad("/w/tts.wav", "/w/say.wav", lead, total)...)...)
	return err
}

// heard transcribes what the party recorded in its last call (at 16 kHz, whisper's rate),
// keeping the recording under --out as <scenario>-<party>.wav.
func (l *lane) heard(ctx context.Context, p party, scenario string) (string, error) {
	if _, err := l.docker(ctx, "exec", p.container, "sox", "/w/heard.wav", "-r", "16000", "/w/heard16.wav"); err != nil {
		return "", fmt.Errorf("%s recorded nothing: %w", p.name, err)
	}
	wav, err := os.ReadFile(filepath.Join(p.dir, "heard16.wav"))
	if err != nil {
		return "", err
	}
	if l.out != "" {
		os.WriteFile(filepath.Join(l.out, scenario+"-"+p.name+".wav"), wav, 0o644)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	return l.voice.Transcribe(ctx, wav)
}

// keep writes a text file under --out.
func (l *lane) keep(name, text string) {
	if l.out != "" {
		os.WriteFile(filepath.Join(l.out, name), []byte(text), 0o644)
	}
}

// farEnds are the lane's baresip parties.
var farEnds = []string{"jarvis", "alice", "carrier"}

// deadFarEnds names the far ends that have exited ("jarvis exited with code 139 (segmentation fault)"),
// keeping each one's log under --out as <scenario>-<party>-baresip.log. A container that is
// not there at all is a lane error.
func (l *lane) deadFarEnds(ctx context.Context, scenario string) ([]string, error) {
	var dead []string
	for _, n := range farEnds {
		c := l.party(n).container
		st, err := l.docker(ctx, "inspect", "-f", "{{.State.Running}} {{.State.ExitCode}}", c)
		if err != nil {
			return dead, err
		}
		f := strings.Fields(st)
		if len(f) == 2 && f[0] == "true" {
			continue
		}
		why := "exited"
		if len(f) == 2 {
			why += " with code " + f[1]
			if code, err := strconv.Atoi(f[1]); err == nil && code > 128 {
				why += " (" + syscall.Signal(code-128).String() + ")"
			}
		}
		log, _ := l.docker(ctx, "logs", "--tail", "300", c)
		l.keep(scenario+"-"+n+"-baresip.log", log)
		dead = append(dead, n+" "+why)
	}
	return dead, nil
}

// restart brings the whole lane up again (scripts/sip-lane up, same name and ports): a far
// end restarted alone would register a new contact, which max_contacts=1 with
// remove_existing=no refuses while the dead one's is still bound.
func (l *lane) restart(ctx context.Context) error {
	args := []string{"up", "--name", l.env["SIP_LANE_NAME"]}
	if o := l.env["SIP_LANE_OFFSET"]; o != "" {
		args = append(args, "--offset", o)
	}
	cmd := exec.CommandContext(ctx, filepath.Join(l.root, "scripts", "sip-lane"), args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("scripts/sip-lane up: %w: %s", err, strings.TrimSpace(string(b)))
	}
	return nil
}
