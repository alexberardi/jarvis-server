package main

import (
	"os/exec"
	"testing"
)

func TestShouldRegroup(t *testing.T) {
	for _, c := range []struct {
		name     string
		userGids []string
		proc     []int
		guard    string
		want     bool
	}{
		{"member, process without it", []string{"1000", "992"}, []int{1000, 65534}, "", true},
		{"member, process has it", []string{"1000", "992"}, []int{1000, 992}, "", false},
		{"not a member", []string{"1000"}, []int{1000}, "", false},
		{"newgrp already tried", []string{"1000", "992"}, []int{1000}, "1", false},
	} {
		if got := shouldRegroup("992", c.userGids, c.proc, c.guard); got != c.want {
			t.Errorf("%s: %v", c.name, got)
		}
	}
	if shouldRegroup("", []string{""}, nil, "") {
		t.Error("no gid")
	}
}

func TestRegroupScript(t *testing.T) {
	got := regroupScript("/opt/it's/jarvis-agent", []string{"run", "--profile", "a b"}, "/usr/bin/zsh", true)
	want := `SHELL='/usr/bin/zsh'; export SHELL; exec '/opt/it'\''s/jarvis-agent' 'run' '--profile' 'a b'` + "\n"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	if got := regroupScript("/a", nil, "", false); got != "unset SHELL; exec '/a'\n" {
		t.Fatalf("no SHELL: %q", got)
	}
	// The quoting survives a real sh: echo stands in for the agent.
	out, err := exec.Command("sh", "-c", regroupScript("echo", []string{"it's", "$HOME", "a b"}, "x", true)).Output()
	if err != nil || string(out) != "it's $HOME a b\n" {
		t.Fatalf("%q %v", out, err)
	}
}
