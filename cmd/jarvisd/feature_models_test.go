package main

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/modules/ocr"
	"github.com/alexberardi/jarvis-server/internal/modules/recipes"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// featureKeyRE pulls the keys out of the SPA's feature table (web/admin/.../features.ts), where
// every entry reads `key: 'some.setting.model',`. Either quote style matches, so reformatting the
// file cannot fail this test for a reason that has nothing to do with drift.
var featureKeyRE = regexp.MustCompile(`(?m)^\s*key:\s*['"]([^'"]+)['"],`)

// TestFeatureModelsMatchTheSPA keeps the Feature models card and the server's slot settings from
// drifting apart: a row whose key is not a registered slot setting silently does nothing, and a
// slot setting with no row is a choice nobody can reach. docs/llm/SPEC-feature-models.md.
func TestFeatureModelsMatchTheSPA(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "admin", "src", "components", "models", "features.ts"))
	if err != nil {
		t.Fatal(err)
	}
	var inUI []string
	for _, m := range featureKeyRE.FindAllStringSubmatch(string(raw), -1) {
		inUI = append(inUI, m[1])
	}
	if len(inUI) == 0 {
		t.Fatal("no feature keys found in features.ts: has the table's shape changed?")
	}

	// Every definition the wired modules declare, so a key cannot be registered in the wrong one.
	defs := map[string]settings.Definition{}
	for _, d := range slices.Concat(cc.Definitions(), ocr.Definitions, recipes.Definitions) {
		defs[d.Key] = d
	}

	slotChoices := make([]any, len(llm.SlotChoices))
	for i, c := range llm.SlotChoices {
		slotChoices[i] = c
	}
	var registered []string
	for key, d := range defs {
		if slices.Equal(d.Options, slotChoices) {
			registered = append(registered, key)
		}
	}

	for _, key := range inUI {
		d, ok := defs[key]
		if !ok {
			t.Errorf("features.ts offers %q, which no module registers", key)
			continue
		}
		if !slices.Equal(d.Options, slotChoices) {
			t.Errorf("features.ts offers %q, which is not a slot setting (options %v)", key, d.Options)
		}
		if d.Default != llm.SlotAuto {
			t.Errorf("%q defaults to %v, not auto: an upgrade would change which model it uses", key, d.Default)
		}
	}
	for _, key := range registered {
		if !slices.Contains(inUI, key) {
			t.Errorf("%q is a slot setting with no row in features.ts, so nobody can choose it", key)
		}
	}
}
