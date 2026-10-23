// Command fakespeech is a fake OpenAI-compatible speech provider for the harness: POST
// /v1/audio/speech answers a tone as 24 kHz s16le PCM (or WAV), records every request, and can
// be told to fail or to answer WAV at another rate, so the remote TTS path
// (internal/modules/tts/remote.go, docs/llm/07-remote-speech.md §5) runs end to end without a
// paid provider. API: server.go.
//
//	fakespeech [--listen ADDR] [--key KEY] [--log FILE]
//
// Point a sandbox at it: tts.provider=remote, tts.remote_url=http://ADDR/v1.
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
		fmt.Fprintln(os.Stderr, "fakespeech:", err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("fakespeech", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:17796", "address to serve on")
	key := fs.String("key", "", "the API key callers must send as a bearer token (empty: any)")
	logPath := fs.String("log", "", "append every request here, one JSON object per line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv := &Server{Key: *key}
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
	fmt.Fprintf(os.Stderr, "fakespeech: %s\n", *listen)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
