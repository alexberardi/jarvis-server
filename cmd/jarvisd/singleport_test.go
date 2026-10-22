package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"runtime"
	"testing"
	"time"

	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/logging"
)

// A fresh install listens on the config port, the admin and the MQTT broker only; every other
// module is reached under its path on the config port, and /services says so.
func TestFreshServeListensOnThreePorts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("dialing a closed port on Windows retries instead of being refused")
	}
	// 20000 keeps every port (7700–9883 + offset) below 32768, outside Linux's
	// ephemeral range (32768–60999) and macOS/Windows' (49152–65535), so a
	// parallel test's outbound connection cannot grab one.
	const offset = 20000
	home := t.TempDir()
	t.Setenv("JARVIS_HOME", home)
	t.Setenv("JARVIS_PORT_OFFSET", fmt.Sprint(offset))
	t.Setenv("JARVIS_HOST", "127.0.0.1")
	t.Setenv("JARVIS_MDNS", "0")
	t.Setenv("JARVIS_NO_BROWSER", "1")
	t.Cleanup(func() { logging.SetShuttingDown(false) })

	// Skip rather than flake if something else already holds a port.
	for _, p := range []int{7700 + offset, 7710 + offset, 1884 + offset} {
		l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p))
		if err != nil {
			t.Skipf("port %d held: %v", p, err)
		}
		l.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- serve(ctx, false, false) }()
	var returned bool
	t.Cleanup(func() {
		if returned {
			return
		}
		cancel()
		select {
		case err := <-done:
			// A cancel while a slow (-race) start is still running ends it with context.Canceled.
			if err != nil && !errors.Is(err, context.Canceled) {
				t.Errorf("serve: %v", err)
			}
		case <-time.After(60 * time.Second): // hang guard
			t.Error("serve did not return after cancel")
		}
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", 7700+offset)
	client := &http.Client{Timeout: 2 * time.Second}
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		// /health answers once the listeners bind; the setup token once auth has started.
		if resp, err := client.Get(base + "/health"); err == nil {
			resp.Body.Close()
			if _, err := os.Stat(authmod.SetupTokenPath(home)); resp.StatusCode == 200 && err == nil {
				break
			}
		}
		select {
		case err := <-done:
			returned = true
			t.Fatalf("serve returned before /health answered: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("/health never answered")
		}
	}

	dial := func(port int) error {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
		}
		return err
	}
	open := map[int]bool{7700: true, 7710: true, 1884: true}
	for l, p := range config.DefaultPorts {
		if err := dial(p + offset); open[p] && err != nil {
			t.Errorf("%s (%d) refused: %v", l, p+offset, err)
		} else if !open[p] && err == nil {
			t.Errorf("%s (%d) accepts with legacy ports off", l, p+offset)
		}
	}
	// The broker starts with cc, right after the HTTP listeners bind.
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		err := dial(1884 + offset)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("mqtt (%d) refused: %v", 1884+offset, err)
		}
	}
	if err := dial(9883 + offset); err == nil {
		t.Errorf("mqtt-ws (%d) accepts with legacy ports off", 9883+offset)
	}

	resp, err := client.Get(base + "/services")
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Services []struct{ Name, URL string } `json:"services"`
	}
	err = json.NewDecoder(resp.Body).Decode(&body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	var ccURL string
	for _, s := range body.Services {
		if s.Name == "jarvis-command-center" {
			ccURL = s.URL
		}
	}
	if want := fmt.Sprintf(":%d/cc", 7700+offset); len(ccURL) < len(want) || ccURL[len(ccURL)-len(want):] != want {
		t.Errorf("jarvis-command-center url %q, want …%s", ccURL, want)
	}

	resp, err = client.Get(base + "/cc/health")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("/cc/health = %d", resp.StatusCode)
	}
}
