package install

import (
	"os"
	"path/filepath"
	"testing"
)

// #483: after rc18.3 was installed in /Applications, the old rc18 app in ~/Downloads kept
// launching (the login item was registered by the bundle that ran first). The decision: a copy
// outside /Applications while an equal-or-newer copy is installed there hands over to it; the
// installed copy (or a newer one outside) opened by hand takes the login item.
func TestDecideHandover(t *testing.T) {
	const (
		apps = "/Applications/Jarvis Automation Agent.app"
		dl   = "/Users/u/Downloads/jarvis-agent-v0.1.0-rc18-darwin-arm64/Jarvis Automation Agent.app"
		tl   = "/private/var/folders/ab/T/AppTranslocation/0A4C/d/Jarvis Automation Agent.app"
	)
	tests := []struct {
		name               string
		running, installed Copy
		launchd            bool
		want               Handover
	}{
		{"an older Downloads copy hands over", Copy{dl, "v0.1.0-rc18"}, Copy{apps, "v0.1.0-rc18.3"}, false, HandOverToInstalled},
		{"launchd's stale login item hands over", Copy{dl, "v0.1.0-rc18"}, Copy{apps, "v0.1.0-rc18.3"}, true, HandOverToInstalled},
		{"the same version installed hands over", Copy{dl, "v0.1.0-rc18.3"}, Copy{apps, "v0.1.0-rc18.3"}, false, HandOverToInstalled},
		{"a newer copy outside Applications adopts the login item", Copy{dl, "v0.1.0-rc19"}, Copy{apps, "v0.1.0-rc18.3"}, false, AdoptLoginItem},
		{"launchd running the newest copy stays put", Copy{dl, "v0.1.0-rc19"}, Copy{apps, "v0.1.0-rc18.3"}, true, RunHere},
		{"the installed copy opened by hand adopts the login item", Copy{apps, "v0.1.0-rc18.3"}, Copy{apps, "v0.1.0-rc18.3"}, false, AdoptLoginItem},
		{"the installed copy under launchd stays put", Copy{apps, "v0.1.0-rc18.3"}, Copy{apps, "v0.1.0-rc18.3"}, true, RunHere},
		{"a translocated run of the same version hands over", Copy{tl, "v0.1.0-rc18.3"}, Copy{apps, "v0.1.0-rc18.3"}, false, HandOverToInstalled},
		{"a translocated newer copy stays put (it must not own the login item)", Copy{tl, "v0.1.0-rc19"}, Copy{apps, "v0.1.0-rc18.3"}, false, RunHere},
		{"nothing installed", Copy{dl, "v0.1.0-rc18"}, Copy{}, false, RunHere},
		{"a dev build never hands over", Copy{dl, "dev"}, Copy{apps, "v0.1.0-rc18.3"}, false, RunHere},
		{"an unreadable installed version stays put", Copy{dl, "v0.1.0-rc18"}, Copy{apps, ""}, false, RunHere},
		{"not run from a bundle", Copy{"", "v0.1.0-rc18"}, Copy{apps, "v0.1.0-rc18.3"}, false, RunHere},
	}
	for _, tt := range tests {
		if got := DecideHandover(tt.running, tt.installed, tt.launchd); got != tt.want {
			t.Errorf("%s: DecideHandover(%+v, %+v, launchd=%v) = %v, want %v",
				tt.name, tt.running, tt.installed, tt.launchd, got, tt.want)
		}
	}
}

func TestFindInstalledApp(t *testing.T) {
	apps, home := t.TempDir(), t.TempDir()
	mk := func(dir string) string {
		t.Helper()
		bundle := filepath.Join(dir, displayName+".app")
		exe := BundleExe(bundle)
		if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(exe, []byte("#!"), 0o755); err != nil {
			t.Fatal(err)
		}
		return bundle
	}
	version := func(exe string) string { return "v0.1.0-rc18.3" }
	running := Copy{Bundle: "/elsewhere/Jarvis Automation Agent.app", Version: "v0.1.0-rc18"}

	if got := FindInstalledApp([]string{apps, home}, running, version); got != (Copy{}) {
		t.Fatalf("found %+v in empty dirs", got)
	}
	inHome := mk(home)
	if got := FindInstalledApp([]string{apps, home}, running, version); got.Bundle != inHome || got.Version != "v0.1.0-rc18.3" {
		t.Fatalf("got %+v, want the ~/Applications copy at rc18.3", got)
	}
	// /Applications wins over ~/Applications.
	inApps := mk(apps)
	if got := FindInstalledApp([]string{apps, home}, running, version); got.Bundle != inApps {
		t.Fatalf("got %+v, want the /Applications copy", got)
	}
	// The running copy is the installed one: its own version, without asking the binary.
	got := FindInstalledApp([]string{apps}, Copy{Bundle: inApps, Version: "v0.1.0-rc19"}, func(string) string {
		t.Fatal("asked the running copy for its own version")
		return ""
	})
	if got.Bundle != inApps || got.Version != "v0.1.0-rc19" {
		t.Fatalf("got %+v, want the running copy's own version", got)
	}
}
