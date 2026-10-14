package main

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/jarvispage"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// The Jarvis page guard (DESIGN.md §14 "The Jarvis page in front"): dictation and the
// browser's fill and click never act on this agent's own server's web page, where the user
// may be chatting. The agent knows the server's config URL at once and the web UI's address
// once discovery answers.

// addServerOrigins adds the discovered web UI and command-center addresses.
func addServerOrigins(o *jarvispage.Origins, svcs node.Services) {
	for _, name := range []string{svcWebUI, node.SvcCommandCenter} {
		if u := svcs[name]; u != "" {
			o.Add(u)
		}
	}
}

// learnServer adds the config URL now and the discovered addresses once jarvisd answers,
// retrying while it can't be reached.
func learnServer(ctx context.Context, configURL string, o *jarvispage.Origins, log *slog.Logger) {
	o.Add(configURL)
	hc := &http.Client{Timeout: 10 * time.Second}
	backoff := 2 * time.Second
	for {
		svcs, err := node.Discover(ctx, hc, configURL)
		if err == nil {
			addServerOrigins(o, svcs)
			return
		}
		log.Debug("jarvis page guard: discovery failed; retrying", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 2*time.Minute)
	}
}

// tryOrigins is try's server: --server, else the enrolled one's, with the web UI's address
// when discovery answers within a few seconds.
func tryOrigins(ctx context.Context, server string) *jarvispage.Origins {
	o := &jarvispage.Origins{}
	if server == "" {
		path, err := credsPath("")
		if err != nil {
			return o
		}
		c, err := node.LoadCreds(filepath.Clean(path))
		if err != nil {
			return o
		}
		server = c.ConfigURL
	}
	o.Add(server)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if svcs, err := node.Discover(ctx, &http.Client{}, server); err == nil {
		addServerOrigins(o, svcs)
	}
	return o
}
