package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The real push lane (scripts/e2e mobile --real-push, docs/decisions/2026-10-18-e2e-push.md):
// the app signs in on a Google Play emulator and registers its real Expo token, a node event
// sends a push through the real relay, Expo and FCM, and the test finds the notification in
// the shade, taps it and checks that the app opened the item.

const appPackage = "com.jarvis.nodemobile"

type pushConfig struct {
	config
	loginFlow     string // the mobile repo's login subflow
	notifications string // the sandbox's notifications URL
	cc            string // the sandbox's cc URL
	userToken     string // the signed-in user's JWT (whose devices the app registers)
	userID        int64
	nodeKey       string // X-API-Key of a node in that user's household: "id:key"
	skipReason    string // non-empty: record the lane as skipped and run nothing

	tokenWait, shadeWait, openWait, poll time.Duration
}

// uiNode is one view in a uiautomator dump.
type uiNode struct {
	Text     string   `xml:"text,attr"`
	Desc     string   `xml:"content-desc,attr"`
	Package  string   `xml:"package,attr"`
	Bounds   string   `xml:"bounds,attr"`
	Children []uiNode `xml:"node"`
}

// findText is the first view whose text or content description contains text.
func findText(dump []byte, text string) (uiNode, bool) {
	var root struct {
		Nodes []uiNode `xml:"node"`
	}
	if xml.Unmarshal(dump, &root) != nil {
		return uiNode{}, false
	}
	var walk func([]uiNode) (uiNode, bool)
	walk = func(ns []uiNode) (uiNode, bool) {
		for _, n := range ns {
			if strings.Contains(n.Text, text) || strings.Contains(n.Desc, text) {
				return n, true
			}
			if m, ok := walk(n.Children); ok {
				return m, true
			}
		}
		return uiNode{}, false
	}
	return walk(root.Nodes)
}

// center is the middle of uiautomator bounds "[x1,y1][x2,y2]".
func center(bounds string) (int, int, error) {
	var x1, y1, x2, y2 int
	if _, err := fmt.Sscanf(bounds, "[%d,%d][%d,%d]", &x1, &y1, &x2, &y2); err != nil {
		return 0, 0, fmt.Errorf("bounds %q: %w", bounds, err)
	}
	return (x1 + x2) / 2, (y1 + y2) / 2, nil
}

// realPush runs the lane and writes <out>/results.json (suite mobile-push, one test).
func realPush(ctx context.Context, pc *pushConfig) (*Results, error) {
	if pc.serial == "" || pc.serial == userSerial {
		return nil, fmt.Errorf("refusing serial %q: the harness only drives its own emulator", pc.serial)
	}
	if err := os.MkdirAll(pc.out, 0o755); err != nil {
		return nil, err
	}
	t0 := time.Now()
	r := Record{Suite: "mobile-push", ID: "real-push", Title: "a real push reaches the shade and its tap opens the inbox item",
		File: "tools/e2emobile/realpush.go", Line: 1, Status: "skipped", Screenshots: []string{}, ConsoleErrors: []string{}}
	if pc.skipReason != "" {
		r.SkipReason = pc.skipReason
	} else {
		base := r
		r = pc.retryOnDrop(ctx, base, func() (Record, string) {
			r := pc.lane(ctx, base)
			if r.Error != nil {
				return r, *r.Error
			}
			return r, ""
		})
	}
	r.DurationMS = time.Since(t0).Milliseconds()
	res := &Results{Suite: "mobile-push", Status: "passed", DurationMS: r.DurationMS, Tests: []Record{r}, DeviceRetries: pc.retries}
	if r.Status != "passed" && r.Status != "skipped" {
		res.Status = "failed"
	}
	raw, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return nil, err
	}
	return res, os.WriteFile(filepath.Join(pc.out, "results.json"), append(raw, '\n'), 0o644)
}

func (pc *pushConfig) lane(ctx context.Context, r Record) Record {
	rel := func(p string) string {
		if s, err := filepath.Rel(filepath.Dir(pc.out), p); err == nil {
			return filepath.ToSlash(s)
		}
		return p
	}
	// keep saves the screen as name.png and name.xml; it returns the dump.
	keep := func(name string) []byte {
		dump := pc.dump(ctx)
		if len(dump) > 0 {
			p := filepath.Join(pc.out, name+".xml")
			if os.WriteFile(p, dump, 0o644) == nil {
				r.Attachments = append(r.Attachments, rel(p))
			}
		}
		if out, err := pc.adbCmd(ctx, "exec-out", "screencap -p"); err == nil && bytes.HasPrefix(out, []byte("\x89PNG")) {
			p := filepath.Join(pc.out, name+".png")
			if os.WriteFile(p, out, 0o644) == nil {
				r.Screenshots = append(r.Screenshots, rel(p))
			}
		}
		return dump
	}
	fail := func(msg string, screen string) Record {
		if screen != "" {
			keep(screen)
		}
		if out, err := pc.adbCmd(ctx, "logcat", "-d"); err == nil {
			p := filepath.Join(pc.out, "logcat.txt")
			if os.WriteFile(p, out, 0o644) == nil {
				r.Attachments = append(r.Attachments, rel(p))
			}
		}
		return failed(r, msg)
	}
	_, _ = pc.adbCmd(ctx, "logcat", "-c")

	// 1. Sign in with the mobile repo's own login subflow; the app then registers its token. Only
	// a token that was not there before counts: a reused sandbox may still hold an old one.
	before, err := pc.tokens(ctx)
	if err != nil {
		return fail("listing the user's push tokens failed: "+err.Error(), "")
	}
	if err := pc.login(ctx); err != nil {
		return fail("signing in failed: "+err.Error(), "login-failed")
	}
	if err := pc.until(ctx, pc.tokenWait, func(ctx context.Context) error { return pc.hasNewToken(ctx, before) }); err != nil {
		return fail(fmt.Sprintf("the app registered no push token in %s (FCM on the emulator? the APK's push config?): %v",
			pc.tokenWait, err), "no-token")
	}

	// 2. The app in the background, so the push is shown in the shade, then the event.
	if _, err := pc.adbCmd(ctx, "shell", "input keyevent KEYCODE_HOME"); err != nil {
		return fail(err.Error(), "")
	}
	title := fmt.Sprintf("E2E real push %s", time.Now().UTC().Format("150405.000"))
	if err := pc.postEvent(ctx, title); err != nil {
		return fail("posting the event failed: "+err.Error(), "")
	}

	// 3. Wait for it in the shade.
	var n uiNode
	if err := pc.until(ctx, pc.shadeWait, func(ctx context.Context) error {
		if _, err := pc.adbCmd(ctx, "shell", "cmd statusbar expand-notifications"); err != nil {
			return err
		}
		var ok bool
		if n, ok = findText(pc.dump(ctx), title); !ok {
			return errors.New("not in the shade yet")
		}
		return nil
	}); err != nil {
		return fail(fmt.Sprintf("the push %q did not reach the notification shade in %s", title, pc.shadeWait), "shade")
	}
	keep("shade")

	// 4. Tap it; the app must open the item.
	x, y, err := center(n.Bounds)
	if err != nil {
		return fail(err.Error(), "")
	}
	if _, err := pc.adbCmd(ctx, "shell", fmt.Sprintf("input tap %d %d", x, y)); err != nil {
		return fail(err.Error(), "")
	}
	if err := pc.until(ctx, pc.openWait, func(ctx context.Context) error {
		if m, ok := findText(pc.dump(ctx), title); !ok || m.Package != appPackage {
			return errors.New("not open yet")
		}
		return nil
	}); err != nil {
		return fail(fmt.Sprintf("tapping the notification did not open %q in the app in %s", title, pc.openWait), "after-tap")
	}
	keep("opened")
	r.Status = "passed"
	return r
}

// until retries f every pc.poll until it succeeds or wait runs out (its last error).
func (pc *pushConfig) until(ctx context.Context, wait time.Duration, f func(context.Context) error) error {
	deadline := time.Now().Add(wait)
	for {
		err := f(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pc.poll):
		}
	}
}

func (pc *pushConfig) dump(ctx context.Context) []byte {
	if _, err := pc.adbCmd(ctx, "shell", "uiautomator dump /data/local/tmp/e2emobile.xml >/dev/null"); err != nil {
		return nil
	}
	out, _ := pc.adbCmd(ctx, "exec-out", "cat /data/local/tmp/e2emobile.xml")
	return out
}

func (pc *pushConfig) login(ctx context.Context) error {
	flow := filepath.Join(pc.out, "login.yaml")
	body := "appId: " + appPackage + "\nname: real push sign-in\n---\n- launchApp:\n    clearState: true\n- runFlow: " +
		strconv.Quote(pc.loginFlow) + "\n"
	if err := os.WriteFile(flow, []byte(body), 0o644); err != nil {
		return err
	}
	args := []string{"--device", pc.serial, "test", flow}
	for _, kv := range pc.env {
		args = append(args, "-e", kv)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var log bytes.Buffer
	cmd := exec.CommandContext(ctx, pc.maestro, args...)
	cmd.Stdout, cmd.Stderr = &log, &log
	cmd.WaitDelay = 10 * time.Second
	err := cmd.Run()
	_ = os.WriteFile(filepath.Join(pc.out, "login-maestro.log"), log.Bytes(), 0o644)
	if err != nil {
		return fmt.Errorf("%w: %s", err, tail(log.String(), 10))
	}
	return nil
}

// tokens is the set of the user's registered push tokens.
func (pc *pushConfig) tokens(ctx context.Context) (map[string]bool, error) {
	var toks []struct {
		PushToken string `json:"push_token"`
	}
	if err := pc.api(ctx, http.MethodGet, pc.notifications+"/api/v0/tokens/me", map[string]string{"Authorization": "Bearer " + pc.userToken}, nil, &toks); err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, t := range toks {
		set[t.PushToken] = true
	}
	return set, nil
}

// hasNewToken succeeds once the user has a token that is not in before.
func (pc *pushConfig) hasNewToken(ctx context.Context, before map[string]bool) error {
	now, err := pc.tokens(ctx)
	if err != nil {
		return err
	}
	for t := range now {
		if !before[t] {
			return nil
		}
	}
	return errors.New("no new device token registered")
}

func (pc *pushConfig) postEvent(ctx context.Context, title string) error {
	var out struct {
		ID   *string `json:"id"`
		Sent bool    `json:"sent"`
	}
	body := map[string]any{"title": title, "summary": "Sent by scripts/e2e mobile --real-push", "body": "Sent by scripts/e2e mobile --real-push.",
		"category": "alert", "create_push_notification": true, "user_id": pc.userID, "target_type": "user"}
	if err := pc.api(ctx, http.MethodPost, pc.cc+"/api/v0/node/inbox-item", map[string]string{"X-API-Key": pc.nodeKey}, body, &out); err != nil {
		return err
	}
	if !out.Sent {
		return errors.New("cc did not send it (withheld?)")
	}
	return nil
}

func (pc *pushConfig) api(ctx context.Context, method, url string, headers map[string]string, body, out any) error {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, rd)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: HTTP %d", method, url, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
