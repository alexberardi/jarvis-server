package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// hardcodedLabelRE finds a chat request pinned to a slot in source: `Label: llm.LabelLive`,
// `Label: "background"` and so on.
var hardcodedLabelRE = regexp.MustCompile(`Label:\s*(?:llm\.)?(?:LabelLive|LabelBackground|"live"|"background")`)

// allowedHardcodedLabels are the only places a slot may still be named in code, with why. Every
// other chat request belongs to a built-in feature and must read that feature's slot setting.
var allowedHardcodedLabels = map[string]string{
	// Not a chat request: the setup wizard's job descriptor, whose Label is a job's title.
	"internal/modules/admin/setupjobs.go": "setup job descriptor, not a ChatRequest",
	// The public async-job API. A caller must not be able to pick a slot, least of all cloud.
	"internal/modules/llm/validate.go": "public queue API is pinned to background by contract",
	// The same contract, applied when the job runs.
	"internal/modules/llm/jobs.go": "public queue API is pinned to background by contract",
}

// TestNoFeatureHardcodesItsModel is the regression guard behind the Feature models card: a
// built-in feature that goes back to naming a slot in code silently ignores the setting the
// admin page writes, and nothing else would catch it. docs/llm/SPEC-feature-models.md.
func TestNoFeatureHardcodesItsModel(t *testing.T) {
	root := filepath.Join("..", "..", "internal")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hits := hardcodedLabelRE.FindAllString(string(raw), -1)
		if len(hits) == 0 {
			return nil
		}
		rel := filepath.ToSlash(strings.TrimPrefix(filepath.ToSlash(path), "../../"))
		if why, ok := allowedHardcodedLabels[rel]; ok {
			t.Logf("%s: %d allowed (%s)", rel, len(hits), why)
			return nil
		}
		t.Errorf("%s pins a chat request to a slot (%s). A built-in feature must read its slot "+
			"setting (llm.SlotFor) so the Feature models card can move it; if this is not a feature, "+
			"add it to allowedHardcodedLabels with the reason.", rel, strings.Join(hits, ", "))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
