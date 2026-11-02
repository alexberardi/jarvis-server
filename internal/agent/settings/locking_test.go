package settings

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

func TestUpdateAcrossProcesses(t *testing.T) {
	if p := os.Getenv("JARVIS_SETTINGS_TEST_FILE"); p != "" {
		k, _ := Lookup(os.Getenv("JARVIS_SETTINGS_TEST_KEY"))
		if err := Update(p, func(s *Settings) error { return s.Set(k, "false") }); err != nil {
			t.Fatal(err)
		}
		return
	}
	p := filepath.Join(t.TempDir(), "agent.json")
	if err := os.WriteFile(p, []byte(`{"future_setting":{"opaque":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, key := range []string{"voice", "island", "tray", "chimes", "browser", "auto_update"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := exec.Command(os.Args[0], "-test.run=^TestUpdateAcrossProcesses$")
			c.Env = append(os.Environ(), "JARVIS_SETTINGS_TEST_FILE="+p, "JARVIS_SETTINGS_TEST_KEY="+key)
			if b, err := c.CombinedOutput(); err != nil {
				t.Errorf("%s: %v %s", key, err, b)
			}
		}()
	}
	wg.Wait()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if len(out) != 7 {
		t.Fatalf("lost updates: %s", b)
	}
}
