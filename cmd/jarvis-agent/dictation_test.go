package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/portal"
)

type fakeEntry struct{ writes, removes int }

func (f *fakeEntry) Write() error  { f.writes++; return nil }
func (f *fakeEntry) Remove() error { f.removes++; return nil }

type fakePortal struct {
	st       portal.State
	allowErr error
	allowed  int
	revoked  int
}

func (f *fakePortal) Probe(context.Context) portal.State { return f.st }
func (f *fakePortal) Allow(context.Context) error        { f.allowed++; return f.allowErr }
func (f *fakePortal) Revoke() error                      { f.revoked++; return nil }

func TestDictationCommand(t *testing.T) {
	ctx := context.Background()
	var out bytes.Buffer
	f := &fakePortal{st: portal.State{Portal: true}}
	e := &fakeEntry{}
	if err := dictationRun(ctx, []string{"status"}, f, &out, "linux", e); err != nil || !strings.Contains(out.String(), "not allowed yet") {
		t.Fatalf("%v %q", err, out.String())
	}
	out.Reset()
	if err := dictationRun(ctx, []string{"allow"}, f, &out, "linux", e); err != nil || f.allowed != 1 || !strings.Contains(out.String(), "Allowed") {
		t.Fatalf("%v %q", err, out.String())
	}
	f.allowErr = errors.New("cancelled")
	if err := dictationRun(ctx, []string{"allow"}, f, &out, "linux", e); err == nil {
		t.Fatal("a cancel reported success")
	}
	if err := dictationRun(ctx, []string{"revoke"}, f, &out, "linux", e); err != nil || f.revoked != 1 {
		t.Fatal(err)
	}
	if e.writes != 2 || e.removes != 1 {
		t.Fatalf(".desktop writes %d removes %d", e.writes, e.removes)
	}
	out.Reset()
	f.st = portal.State{Why: "this desktop has no RemoteDesktop portal"}
	_ = dictationRun(ctx, []string{"status"}, f, &out, "linux", e)
	if !strings.Contains(out.String(), "unavailable: this desktop has no RemoteDesktop portal") {
		t.Fatal(out.String())
	}
	for _, bad := range [][]string{nil, {"grant"}, {"allow", "x"}} {
		if dictationRun(ctx, bad, f, &out, "linux", e) == nil {
			t.Fatalf("%v accepted", bad)
		}
	}
	if err := dictationRun(ctx, []string{"allow"}, f, &out, "darwin", e); err == nil || f.allowed != 2 {
		t.Fatalf("darwin: %v (allowed %d)", err, f.allowed)
	}
}
