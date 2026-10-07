package main

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/doctor"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
)

// fakeManager is an installed service whose uninstall and purge are recorded.
type fakeManager struct {
	service.Manager
	home        string
	uninstalled bool
	purged      *service.PurgePlan
}

func (f *fakeManager) InstalledHome() string { return f.home }
func (f *fakeManager) Uninstall(context.Context) error {
	if f.uninstalled {
		return service.ErrNotInstalled
	}
	f.uninstalled = true
	return nil
}
func (f *fakeManager) PurgePlan(home string) service.PurgePlan {
	return service.PurgePlan{Home: home, Account: "jarvisd"}
}
func (f *fakeManager) Purge(_ context.Context, p service.PurgePlan) error {
	f.purged = &p
	return os.RemoveAll(p.Home)
}

// fakeFirewall has one tagged rule to remove.
type fakeFirewall struct{ doctor.Firewall }

func (fakeFirewall) RemoveCmds(context.Context) [][]string {
	return [][]string{{"ufw", "delete", "allow", "from", "10.0.0.0/24", "to", "any", "port", "7700", "proto", "tcp"}}
}
func (fakeFirewall) Allows(context.Context, doctor.Port, *net.IPNet) (bool, bool) { return true, true }

func TestUninstallRemovesFirewallRules(t *testing.T) {
	var ran []string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil, nil
	}
	m := &fakeManager{home: filepath.Join(t.TempDir(), "jarvisd")}
	var out bytes.Buffer
	o := uninstallOptions{firewall: fakeFirewall{}, run: run, elevated: func() bool { return true }}
	if err := uninstall(context.Background(), m, o, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if !m.uninstalled || m.purged != nil || len(ran) != 1 || !strings.HasPrefix(ran[0], "ufw delete allow from 10.0.0.0/24") {
		t.Errorf("uninstalled=%v purged=%v ran=%q", m.uninstalled, m.purged, ran)
	}
	// Without root the rules stay and the commands are printed.
	m.uninstalled, ran = false, nil
	out.Reset()
	o.elevated = func() bool { return false }
	if err := uninstall(context.Background(), m, o, nil, false, &out); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 || !strings.Contains(out.String(), "ufw delete allow") {
		t.Errorf("ran=%q out=%s", ran, out.String())
	}
	// --keep-firewall.
	m.uninstalled = false
	o.elevated, o.KeepFirewall = func() bool { return true }, true
	if err := uninstall(context.Background(), m, o, nil, false, &out); err != nil || len(ran) != 0 {
		t.Errorf("keep: err=%v ran=%q", err, ran)
	}
}

func TestUninstallPurgeConfirmation(t *testing.T) {
	home := filepath.Join(t.TempDir(), "jarvisd")
	os.MkdirAll(home, 0o700)
	os.WriteFile(filepath.Join(home, "jarvis.db"), make([]byte, 2048), 0o600)
	o := uninstallOptions{Purge: true, firewall: fakeFirewall{}, run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil },
		elevated: func() bool { return true }}
	ctx := context.Background()

	// No terminal, no --yes: refused before anything changes.
	m := &fakeManager{home: home}
	var out bytes.Buffer
	if err := uninstall(ctx, m, o, nil, false, &out); err == nil || m.uninstalled {
		t.Fatalf("err=%v uninstalled=%v", err, m.uninstalled)
	}
	if !strings.Contains(out.String(), home+" (2.0 KB") || !strings.Contains(out.String(), "the jarvisd account") {
		t.Errorf("prompt %s", out.String())
	}
	// A wrong answer changes nothing.
	if err := uninstall(ctx, m, o, strings.NewReader("/var/lib/other\n"), true, &out); err == nil || m.uninstalled {
		t.Fatalf("wrong answer: err=%v uninstalled=%v", err, m.uninstalled)
	}
	// The path typed back: uninstalled and purged.
	if err := uninstall(ctx, m, o, strings.NewReader(home+"\n"), true, &out); err != nil {
		t.Fatal(err)
	}
	if !m.uninstalled || m.purged == nil || m.purged.Home != home {
		t.Fatalf("uninstalled=%v purged=%+v", m.uninstalled, m.purged)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Error("home not deleted")
	}
	// --yes skips the prompt; the legacy directory is never purged.
	legacy := filepath.Join(t.TempDir(), ".jarvis")
	os.MkdirAll(legacy, 0o700)
	o.Yes = true
	if err := uninstall(ctx, &fakeManager{home: legacy}, o, nil, false, &out); err == nil {
		t.Error("purging ~/.jarvis must be refused")
	}
	if _, err := os.Stat(legacy); err != nil {
		t.Error("legacy dir touched")
	}
}
