package logging

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type memSink struct {
	mu      sync.Mutex
	records []Record
	block   chan struct{}
}

func (m *memSink) Write(_ context.Context, b []Record) error {
	if m.block != nil {
		<-m.block
	}
	m.mu.Lock()
	m.records = append(m.records, b...)
	m.mu.Unlock()
	return nil
}

func TestShipsToSinkAndStderr(t *testing.T) {
	sink := &memSink{}
	sh := NewShipper(sink, 100, 10, time.Hour)
	var out bytes.Buffer
	log := New(&out, slog.LevelInfo, sh).With("module", "cc")
	log.Debug("hidden")
	log.Info("node registered", "node_id", "n1")
	log.WithGroup("req").Warn("slow", "ms", 900)
	sh.Close() // flushes

	if !strings.Contains(out.String(), "node registered") || strings.Contains(out.String(), "hidden") {
		t.Fatalf("stderr: %q", out.String())
	}
	if len(sink.records) != 2 {
		t.Fatalf("shipped %d records", len(sink.records))
	}
	r := sink.records[0]
	if r.Source != "cc" || r.Message != "node registered" || r.Attrs["node_id"] != "n1" || r.Level != slog.LevelInfo {
		t.Fatalf("record %+v", r)
	}
	if sink.records[1].Attrs["req.ms"] != int64(900) {
		t.Fatalf("grouped attrs %+v", sink.records[1].Attrs)
	}
}

func TestFullBufferDropsInsteadOfBlocking(t *testing.T) {
	sink := &memSink{block: make(chan struct{})}
	sh := NewShipper(sink, 2, 1, time.Hour)
	log := New(&bytes.Buffer{}, slog.LevelInfo, sh)
	done := make(chan struct{})
	go func() {
		for range 50 {
			log.Info("x")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("logging blocked on a stuck sink")
	}
	if sh.Dropped() == 0 {
		t.Fatal("expected drops")
	}
	close(sink.block)
	sh.Close()
}

func TestParseLevel(t *testing.T) {
	for in, want := range map[string]slog.Level{"debug": slog.LevelDebug, "WARN": slog.LevelWarn, "error": slog.LevelError, "": slog.LevelInfo} {
		if ParseLevel(in) != want {
			t.Errorf("%q", in)
		}
	}
}

// jarvisd panicked at shutdown when mDNS logged after main had closed the shipper.
func TestLoggingAfterCloseIsSafe(t *testing.T) {
	sh := NewShipper(&memSink{}, 16, 4, time.Millisecond)
	log := New(io.Discard, slog.LevelInfo, sh)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 200 {
				log.Info("still running")
			}
		}()
	}
	sh.Close()
	wg.Wait()
	log.Info("after close")
	sh.Close() // idempotent
}

type lateSink struct {
	memSink
	ready atomic.Bool
}

func (l *lateSink) Write(ctx context.Context, b []Record) error {
	if !l.ready.Load() {
		return ErrNotReady
	}
	return l.memSink.Write(ctx, b)
}

// jarvisd's shipper starts before the logs module is wired; a slow startup (migrations) let
// the first flush hit an unwired module and crash. Records wait until the sink is ready.
func TestShipperHoldsRecordsUntilSinkReady(t *testing.T) {
	sink := &lateSink{}
	sh := NewShipper(sink, 100, 10, time.Millisecond)
	log := New(io.Discard, slog.LevelInfo, sh)
	log.Info("starting jarvisd")
	time.Sleep(20 * time.Millisecond) // several flushes while not ready
	sink.ready.Store(true)
	log.Info("listening")
	sh.Close()
	if len(sink.records) != 2 || sink.records[0].Message != "starting jarvisd" {
		t.Fatalf("records %+v", sink.records)
	}
}
