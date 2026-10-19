// Command ccsites serves the computer-task suite's fixture websites on one port: a store (/store/:
// search, compare, cart and checkout with a saved card), a bank and a card issuer (/bank/, /card/:
// monthly statements with a CSV download) and a web form (/form/), plus a control API (/control/)
// that reports what an errand did (orders placed, files downloaded, forms sent) and hands out the
// session cookies that stand in for saved logins. Everything is seeded and deterministic. Pages and
// API: server.go; plan: docs/agent/COMPUTER-CONTROL.md.
//
//	ccsites [--listen ADDR] [--state DIR]
//
// It prints "ccsites: http://ADDR" on stdout once it listens, so --listen 127.0.0.1:0 works.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "ccsites:", err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("ccsites", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:17799", "address to serve on (port 0 picks one)")
	stateDir := fs.String("state", "", "keep the state in DIR/state.json across restarts (default: memory only)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv, err := NewServer(*stateDir)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	fmt.Printf("ccsites: http://%s\n", ln.Addr())
	if err := hs.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
