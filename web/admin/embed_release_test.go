//go:build release_ui

package adminui

import (
	"io/fs"
	"strings"
	"testing"
)

// TestEmbeddedUI fails a release build that embeds only the placeholder. Run it after
// `npm run build`: go test ./web/admin -run TestEmbeddedUI -tags release_ui
func TestEmbeddedUI(t *testing.T) {
	ui, ok := UI()
	if !ok {
		t.Fatal("web/admin/dist/ui/index.html is not embedded: build the SPA before jarvisd")
	}
	index, err := fs.ReadFile(ui, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(index), `<script type="module"`) {
		t.Fatalf("index.html has no module script; is it the Vite build?\n%s", index)
	}
	assets, err := fs.ReadDir(ui, "assets")
	if err != nil {
		t.Fatalf("no assets/ in the embedded UI: %v", err)
	}
	var js bool
	for _, e := range assets {
		if strings.HasSuffix(e.Name(), ".js") {
			js = true
		}
	}
	if !js {
		t.Fatal("no JavaScript bundle under assets/")
	}
}
