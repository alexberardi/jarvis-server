package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/browser"
)

// TestMain lets a test run this binary's main() in a child process (as a browser would).
func TestMain(m *testing.M) {
	if os.Getenv("JARVIS_AGENT_TEST_MAIN") == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A browser starts the agent binary with its own argv; main() must become the native host,
// reach the agent's socket, and write nothing but native-messaging frames to stdout.
func TestBrowserStartsHostMode(t *testing.T) {
	dir, err := os.MkdirTemp("", "jba")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "s", "b.sock")
	ln, err := browser.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	hub := browser.NewHub(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = hub.Serve(ctx, ln) }()

	for _, argv := range [][]string{
		{"chrome-extension://" + browser.ChromeDevExtensionID + "/"},
		{"chrome-extension://" + browser.ChromeStoreExtensionID + "/", "--parent-window=0"},
		{filepath.Join(dir, "io.jarvis.agent.browser.json"), browser.FirefoxExtensionID},
	} {
		var in bytes.Buffer
		hello, _ := json.Marshal(browser.Message{Type: "hello", Protocol: browser.Protocol, Browser: "TestBrowser"})
		if err := browser.WriteNative(&in, hello, 1<<20); err != nil {
			t.Fatal(err)
		}
		stdinR, stdinW, _ := os.Pipe()
		stdoutR, stdoutW, _ := os.Pipe()
		cmd := exec.Command(os.Args[0], argv...)
		cmd.Env = append(os.Environ(), "JARVIS_AGENT_TEST_MAIN=1", browser.SocketEnv+"="+sock)
		cmd.Stdin = stdinR
		cmd.Stdout = stdoutW
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_ = stdoutW.Close() // the child has its own copy
		_, _ = stdinW.Write(in.Bytes())
		deadline := time.Now().Add(10 * time.Second) // a hang guard
		for !hub.Connected() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if bs := hub.Browsers(); len(bs) != 1 || bs[0].Browser != "TestBrowser" {
			t.Fatalf("%v: hub sees %+v", argv, bs)
		}
		// A command nobody answers: it reaches stdout as one native frame; the caller gives up
		// only once it has (not after a deadline the host may not have met).
		callCtx, callCancel := context.WithCancel(ctx)
		callErr := make(chan error, 1)
		go func() { _, _, err := hub.Call(callCtx, 0, "list_tabs", nil); callErr <- err }()
		frame := make(chan []byte, 1)
		go func() { msg, _ := browser.ReadNative(stdoutR, 1<<20); frame <- msg }()
		var msg []byte
		select {
		case msg = <-frame:
		case <-time.After(10 * time.Second):
			t.Fatalf("%v: the command never reached stdout", argv)
		}
		callCancel()
		if err := <-callErr; err == nil {
			t.Fatal("no extension answered, yet the call succeeded")
		}
		var m browser.Message
		if json.Unmarshal(msg, &m) != nil || m.Type != "command" || m.Cmd != "list_tabs" {
			t.Fatalf("%v: frame %q", argv, msg)
		}
		_ = stdinW.Close()             // the browser closes the port: the host exits 0
		rest, _ := io.ReadAll(stdoutR) // to EOF: the host has exited
		if err := cmd.Wait(); err != nil {
			t.Fatalf("%v: host exit: %v", argv, err)
		}
		_ = stdinR.Close()
		_ = stdoutR.Close()
		if len(rest) != 0 {
			t.Fatalf("%v: extra bytes on stdout: %q", argv, rest)
		}
		for hub.Connected() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// TestBrowserAnswerDefaultsToYes: the setup prompt is [Y/n]. Enter installs; "n" or anything
// else skips; input that ends without an answer (no one there) never installs.
func TestBrowserAnswerDefaultsToYes(t *testing.T) {
	for _, c := range []struct {
		line string
		err  error
		want bool
	}{
		{"\n", nil, true},
		{"  \n", nil, true},
		{"y\n", nil, true},
		{"YES\n", nil, true},
		{"yes", io.EOF, true},
		{"n\n", nil, false},
		{"no\n", nil, false},
		{"later\n", nil, false},
		{"", io.EOF, false},
	} {
		if got := browserAnswerYes(c.line, c.err); got != c.want {
			t.Errorf("browserAnswerYes(%q, %v) = %v, want %v", c.line, c.err, got, c.want)
		}
	}
}

// browser status prints each connected browser's website access (on, off, or nothing for an
// extension that doesn't report it), and how to turn it on for each one that has it off.
func TestBrowserLiveWebsiteAccess(t *testing.T) {
	on, off := true, false
	got := liveText(browser.StatusReply{Browsers: []browser.BrowserInfo{
		{Browser: "Firefox", Version: "140.0", ExtensionVersion: "0.3.2", Focused: true, WebsiteAccess: &off},
		{Browser: "Google Chrome", Version: "130", ExtensionVersion: "0.3.2", WebsiteAccess: &on},
		{Browser: "Brave", Version: "1", ExtensionVersion: "0.3.1"},
	}})
	for _, want := range []string{
		"Firefox 140.0 (extension 0.3.2), focused, website access off;",
		"Google Chrome 130 (extension 0.3.2), website access on;",
		"Brave 1 (extension 0.3.1)\n",
		"Firefox: website access is off, so Jarvis can't read or act on pages. To turn it on: " + browser.WebsiteAccessSteps("Firefox"),
	} {
		if !strings.Contains(got+"\n", want) {
			t.Errorf("status lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Google Chrome: website access is off") || strings.Contains(got, "Brave: website access") {
		t.Errorf("steps for a browser that doesn't need them:\n%s", got)
	}
	if !strings.Contains(browser.WebsiteAccessSteps("Firefox"), "about:addons") ||
		!strings.Contains(browser.WebsiteAccessSteps("Google Chrome"), "Site access") {
		t.Errorf("steps: %q / %q", browser.WebsiteAccessSteps("Firefox"), browser.WebsiteAccessSteps("Google Chrome"))
	}
}

// browser install ends with how to turn website access on: for every connected browser that has
// it off, and for Firefox (where it can be off from the install on) when Firefox hasn't
// connected yet to say.
func TestInstallAccessNote(t *testing.T) {
	on, off := true, false
	if got := installAccessNote(false, true, browser.StatusReply{}, nil); got != "" {
		t.Errorf("chromium only, nothing connected: %q", got)
	}
	got := installAccessNote(true, false, browser.StatusReply{}, errors.New("not running"))
	if !strings.Contains(got, browser.WebsiteAccessSteps("Firefox")) {
		t.Errorf("firefox, not connected: %q", got)
	}
	got = installAccessNote(true, true, browser.StatusReply{Browsers: []browser.BrowserInfo{{Browser: "Firefox", WebsiteAccess: &on}}}, nil)
	if got != "" {
		t.Errorf("firefox connected with access on: %q", got)
	}
	got = installAccessNote(true, true, browser.StatusReply{Browsers: []browser.BrowserInfo{
		{Browser: "Firefox", WebsiteAccess: &on}, {Browser: "Google Chrome", WebsiteAccess: &off},
	}}, nil)
	if !strings.Contains(got, "Google Chrome: website access is off") || !strings.Contains(got, browser.WebsiteAccessSteps("Google Chrome")) ||
		strings.Contains(got, "Firefox") {
		t.Errorf("chrome off: %q", got)
	}
}
