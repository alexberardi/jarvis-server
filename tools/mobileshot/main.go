// Command mobileshot is scripts/mobile-shot's driver: it walks the mobile app on one emulator
// (always addressed by serial) with plain adb (input, uiautomator dump, screencap), logs in,
// visits the screens in each theme and font scale, and writes PNGs, UI dumps and index.json.
//
//	mobileshot run --serial emulator-5580 --out DIR --email E --password P
//	               [--adb PATH] [--package com.jarvis.nodemobile] [--screens a,b]
//	               [--themes light,dark] [--font-scales 1.0,1.3] [--pages 3]
//	               [--chat TEXT] [--chat-expect RE] [--no-chat]
//	mobileshot dump --serial S        print the visible texts and testIDs on screen now
//
// The emulator, the APK and the adb reverse tunnels are the script's job.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Screens, in the order they are shot. landing and login are taken logged out; chat sends one
// message (once, in the first theme) and waits for the answer. inbox_detail opens the newest inbox
// item from the inbox modal, and is skipped when the inbox is empty.
var allScreens = []string{"landing", "login", "home", "devices", "pantry", "routines", "nodes", "settings", "inbox", "inbox_detail", "chat"}

// tabs maps a screen to its bottom-tab label.
var tabs = map[string]string{"home": "Home", "devices": "Devices", "pantry": "Pantry", "routines": "Routines", "nodes": "Nodes"}

// modals are reached from buttons on Home's header (testIDs), closed with Back.
var modals = map[string]string{"settings": "settings-button", "inbox": "inbox-button", "inbox_detail": "inbox-button"}

// selInboxItem is an inbox list card (InboxListScreen's testID inbox-item-<id>), not its
// selection checkbox.
var selInboxItem = MustSelector("id~^inbox-item-")

const defaultChat = "what is 12 times 7"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mobileshot run|dump ...")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	var err error
	switch os.Args[1] {
	case "run":
		err = runCmd(ctx, os.Args[2:])
	case "dump":
		err = dumpCmd(ctx, os.Args[2:])
	default:
		err = fmt.Errorf("unknown command %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "mobileshot:", err)
		os.Exit(1)
	}
}

type config struct {
	adb, serial, pkg, out, email, password string
	screens, themes, scales                []string
	pages                                  int
	chat, expect                           string
	noChat                                 bool
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseConfig validates the run flags.
func parseConfig(args []string) (*config, error) {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	c := &config{}
	var screens, themes, scales string
	fs.StringVar(&c.adb, "adb", "adb", "adb binary")
	fs.StringVar(&c.serial, "serial", "", "the emulator's serial (required)")
	fs.StringVar(&c.pkg, "package", "com.jarvis.nodemobile", "the app's package")
	fs.StringVar(&c.out, "out", "", "output directory (required)")
	fs.StringVar(&c.email, "email", "", "login email")
	fs.StringVar(&c.password, "password", "", "login password")
	fs.StringVar(&screens, "screens", strings.Join(allScreens, ","), "screens to shoot")
	fs.StringVar(&themes, "themes", "light,dark", "light, dark")
	fs.StringVar(&scales, "font-scales", "1.0", "Android font scales")
	fs.IntVar(&c.pages, "pages", 3, "screenfuls per screen: scroll down and shoot again while it moves")
	fs.StringVar(&c.chat, "chat", defaultChat, "the chat message")
	fs.StringVar(&c.expect, "chat-expect", "", "regexp the reply must match (default 84 for the default message)")
	fs.BoolVar(&c.noChat, "no-chat", false, "skip the chat screen")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if c.serial == "" || c.out == "" {
		return nil, errors.New("--serial and --out are required")
	}
	c.screens, c.themes, c.scales = splitList(screens), splitList(themes), splitList(scales)
	known := map[string]bool{}
	for _, s := range allScreens {
		known[s] = true
	}
	for _, s := range c.screens {
		if !known[s] {
			return nil, fmt.Errorf("--screens: unknown screen %q (%s)", s, strings.Join(allScreens, ", "))
		}
	}
	if c.noChat {
		c.screens = without(c.screens, "chat")
	}
	for _, t := range c.themes {
		if t != "light" && t != "dark" {
			return nil, fmt.Errorf("--themes: light or dark, not %q", t)
		}
	}
	scaleRE := regexp.MustCompile(`^[0-9](\.[0-9]{1,2})?$`)
	for _, s := range c.scales {
		if !scaleRE.MatchString(s) {
			return nil, fmt.Errorf("--font-scales: %q is not a scale like 1.0 or 1.3", s)
		}
	}
	if len(c.screens) == 0 || len(c.themes) == 0 || len(c.scales) == 0 {
		return nil, errors.New("nothing to shoot")
	}
	if c.expect == "" && c.chat == defaultChat {
		c.expect = `\b84\b`
	}
	if c.expect != "" {
		if _, err := regexp.Compile(c.expect); err != nil {
			return nil, fmt.Errorf("--chat-expect: %w", err)
		}
	}
	if c.pages < 1 {
		c.pages = 1
	}
	return c, nil
}

func without(list []string, drop string) []string {
	var out []string
	for _, s := range list {
		if s != drop {
			out = append(out, s)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// shotName is the file stem: <screen>__<theme>, then __f<scale> unless it is 1.0, then .p<n>
// for the screenfuls after the first.
func shotName(screen, theme, scale string, page int) string {
	n := screen + "__" + theme
	if scale != "1.0" && scale != "1" {
		n += "__f" + scale
	}
	if page > 1 {
		n += fmt.Sprintf(".p%d", page)
	}
	return n
}

// Shot is one screenshot in index.json.
type Shot struct {
	Screen    string   `json:"screen"`
	Theme     string   `json:"theme"`
	FontScale string   `json:"font_scale"`
	Page      int      `json:"page"`
	PNG       string   `json:"png"`
	XML       string   `json:"xml,omitempty"`
	Texts     []string `json:"texts"`
	Overflow  []string `json:"overflow,omitempty"`
	Luminance float64  `json:"luminance"`
	ThemeOK   bool     `json:"theme_ok"`
	Busy      bool     `json:"still_loading,omitempty"`
}

// ChatResult is the chat round trip in index.json.
type ChatResult struct {
	Message string  `json:"message"`
	Reply   string  `json:"reply,omitempty"`
	OK      bool    `json:"ok"`
	Seconds float64 `json:"seconds"`
	Error   string  `json:"error,omitempty"`
}

// Index is index.json.
type Index struct {
	Serial   string      `json:"serial"`
	Package  string      `json:"package"`
	Started  time.Time   `json:"started"`
	Seconds  float64     `json:"seconds"`
	LoggedIn bool        `json:"logged_in"`
	Chat     *ChatResult `json:"chat,omitempty"`
	Shots    []Shot      `json:"shots"`
	Skipped  []Skip      `json:"skipped,omitempty"`
	Failures []string    `json:"failures"`
}

// Skip is a screen that had nothing to show in one look; not a failure.
type Skip struct {
	Screen    string `json:"screen"`
	Theme     string `json:"theme"`
	FontScale string `json:"font_scale"`
	Reason    string `json:"reason"`
}

type runner struct {
	c   *config
	d   *Device
	idx *Index
}

func logf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "mobileshot: "+format+"\n", a...)
}

func (r *runner) fail(format string, a ...any) {
	msg := fmt.Sprintf(format, a...)
	logf("FAIL %s", msg)
	r.idx.Failures = append(r.idx.Failures, msg)
}

func runCmd(ctx context.Context, args []string) error {
	c, err := parseConfig(args)
	if err != nil {
		return err
	}
	d, err := NewDevice(c.serial, execRunner(c.adb))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.out, 0o755); err != nil {
		return err
	}
	r := &runner{c: c, d: d, idx: &Index{Serial: c.serial, Package: c.pkg, Started: time.Now(), Failures: []string{}}}
	r.run(ctx)
	r.idx.Seconds = time.Since(r.idx.Started).Seconds()
	_ = os.WriteFile(filepath.Join(c.out, "logcat.txt"), []byte(d.Logcat(ctx)), 0o644)
	b, _ := json.MarshalIndent(r.idx, "", "  ")
	if err := os.WriteFile(filepath.Join(c.out, "index.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}
	for _, s := range r.idx.Shots {
		if !s.ThemeOK {
			logf("warning: %s looks %s for a %s theme (luminance %.2f)", s.PNG, map[bool]string{true: "dark", false: "light"}[s.Luminance < 0.5], s.Theme, s.Luminance)
		}
		for _, o := range s.Overflow {
			logf("warning: %s: off the screen's edge: %s", s.PNG, o)
		}
	}
	if len(r.idx.Failures) > 0 {
		return fmt.Errorf("%d failure(s): %s", len(r.idx.Failures), strings.Join(r.idx.Failures, "; "))
	}
	return nil
}

type look struct{ theme, scale string }

func (r *runner) looks() []look {
	var out []look
	for _, s := range r.c.scales {
		for _, t := range r.c.themes {
			out = append(out, look{t, s})
		}
	}
	return out
}

var (
	selLogIn    = MustSelector("text=Log In")
	selEmail    = MustSelector("id=email-input")
	selPassword = MustSelector("id=password-input")
	selLoginBtn = MustSelector("id=login-button")
	selSend     = MustSelector("id=send-button")
	selComposer = MustSelector("hint~^Message Jarvis")
	selTools    = MustSelector("text~tools loaded$")
	selChangePw = MustSelector("text~(?i)change (your )?password")
)

func (r *runner) run(ctx context.Context) {
	ls := r.looks()
	// Logged out: the app was pm-cleared by the script.
	if contains(r.c.screens, "landing") || contains(r.c.screens, "login") {
		for _, l := range ls {
			if err := r.loggedOut(ctx, l); err != nil {
				r.fail("%s/%s logged-out screens: %v", l.theme, l.scale, err)
			}
		}
	}
	var inApp []string
	for _, s := range r.c.screens {
		if s != "landing" && s != "login" {
			inApp = append(inApp, s)
		}
	}
	if len(inApp) == 0 {
		return
	}
	if err := r.login(ctx, ls[0]); err != nil {
		r.fail("login: %v", err)
		return
	}
	r.idx.LoggedIn = true
	for i, l := range ls {
		if err := r.prepare(ctx, l); err != nil {
			r.fail("%s/%s: %v", l.theme, l.scale, err)
			continue
		}
		for _, s := range inApp {
			if s == "chat" {
				if i == 0 {
					r.chatRound(ctx, l)
				}
				continue
			}
			if err := r.visit(ctx, s, l); err != nil {
				r.fail("%s (%s, font %s): %v", s, l.theme, l.scale, err)
			}
		}
	}
}

// prepare applies a look and cold-starts the app into its main tabs.
func (r *runner) prepare(ctx context.Context, l look) error {
	logf("look: %s, font scale %s", l.theme, l.scale)
	if err := r.d.Look(ctx, l.theme, l.scale); err != nil {
		return err
	}
	if err := r.d.Launch(ctx, r.c.pkg); err != nil {
		return err
	}
	_, _, err := r.waitTab(ctx, "Home", 60*time.Second)
	return err
}

func (r *runner) loggedOut(ctx context.Context, l look) error {
	logf("look: %s, font scale %s (logged out)", l.theme, l.scale)
	if err := r.d.Look(ctx, l.theme, l.scale); err != nil {
		return err
	}
	if err := r.d.Launch(ctx, r.c.pkg); err != nil {
		return err
	}
	sc, btn, err := r.waitFor(ctx, 60*time.Second, selLogIn)
	if err != nil {
		return fmt.Errorf("landing: %w", err)
	}
	_ = sc
	if contains(r.c.screens, "landing") {
		if err := r.shoot(ctx, "landing", l, 1); err != nil {
			return err
		}
	}
	if contains(r.c.screens, "login") {
		if err := r.d.Tap(ctx, btn); err != nil {
			return err
		}
		if _, _, err := r.waitFor(ctx, 20*time.Second, selEmail); err != nil {
			return fmt.Errorf("login screen: %w", err)
		}
		if err := r.shoot(ctx, "login", l, 1); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) login(ctx context.Context, l look) error {
	if r.c.email == "" {
		return errors.New("no --email")
	}
	logf("logging in as %s", r.c.email)
	if err := r.d.Look(ctx, l.theme, l.scale); err != nil {
		return err
	}
	if err := r.d.Launch(ctx, r.c.pkg); err != nil {
		return err
	}
	_, btn, err := r.waitFor(ctx, 60*time.Second, selLogIn)
	if err != nil {
		return fmt.Errorf("landing: %w", err)
	}
	if err := r.d.Tap(ctx, btn); err != nil {
		return err
	}
	for _, f := range []struct {
		sel  Selector
		text string
	}{{selEmail, r.c.email}, {selPassword, r.c.password}} {
		_, n, err := r.waitFor(ctx, 20*time.Second, f.sel)
		if err != nil {
			return err
		}
		if err := r.d.Tap(ctx, n); err != nil {
			return err
		}
		if err := r.d.Type(ctx, f.text); err != nil {
			return err
		}
	}
	_, n, err := r.waitFor(ctx, 10*time.Second, selLoginBtn)
	if err != nil {
		return err
	}
	if err := r.d.Tap(ctx, n); err != nil {
		return err
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		sc, _, err := r.d.Dump(ctx)
		if err == nil {
			if _, ok := sc.Tab("Home"); ok {
				logf("logged in")
				return nil
			}
			if _, ok := sc.Find(selChangePw); ok {
				_ = r.shoot(ctx, "login-failed", l, 1)
				return errors.New("the app wants a password change (a temporary password?)")
			}
		}
		if err := sleep(ctx, time.Second); err != nil {
			return err
		}
	}
	_ = r.shoot(ctx, "login-failed", l, 1)
	return errors.New("no Home tab 60s after Log In (login-failed png and xml show why)")
}

func (r *runner) visit(ctx context.Context, screen string, l look) error {
	if label, ok := tabs[screen]; ok {
		_, tab, err := r.waitTab(ctx, label, 20*time.Second)
		if err != nil {
			return err
		}
		if err := r.d.Tap(ctx, tab); err != nil {
			return err
		}
		return r.shootScreen(ctx, screen, l)
	}
	id := modals[screen]
	_, home, err := r.waitTab(ctx, "Home", 20*time.Second)
	if err != nil {
		return err
	}
	if err := r.d.Tap(ctx, home); err != nil {
		return err
	}
	_, btn, err := r.waitFor(ctx, 20*time.Second, MustSelector("id="+id))
	if err != nil {
		return err
	}
	if err := r.d.Tap(ctx, btn); err != nil {
		return err
	}
	if err := sleep(ctx, 1500*time.Millisecond); err != nil {
		return err
	}
	if screen == "inbox_detail" {
		err = r.inboxDetail(ctx, l)
	} else {
		err = r.shootScreen(ctx, screen, l)
	}
	_ = r.d.Key(ctx, "KEYCODE_BACK")
	_ = sleep(ctx, time.Second)
	return err
}

// inboxDetail opens the first (newest) card of the open inbox modal, shoots it and goes back
// to the list. An empty inbox is recorded as skipped.
func (r *runner) inboxDetail(ctx context.Context, l look) error {
	sc, _, err := r.settle(ctx, 20*time.Second)
	if err != nil {
		return err
	}
	item, ok := firstInboxItem(sc)
	if !ok {
		r.skip("inbox_detail", l, "no inbox items")
		return nil
	}
	if err := r.d.Tap(ctx, item); err != nil {
		return err
	}
	if err := sleep(ctx, 1500*time.Millisecond); err != nil {
		return err
	}
	err = r.shootScreen(ctx, "inbox_detail", l)
	_ = r.d.Key(ctx, "KEYCODE_BACK")
	_ = sleep(ctx, time.Second)
	return err
}

func firstInboxItem(sc *Screen) (Node, bool) { return sc.Find(selInboxItem) }

func (r *runner) skip(screen string, l look, reason string) {
	logf("skip %s (%s, font %s): %s", screen, l.theme, l.scale, reason)
	r.idx.Skipped = append(r.idx.Skipped, Skip{Screen: screen, Theme: l.theme, FontScale: l.scale, Reason: reason})
}

// chatRound sends one message from Home and waits for the answer.
func (r *runner) chatRound(ctx context.Context, l look) {
	res := &ChatResult{Message: r.c.chat}
	r.idx.Chat = res
	t0 := time.Now()
	err := func() error {
		_, home, err := r.waitTab(ctx, "Home", 20*time.Second)
		if err != nil {
			return err
		}
		if err := r.d.Tap(ctx, home); err != nil {
			return err
		}
		// The composer is enabled once a node is online and its tools are in.
		sc, _, err := r.waitFor(ctx, 120*time.Second, selComposer, selTools)
		if err != nil {
			return fmt.Errorf("the composer never became ready (no online node, or tools never loaded): %w", err)
		}
		box, ok := editText(sc)
		if !ok {
			return errors.New("no text box on Home")
		}
		if err := r.d.Tap(ctx, box); err != nil {
			return err
		}
		if err := r.d.Type(ctx, r.c.chat); err != nil {
			return err
		}
		_, send, err := r.waitFor(ctx, 10*time.Second, selSend)
		if err != nil {
			return err
		}
		if err := r.d.Tap(ctx, send); err != nil {
			return err
		}
		logf("chat: sent %q, waiting for the reply", r.c.chat)
		var expect *regexp.Regexp
		if r.c.expect != "" {
			expect = regexp.MustCompile(r.c.expect)
		}
		deadline := time.Now().Add(240 * time.Second)
		last, stable := "", 0
		for time.Now().Before(deadline) {
			if err := sleep(ctx, 2*time.Second); err != nil {
				return err
			}
			sc, _, err := r.d.Dump(ctx)
			if err != nil {
				continue
			}
			reply := replyAfter(sc, r.c.chat)
			if reply == "" || sc.Busy() {
				continue
			}
			if reply == last {
				stable++
			} else {
				last, stable = reply, 0
			}
			// Done once the reply has stopped growing (streamed) for two polls.
			if stable >= 2 {
				res.Reply = reply
				if expect != nil && !expect.MatchString(reply) {
					return fmt.Errorf("reply %q does not match %s", reply, r.c.expect)
				}
				return nil
			}
		}
		res.Reply = last
		return errors.New("no reply within 240s")
	}()
	res.Seconds = time.Since(t0).Seconds()
	if shotErr := r.shoot(ctx, "chat", l, 1); shotErr != nil && err == nil {
		err = shotErr
	}
	if err != nil {
		res.Error = err.Error()
		r.fail("chat: %v", err)
		return
	}
	res.OK = true
	logf("chat: reply in %.0fs: %q", res.Seconds, res.Reply)
}

func editText(sc *Screen) (Node, bool) {
	for _, n := range sc.Nodes {
		if n.Class == "android.widget.EditText" && !n.Bounds.Empty() {
			return n, true
		}
	}
	return Node{}, false
}

// replyAfter is the text of the views after the last bubble holding the sent message, above the
// composer: the assistant's answer (and any status line), joined.
func replyAfter(sc *Screen, sent string) string {
	at := -1
	for i, n := range sc.Nodes {
		if strings.TrimSpace(n.Text) == sent && n.Class != "android.widget.EditText" {
			at = i
		}
	}
	if at < 0 {
		return ""
	}
	var parts []string
	for _, n := range sc.Nodes[at+1:] {
		if n.Class == "android.widget.EditText" {
			break
		}
		if t := strings.TrimSpace(n.Text); t != "" && !iconGlyph(t) {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

func (r *runner) waitTab(ctx context.Context, label string, timeout time.Duration) (*Screen, Node, error) {
	deadline := time.Now().Add(timeout)
	for {
		sc, _, err := r.d.Dump(ctx)
		if err == nil {
			if n, ok := sc.Tab(label); ok {
				return sc, n, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, Node{}, fmt.Errorf("no %q tab after %s", label, timeout)
		}
		if err := sleep(ctx, 700*time.Millisecond); err != nil {
			return nil, Node{}, err
		}
	}
}

// waitFor polls until any selector matches, returning the screen and the first match.
func (r *runner) waitFor(ctx context.Context, timeout time.Duration, sels ...Selector) (*Screen, Node, error) {
	deadline := time.Now().Add(timeout)
	for {
		sc, _, err := r.d.Dump(ctx)
		if err == nil {
			for _, s := range sels {
				if n, ok := sc.Find(s); ok {
					return sc, n, nil
				}
			}
		}
		if time.Now().After(deadline) {
			names := make([]string, len(sels))
			for i, s := range sels {
				names[i] = s.String()
			}
			return nil, Node{}, fmt.Errorf("%s not on screen after %s", strings.Join(names, " or "), timeout)
		}
		if err := sleep(ctx, 700*time.Millisecond); err != nil {
			return nil, Node{}, err
		}
	}
}

// settle waits until two dumps in a row agree and no spinner shows (or the timeout), and
// returns the last dump.
func (r *runner) settle(ctx context.Context, timeout time.Duration) (*Screen, []byte, error) {
	deadline := time.Now().Add(timeout)
	var prev string
	for {
		sc, raw, err := r.d.Dump(ctx)
		if err != nil {
			return nil, nil, err
		}
		sig := sc.Signature()
		if (sig == prev && !sc.Busy()) || time.Now().After(deadline) {
			return sc, raw, nil
		}
		prev = sig
		if err := sleep(ctx, 600*time.Millisecond); err != nil {
			return nil, nil, err
		}
	}
}

// shootScreen shoots the screen, then scrolls and shoots again while the content moves, up to
// --pages screenfuls.
func (r *runner) shootScreen(ctx context.Context, screen string, l look) error {
	if err := r.shoot(ctx, screen, l, 1); err != nil {
		return err
	}
	for p := 2; p <= r.c.pages; p++ {
		sc, _, err := r.d.Dump(ctx)
		if err != nil {
			return err
		}
		if !scrollable(sc) {
			return nil
		}
		before := sc.Signature()
		x := sc.W / 2
		if err := r.d.Swipe(ctx, x, sc.H*7/10, sc.H*3/10, 400); err != nil {
			return err
		}
		_ = sleep(ctx, 800*time.Millisecond)
		after, _, err := r.settle(ctx, 8*time.Second)
		if err != nil {
			return err
		}
		if after.Signature() == before {
			return nil
		}
		if err := r.shoot(ctx, screen, l, p); err != nil {
			return err
		}
	}
	return nil
}

func scrollable(sc *Screen) bool {
	for _, n := range sc.Nodes {
		if n.Scrollable && !n.Bounds.Empty() {
			return true
		}
	}
	return false
}

func (r *runner) shoot(ctx context.Context, screen string, l look, page int) error {
	// SystemUI restarts some time after a night-mode switch and forgets demo mode.
	r.d.FreezeStatusBar(ctx)
	sc, raw, err := r.settle(ctx, 20*time.Second)
	if err != nil {
		return err
	}
	name := shotName(screen, l.theme, l.scale, page)
	png := filepath.Join(r.c.out, name+".png")
	if err := r.d.Screenshot(ctx, png); err != nil {
		return err
	}
	xmlPath := filepath.Join(r.c.out, name+".xml")
	_ = os.WriteFile(xmlPath, raw, 0o644)
	lum, err := fileLuminance(png)
	if err != nil {
		return err
	}
	s := Shot{Screen: screen, Theme: l.theme, FontScale: l.scale, Page: page, PNG: name + ".png", XML: name + ".xml",
		Texts: visibleTexts(sc), Overflow: sc.Overflow(), Luminance: round2(lum), ThemeOK: ThemeMatches(l.theme, lum), Busy: sc.Busy()}
	r.idx.Shots = append(r.idx.Shots, s)
	logf("shot %s", png)
	return nil
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }

// iconGlyph is an icon font's character (Material Community Icons live in the Private Use Area),
// which tells a reader nothing.
func iconGlyph(s string) bool {
	for _, r := range s {
		if !(r >= 0xE000 && r <= 0xF8FF) && !(r >= 0xF0000 && r <= 0x10FFFF) {
			return false
		}
	}
	return true
}

func visibleTexts(sc *Screen) []string {
	var out []string
	for _, t := range sc.Texts() {
		if !iconGlyph(t) {
			out = append(out, t)
		}
	}
	return out
}

func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

func dumpCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("dump", flag.ContinueOnError)
	adb := fs.String("adb", "adb", "adb binary")
	serial := fs.String("serial", "", "the emulator's serial")
	if err := fs.Parse(args); err != nil {
		return err
	}
	d, err := NewDevice(*serial, execRunner(*adb))
	if err != nil {
		return err
	}
	sc, _, err := d.Dump(ctx)
	if err != nil {
		return err
	}
	for _, n := range sc.Nodes {
		l := strings.TrimSpace(n.Label())
		id := n.ID
		if (l == "" || iconGlyph(l)) && (id == "" || strings.Contains(id, "android:id/")) {
			continue
		}
		fmt.Printf("%-28s %-22s %q hint=%q %v\n", strings.TrimPrefix(n.Class, "android.widget."), id, l, n.Hint, n.Bounds)
	}
	return nil
}
