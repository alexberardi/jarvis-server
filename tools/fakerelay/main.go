// Command fakerelay is a fake push relay for the harness: it speaks the relay protocol jarvisd's
// notifications module uses (POST /v1/register, POST /v1/send) and records every push (title,
// body, data, priority, target tokens) instead of handing it to Expo, so E2E tests can assert
// which push an event sent. scripts/sandbox up starts one on every sandbox (port 7797 + offset)
// and points relay.url at it; tests read $SBX_FAKERELAY/control/pushes. API: server.go;
// decision: docs/decisions/2026-10-18-e2e-push.md.
//
//	fakerelay [--listen ADDR] [--log FILE]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
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
		fmt.Fprintln(os.Stderr, "fakerelay:", err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fakerelay", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:17797", "address to serve on")
	logPath := fs.String("log", "", "append every recorded push here, one JSON object per line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv := &Server{}
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		srv.Log = f
	}
	hs := &http.Server{Addr: *listen, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	fmt.Fprintf(os.Stderr, "fakerelay: %s\n", *listen)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
