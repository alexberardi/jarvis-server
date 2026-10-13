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
