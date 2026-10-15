package buildinfo

import (
	"runtime/debug"
	"testing"
	"time"
)

const rev = "0123456789abcdef0123456789abcdef01234567"

func TestFromSettings(t *testing.T) {
	v, ok := FromSettings([]debug.BuildSetting{
		{Key: "-trimpath", Value: "true"},
		{Key: "vcs", Value: "git"},
		{Key: "vcs.revision", Value: rev},
		{Key: "vcs.time", Value: "2026-10-13T21:04:05Z"},
		{Key: "vcs.modified", Value: "true"},
	})
	want := VCS{Revision: rev, Time: time.Date(2026, 10, 13, 21, 4, 5, 0, time.UTC), Modified: true}
	if !ok || v != want {
		t.Fatalf("got %+v %v, want %+v", v, ok, want)
	}
	if _, ok := FromSettings([]debug.BuildSetting{{Key: "vcs.modified", Value: "false"}}); ok {
		t.Fatal("no revision: ok")
	}
}

func TestDescribe(t *testing.T) {
	clean := VCS{Revision: rev}
	dirty := VCS{Revision: rev, Modified: true}
	for _, c := range []struct {
		version string
		v       VCS
		ok      bool
		want    string
	}{
		{"v0.4.1", dirty, true, "v0.4.1"}, // a release keeps its tag
		{"dev", clean, true, "dev (0123456)"},
		{"dev", dirty, true, "dev (0123456+dirty)"},
		{"dev-0123456", clean, true, "dev-0123456 (0123456)"},
		{"dev", VCS{}, false, "dev"}, // go run: nothing recorded
	} {
		if got := Describe(c.version, c.v, c.ok); got != c.want {
			t.Errorf("Describe(%q, %+v) = %q, want %q", c.version, c.v, got, c.want)
		}
	}
	if !IsDev("dev") || !IsDev("dev-sandbox") || IsDev("v1.0.0") || IsDev("desktop-shot") {
		t.Fatal("IsDev")
	}
}
