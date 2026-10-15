package packages

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPipDepSpec(t *testing.T) {
	for in, want := range map[PipDep]string{
		{Name: "httpx"}:                    "httpx",
		{Name: "httpx", Version: "0.27"}:   "httpx==0.27",
		{Name: "httpx", Version: ">=0.27"}: "httpx>=0.27",
		{Name: "a", Version: " 1.0 "}:      "a==1.0",
	} {
		if got := in.Spec(); got != want {
			t.Errorf("%+v = %q, want %q", in, got, want)
		}
	}
	for _, bad := range []string{"name: x\npackages: [{name: 'x; rm -rf /'}]\n", "name: x\npackages: [{name: x, version: '1 --pre'}]\n",
		"name: '../x'\n", "name: x\ncomponents: [{type: command, name: c, path: ../c.py}]\n",
		"name: x\ncomponents: [{type: command, name: c, path: /c.py}]\n", "name: x\ncomponents: [{type: command, name: c, path: c.txt}]\n"} {
		if _, err := ParseManifest([]byte(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := ParseManifest([]byte("name: home-assistant\npackages: [{name: 'httpx[http2]', version: '>=0.27,<1'}]\n")); err != nil {
		t.Fatal(err)
	}
}

// Components a manifest doesn't declare are found as the Pi finds them.
func TestLoadManifestInfersComponents(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"jarvis_command.yaml", "command.py", "commands/b/command.py", "commands/a/command.py",
		"commands/_private/command.py", "agents/reminders/agent.py", "commands/noentry/other.py"} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		body := ""
		if f == "jarvis_command.yaml" {
			body = "name: meds\nversion: '0.2.3'\n"
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []Component{{"command", "meds", "command.py"}, {"command", "a", "commands/a/command.py"},
		{"command", "b", "commands/b/command.py"}, {"agent", "reminders", "agents/reminders/agent.py"}}
	if !reflect.DeepEqual(m.Components, want) {
		t.Fatalf("components = %+v", m.Components)
	}
	if !reflect.DeepEqual(m.Skipped(), []string{"agent:reminders"}) || len(m.Commands()) != 3 {
		t.Fatalf("skipped = %v", m.Skipped())
	}
	if err := m.CheckDesktop("linux"); err != nil {
		t.Fatal(err)
	}
}
