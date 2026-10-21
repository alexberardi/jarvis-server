package firstrun

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The setup page and the extension's popup carry direction B's tokens (docs/decisions/
// 2026-10-20-web-ui-direction.md) as plain CSS variables, copied from web/admin/src/index.css
// (itself docs/design/directions/tokens.py's DIRECTIONS["B"]). This test keeps the three copies
// equal, and keeps colours out of the rest of the agent's CSS, so a page can't drift back to its
// own palette.

var (
	cssVar   = regexp.MustCompile(`(--[a-z0-9-]+)\s*:\s*([^;]+);`)
	cssDecl  = regexp.MustCompile(`[a-z-]+\s*:\s*([^;{}]+)`)
	hexColor = regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`)
	rgbColor = regexp.MustCompile(`\b(rgba?|hsla?)\(`)
)

// block returns the text after marker up to the first "}", and the css without it.
func block(t *testing.T, css, marker string) (string, string) {
	t.Helper()
	i := strings.Index(css, marker)
	if i < 0 {
		t.Fatalf("no %q", marker)
	}
	j := strings.Index(css[i:], "}")
	return css[i : i+j], css[:i] + css[i+j:]
}

func vars(s string) map[string]string {
	m := map[string]string{}
	for _, v := range cssVar.FindAllStringSubmatch(s, -1) {
		m[v[1]] = strings.ToLower(strings.TrimSpace(v[2]))
	}
	return m
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTokensMatchWebAdmin(t *testing.T) {
	admin := readFile(t, "../../../web/admin/src/index.css")
	theme, _ := block(t, admin, "@theme static {")
	root, _ := block(t, admin, "\n:root {")
	dark, _ := block(t, admin, "\n.dark {")
	wantLight := vars(theme)
	for k, v := range vars(root) {
		wantLight[k] = v
	}
	wantDark := vars(dark)
	// The old palette's accents: web/admin keeps them for pages not yet moved, B has none.
	for _, m := range []map[string]string{wantLight, wantDark} {
		delete(m, "--color-secondary")
		delete(m, "--color-tertiary")
	}

	app, err := assets.ReadFile("assets/app.css")
	if err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{
		"firstrun/assets/app.css":      string(app),
		"browser/extension/popup.html": readFile(t, "../browser/extension/popup.html"),
	}
	for name, css := range pages {
		light, rest := block(t, css, ":root {")
		darkBlock, rest := block(t, rest, "prefers-color-scheme: dark")
		got, gotDark := vars(light), vars(darkBlock)

		for k, v := range wantLight {
			if strings.HasPrefix(k, "--color-") && got[k] != v {
				t.Errorf("%s: light %s = %q, want %q", name, k, got[k], v)
			}
		}
		for k, v := range wantDark {
			if strings.HasPrefix(k, "--color-") && gotDark[k] != v {
				t.Errorf("%s: dark %s = %q, want %q", name, k, gotDark[k], v)
			}
		}
		// The rest of B's tokens a page copies (type, radius, motion) must be copied exactly.
		for k, v := range got {
			if w, ok := wantLight[k]; ok && w != v {
				t.Errorf("%s: %s = %q, want %q (web/admin)", name, k, v, w)
			}
		}
		for k := range gotDark {
			if !strings.HasPrefix(k, "--color-") {
				t.Errorf("%s: dark mode sets %s; only colours change", name, k)
			}
		}
		for _, d := range cssDecl.FindAllStringSubmatch(rest, -1) {
			if hexColor.MatchString(d[1]) || rgbColor.MatchString(d[1]) {
				t.Errorf("%s: a colour outside the tokens: %q", name, strings.TrimSpace(d[0]))
			}
		}
	}
}
