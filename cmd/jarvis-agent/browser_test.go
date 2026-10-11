package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
		{"chrome-extension://" + browser.ChromeExtensionID + "/"},
		{filepath.Join(dir, "io.jarvis.agent.browser.json"), browser.FirefoxExtensionID},
	} {
		var in bytes.Buffer
		hello, _ := json.Marshal(browser.Message{Type: "hello", Protocol: browser.Protocol, Browser: "TestBrowser"})
		if err := browser.WriteNative(&in, hello, 1<<20); err != nil {
			t.Fatal(err)
		}
		stdinR, stdinW, _ := os.Pipe()
		var stdout bytes.Buffer
		cmd := exec.Command(os.Args[0], argv...)
		cmd.Env = append(os.Environ(), "JARVIS_AGENT_TEST_MAIN=1", browser.SocketEnv+"="+sock)
		cmd.Stdin = stdinR
		cmd.Stdout = &stdout
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		_, _ = stdinW.Write(in.Bytes())
		deadline := time.Now().Add(10 * time.Second)
		for !hub.Connected() && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if bs := hub.Browsers(); len(bs) != 1 || bs[0].Browser != "TestBrowser" {
			t.Fatalf("%v: hub sees %+v", argv, bs)
		}
		callCtx, callCancel := context.WithTimeout(ctx, 300*time.Millisecond)
		_, _, err := hub.Call(callCtx, 0, "list_tabs", nil)
		callCancel()
		if err == nil {
			t.Fatal("no extension answered, yet the call succeeded")
		}
		// The command reached stdout as one native frame.
		_ = stdinW.Close() // the browser closes the port: the host exits 0
		if err := cmd.Wait(); err != nil {
			t.Fatalf("%v: host exit: %v", argv, err)
		}
		_ = stdinR.Close()
		msg, err := browser.ReadNative(&stdout, 1<<20)
		if err != nil {
			t.Fatalf("%v: stdout is not native frames: %v (%q)", argv, err, stdout.String())
		}
		var m browser.Message
		if json.Unmarshal(msg, &m) != nil || m.Type != "command" || m.Cmd != "list_tabs" {
			t.Fatalf("%v: frame %q", argv, msg)
		}
		if stdout.Len() != 0 {
			t.Fatalf("%v: extra bytes on stdout: %q", argv, stdout.String())
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
