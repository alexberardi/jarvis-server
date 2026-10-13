package install

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const ours = "net.jarvisautomation.agent.desktop"

func TestMimeDefault(t *testing.T) {
	for name, tc := range map[string]struct{ before, after string }{
		"no file":     {"", "[Default Applications]\nx-scheme-handler/jarvis-agent=" + ours + ";\n"},
		"other entry": {"[Default Applications]\ntext/html=firefox.desktop;\n", "[Default Applications]\ntext/html=firefox.desktop;\nx-scheme-handler/jarvis-agent=" + ours + ";\n"},
		"replaces": {"[Default Applications]\nx-scheme-handler/jarvis-agent=old.desktop;\ntext/html=f.desktop;\n",
			"[Default Applications]\nx-scheme-handler/jarvis-agent=" + ours + ";\ntext/html=f.desktop;\n"},
		"section later": {"[Added Associations]\na/b=c.desktop;\n\n[Default Applications]\nd/e=f.desktop;\n\n[Removed Associations]\n",
			"[Added Associations]\na/b=c.desktop;\n\n[Default Applications]\nd/e=f.desktop;\n\nx-scheme-handler/jarvis-agent=" + ours + ";\n[Removed Associations]\n"},
		"no section": {"[Added Associations]\na/b=c.desktop;\n",
			"[Added Associations]\na/b=c.desktop;\n\n[Default Applications]\nx-scheme-handler/jarvis-agent=" + ours + ";\n"},
	} {
		p := filepath.Join(t.TempDir(), "mimeapps.list")
		if tc.before != "" {
			_ = os.WriteFile(p, []byte(tc.before), 0o644)
		}
		for range 2 { // idempotent
			if err := setMimeDefault(p, mimeURLHandler, ours); err != nil {
				t.Fatal(name, err)
			}
		}
		if b, _ := os.ReadFile(p); string(b) != tc.after {
			t.Errorf("%s:\n%s\nwant\n%s", name, b, tc.after)
		}
		if err := unsetMimeDefault(p, mimeURLHandler, ours); err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(p)
		if tc.before == "" {
			if !os.IsNotExist(err) {
				t.Errorf("%s: left %q", name, b)
			}
			continue
		}
		if strings.Contains(string(b), "jarvis-agent") {
			t.Errorf("%s: unset left %q", name, b)
		}
	}
}

func TestMimeUnsetKeepsSomeoneElsesHandler(t *testing.T) {
	p := filepath.Join(t.TempDir(), "mimeapps.list")
	_ = os.WriteFile(p, []byte("[Default Applications]\nx-scheme-handler/jarvis-agent=other.desktop;\n"), 0o644)
	if err := unsetMimeDefault(p, mimeURLHandler, ours); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "other.desktop") {
		t.Fatal("removed another app's handler")
	}
}

// The generated entries pass desktop-file-validate, where it is installed (CI's ubuntu job
// installs desktop-file-utils), with an awkward path too.
func TestDesktopEntriesValidate(t *testing.T) {
	tool, err := exec.LookPath("desktop-file-validate")
	if err != nil {
		t.Skip("desktop-file-validate not installed")
	}
	dir := t.TempDir()
	for name, data := range map[string]string{
		"plain/" + ours:     MenuEntry("/home/u/.local/lib/jarvis-agent/jarvis-agent"),
		"awkward/" + ours:   MenuEntry(`/home/my user/100% "x"/jarvis-agent`),
		"autostart/" + ours: AutostartEntry("/home/my user/.local/lib/jarvis-agent/jarvis-agent"),
	} {
		p := filepath.Join(dir, name)
		if err := writeFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(tool, p).CombinedOutput(); err != nil || len(strings.TrimSpace(string(out))) > 0 {
			t.Errorf("%s: %v\n%s\n%s", name, err, out, data)
		}
	}
}
