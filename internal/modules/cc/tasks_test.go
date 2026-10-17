package cc

import (
	"context"
	"testing"
	"time"

	"github.com/alexberardi/jarvis-server/internal/platform/module"
)

var _ module.Waiter = (*Module)(nil)

// The runner closes the DB once Wait returns: a fire-and-forget trace write must have landed.
func TestModuleWaitDrainsTraceWrites(t *testing.T) {
	e := newEnv(t)
	for range 20 {
		e.m.recordTraceAsync(Trace{ConversationID: "c", RequestType: "stt", Source: "node", TotalDurationMS: 1})
	}
	e.m.Wait()
	if _, total, err := e.m.ListTraces(context.Background(), TraceFilter{}); err != nil || total != 20 {
		t.Fatalf("after Wait: %d traces (%v); want all 20 written", total, err)
	}
	e.m.recordTraceAsync(Trace{ConversationID: "late", RequestType: "stt", Source: "node", TotalDurationMS: 1})
	if _, total, _ := e.m.ListTraces(context.Background(), TraceFilter{}); total != 20 {
		t.Fatalf("a trace queued after Wait was written (%d traces)", total)
	}
}

func TestTaskGroupWaitBlocksUntilTasksFinish(t *testing.T) {
	var g taskGroup
	release, finished := make(chan struct{}), make(chan struct{})
	if !g.Go(func() { <-release; close(finished) }) {
		t.Fatal("Go refused a task before Wait")
	}
	waited := make(chan struct{})
	go func() { g.Wait(); close(waited) }()
	select {
	case <-waited:
		t.Fatal("Wait returned while a task was still running")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	select {
	case <-waited:
	case <-time.After(10 * time.Second): // hang guard
		t.Fatal("Wait never returned")
	}
	select {
	case <-finished:
	default:
		t.Fatal("Wait returned before the task finished")
	}
}

func TestTaskGroupTracksTasksStartedWhileDraining(t *testing.T) {
	var g taskGroup
	release := make(chan struct{})
	nested := make(chan bool, 1)
	nestedDone := make(chan struct{})
	g.Go(func() {
		<-release
		nested <- g.Go(func() { time.Sleep(50 * time.Millisecond); close(nestedDone) })
	})
	waited := make(chan struct{})
	go func() { g.Wait(); close(waited) }()
	time.Sleep(20 * time.Millisecond) // let Wait start draining
	close(release)
	<-waited
	if !<-nested {
		t.Fatal("a task spawned by a running task during the drain was refused")
	}
	select {
	case <-nestedDone:
	default:
		t.Fatal("Wait returned before the nested task finished")
	}
}

func TestTaskGroupRefusesAfterDrain(t *testing.T) {
	var g taskGroup
	g.Wait()
	ran := make(chan struct{}, 1)
	if g.Go(func() { ran <- struct{}{} }) {
		t.Fatal("Go accepted a task after the group drained")
	}
	g.Wait() // idempotent
	select {
	case <-ran:
		t.Fatal("a refused task ran")
	case <-time.After(50 * time.Millisecond):
	}
}
