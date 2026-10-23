// Command fakespeech is a fake OpenAI-compatible speech provider for the harness: it answers
// POST /v1/audio/transcriptions with canned text and POST /v1/audio/speech with a tone (24 kHz
// s16le PCM or WAV), records every request (never the key) and can be told to fail, so jarvisd's
// remote stt and tts run end to end without a paid provider (docs/llm/07-remote-speech.md §5).
// scripts/sandbox up --speech fake starts one (port 7796 + offset) and points stt.remote_url at
// it; tests read $SBX_FAKESPEECH/control/requests. API: server.go.
//
//	fakespeech [--listen ADDR] [--key KEY] [--log FILE]
//
// Point tts at it by hand: tts.provider=remote, tts.remote_url=http://ADDR/v1.
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
	key := fs.String("key", os.Getenv("FAKESPEECH_KEY"), "the only API key accepted (default $FAKESPEECH_KEY; empty = any)")
	logPath := fs.String("log", "", "append every recorded request here, one JSON object per line")
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
