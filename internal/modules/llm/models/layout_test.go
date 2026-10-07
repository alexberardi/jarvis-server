package models

import (
	"path/filepath"
	"strings"
	"testing"
)

// windowsModelPath is the path an engine gets on a Windows service install.
func windowsModelPath(repo, name string) string {
	return `C:\ProgramData\jarvisd\models\` + strings.ReplaceAll(repoDir(repo)+"/"+fileRel(name), "/", `\`)
}

// The I0 case: a 314-character path that llama-server.exe could not open. Every model path
// must stay under 200 characters below C:\ProgramData\jarvisd.
func TestModelPathStaysShortOnWindows(t *testing.T) {
	long := func(prefix string, n int) string { return prefix + strings.Repeat("x", n-len(prefix)) }
	for _, tc := range []struct{ repo, name string }{
		{long("owner-", 39) + "/" + long("Repo-", 96), long("sub-", 60) + "/" + long("Model-", 76) + ".gguf"},
		{"bartowski/mistralai_Mistral-Small-3.2-24B-Instruct-2506-GGUF", "mistralai_Mistral-Small-3.2-24B-Instruct-2506-Q4_K_M.gguf"},
		{long("o", 96) + "/" + long("r", 96), long("a", 40) + "/" + long("b", 40) + "/" + long("c", 40) + "/" + long("Split", 150) + "-00001-of-00003.gguf"},
		{"x/y", long("Weird.Name.With.Dots", 200)},
		{"x/" + strings.Repeat("é", 60), strings.Repeat("ü", 70) + ".gguf"},
	} {
		p := windowsModelPath(tc.repo, tc.name)
		if len(p) >= 200 {
			t.Errorf("%d chars: %s", len(p), p)
		}
	}
}

func TestLayoutKeepsShortNamesAndShards(t *testing.T) {
	if got := repoDir("Qwen/Qwen2.5-0.5B-Instruct-GGUF"); got != "Qwen--Qwen2.5-0.5B-Instruct-GGUF" {
		t.Errorf("short repo changed: %s", got)
	}
	if got := fileRel("Q8/Split-Q8_0-00001-of-00002.gguf"); got != "Q8/Split-Q8_0-00001-of-00002.gguf" {
		t.Errorf("short file changed: %s", got)
	}
	stem := "Some-Extremely-Long-Model-Name-" + strings.Repeat("Variant-", 12) + "Q4_K_M"
	a := fileRel("dir/" + stem + "-00001-of-00003.gguf")
	b := fileRel("dir/" + stem + "-00002-of-00003.gguf")
	if !strings.HasSuffix(a, "-00001-of-00003.gguf") || strings.TrimSuffix(a, "-00001-of-00003.gguf") != strings.TrimSuffix(b, "-00002-of-00003.gguf") {
		t.Errorf("shards lost their common stem: %s %s", a, b)
	}
	// Two long repos with the same prefix get different directories.
	base := "someone/" + strings.Repeat("Long-Repo-Name-", 5)
	if repoDir(base+"A") == repoDir(base+"B") {
		t.Error("collision")
	}
	if !strings.HasSuffix(fileRel(strings.Repeat("n", 120)+".gguf"), ".gguf") {
		t.Error("extension lost")
	}
}

// Models downloaded before the caps keep resolving to where their files are.
func TestLegacyLayoutStillResolves(t *testing.T) {
	m := &Manager{ModelsDir: t.TempDir()}
	repo := "bartowski/mistralai_Mistral-Small-3.2-24B-Instruct-2506-GGUF"
	name := "mistralai_Mistral-Small-3.2-24B-Instruct-2506-Q4_K_M.gguf"
	files := []File{{Name: name}}

	fresh := m.modelRow("id", "llm", "", "", repo, "main", files)
	if m.usesLegacyLayout(fresh) || m.filePath(fresh, name) != fresh.Path {
		t.Fatalf("new row: %s vs %s", m.filePath(fresh, name), fresh.Path)
	}
	if filepath.Base(m.modelRoot(fresh)) != repoDir(repo) || repoDir(repo) == legacyRepoDir(repo) {
		t.Fatal("new layout not shortened")
	}

	old := fresh
	old.Path = filepath.Join(m.ModelsDir, legacyRepoDir(repo), name)
	if !m.usesLegacyLayout(old) || m.filePath(old, name) != old.Path || filepath.Base(m.modelRoot(old)) != legacyRepoDir(repo) {
		t.Fatalf("legacy row resolves to %s", m.filePath(old, name))
	}
}
