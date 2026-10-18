package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// harnessSimulator is the name scripts/lib/ios.sh gives the simulator it creates. A UDID that
// is not a device of this name (one of the user's simulators, or "booted") is refused.
const harnessSimulator = "jarvis-harness-e2e"

// checkSimulator refuses any device but the harness's own simulator: the UDID must be one simctl
// lists under the harness's device name.
func (c *config) checkSimulator(ctx context.Context) error {
	if c.udid == "" || c.udid == "booted" || c.udid == "all" || c.deviceName == "" {
		return fmt.Errorf("refusing simulator %q: the harness only drives its own simulator, by UDID", c.udid)
	}
	out, err := c.simctl(ctx, "list", "devices", "-j")
	if err != nil {
		return err
	}
	var list struct {
		Devices map[string][]struct {
			UDID string `json:"udid"`
			Name string `json:"name"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return fmt.Errorf("simctl list devices -j: %w", err)
	}
	for _, devs := range list.Devices {
		for _, d := range devs {
			if d.UDID == c.udid {
				if d.Name != c.deviceName {
					return fmt.Errorf("refusing simulator %s (%q): not the harness's %q", c.udid, d.Name, c.deviceName)
				}
				return nil
			}
		}
	}
	return fmt.Errorf("refusing simulator %s: simctl has no such device", c.udid)
}

// simctl runs `xcrun simctl` with a timeout. Callers name the harness UDID in every device
// command; nothing here addresses "booted".
func (c *config) simctl(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.adbTimeout)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, c.xcrun, append([]string{"simctl"}, args...)...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("simctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}

// iosArtifacts keeps the screen as the flow left it: a picture, Maestro's view hierarchy (texts,
// accessibility ids) and the app's part of the simulator's log since the flow started.
func (c *config) iosArtifacts(ctx context.Context, dir string, started time.Time, r *Record, rel func(string) string) {
	png := filepath.Join(dir, "screenshot.png")
	if _, err := c.simctl(ctx, "io", c.udid, "screenshot", png); err == nil {
		if b, err := os.ReadFile(png); err == nil && bytes.HasPrefix(b, []byte("\x89PNG")) {
			r.Screenshots = append(r.Screenshots, rel(png))
		}
	}
	hctx, cancel := context.WithTimeout(ctx, c.adbTimeout)
	defer cancel()
	if out, err := exec.CommandContext(hctx, c.maestro, "--device", c.udid, "hierarchy").Output(); err == nil && len(out) > 0 {
		p := filepath.Join(dir, "hierarchy.json")
		if os.WriteFile(p, out, 0o644) == nil {
			r.Attachments = append(r.Attachments, rel(p))
		}
	}
	pred := `subsystem == "com.facebook.react.log"`
	if c.process != "" {
		pred = fmt.Sprintf("process == %q", c.process)
	}
	if out, err := c.simctl(ctx, "spawn", c.udid, "log", "show", "--style", "compact",
		"--start", started.Add(-time.Second).Format("2006-01-02 15:04:05"), "--predicate", pred); err == nil {
		p := filepath.Join(dir, "device.log")
		if os.WriteFile(p, out, 0o644) == nil {
			r.Attachments = append(r.Attachments, rel(p))
		}
		r.ConsoleErrors = iosJSErrors(string(out), 20)
	}
}

// iosJSErrors picks the app's JavaScript errors out of a `log show --style compact` (date, time,
// then the type: E error, F fault): React Native logs JavaScript to com.facebook.react.log.
func iosJSErrors(log string, max int) []string {
	out := []string{}
	for _, l := range strings.Split(log, "\n") {
		l = strings.TrimSpace(l)
		f := strings.Fields(l)
		if len(f) > 3 && (f[2] == "E" || f[2] == "F") && strings.Contains(l, "[com.facebook.react.log") && len(out) < max {
			out = append(out, l)
		}
	}
	return out
}

// listenPush is the push endpoint's listener (a variable for the tests).
var listenPush = func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }

// startPush serves POST /push on loopback for the flows (PUSH_URL; Maestro's runScript runs on
// this host): the body, an APNs JSON payload, goes to the app on the harness simulator with
// `xcrun simctl push`, so a flow can deliver a notification and then tap it.
func (c *config) startPush() (url string, stop func(), err error) {
	ln, err := listenPush()
	if err != nil {
		return "", nil, fmt.Errorf("push endpoint: %w", err)
	}
	srv := &http.Server{Handler: c.pushHandler(), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String() + "/push", func() { _ = srv.Close() }, nil
}

func (c *config) pushHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /push", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil || !json.Valid(body) {
			http.Error(w, "want an APNs JSON payload", http.StatusBadRequest)
			return
		}
		f, err := os.CreateTemp(c.out, ".push-*.apns")
		if err == nil {
			_, err = f.Write(body)
			err = errors.Join(err, f.Close())
			defer os.Remove(f.Name())
		}
		if err == nil {
			_, err = c.simctl(r.Context(), "push", c.udid, c.bundleID, f.Name())
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "e2emobile: push:", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		fmt.Fprintln(w, "delivered")
	})
	return mux
}
