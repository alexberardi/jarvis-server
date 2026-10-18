// Command scriptllm is a scripted model for UI tests: an OpenAI-compatible chat server that
// answers from a script instead of a model, so a test sees the same words and tool calls on
// every run. scripts/sandbox up --llm scripted starts it (port 7798 + offset) behind the LLM
// tap and points the sandbox's llm.live at it.
//
//	scriptllm [--listen ADDR] [--script FILE] [--dir DIR] [--log FILE] [--context N]
//
// A script (e2e/llm/*.json) is a list of rules, each a regexp on the last user message (or,
// with after_tool, on a tool's result) and a reply and/or tool calls; a turn no rule matches
// gets the script's fallback and is recorded in --log and at GET /control/unmatched. A test
// switches scripts with POST /control/script. Format and API:
// docs/decisions/2026-10-17-scripted-llm.md and server.go.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "scriptllm:", err)
		os.Exit(2)
	}
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("scriptllm", flag.ContinueOnError)
	listen := fs.String("listen", "127.0.0.1:17798", "address to serve on")
	script := fs.String("script", "e2e/llm/default.json", "the script to start with (relative to --dir)")
	dir := fs.String("dir", ".", "where relative script paths are resolved (the repo root)")
	logPath := fs.String("log", "", "append unmatched turns here, one JSON object per line")
	ctxLen := fs.Int("context", 32768, "context_length reported by /v1/models")
	if err := fs.Parse(args); err != nil {
		return err
	}
	srv := &Server{Dir: *dir, Context: *ctxLen}
	path := *script
	if !filepath.IsAbs(path) {
		path = filepath.Join(*dir, path)
	}
	sc, err := LoadScript(path)
	if err != nil {
		return err
	}
	srv.Use(sc, path)
	var log io.Writer
	if *logPath != "" {
		f, err := openLog(*logPath)
		if err != nil {
			return err
		}
		defer f.Close()
		log = f
	}
	srv.Log = log
	hs := &http.Server{Addr: *listen, Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		hs.Shutdown(sctx)
	}()
	fmt.Fprintf(os.Stderr, "scriptllm: %s, script %s (%d rules)\n", *listen, path, len(sc.Rules))
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
