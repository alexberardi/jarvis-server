package models

import (
	"crypto/sha256"
	"encoding/hex"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Where a Hugging Face file lands: <models>/<repo dir>/<file>.
//
// The upstream Windows builds of llama-server and whisper-server are not long-path aware:
// in I0 llama-server could not open a 314-character model path even with LongPathsEnabled=1
// (docs/install/I0-results.md). Both halves of the path come from Hugging Face and can be
// long, so each part is capped: at most 48 + 1 + 24 + 1 + 88 = 162 characters under the
// models directory, which keeps a model path under 200 characters below
// C:\ProgramData\jarvisd\models\ (29 characters) and well inside MAX_PATH (260).
// Over-long parts keep a readable prefix plus 8 hex characters of a SHA-256 of the
// original, so different repos never share a directory. Short names are unchanged.
const (
	maxRepoDir = 48
	maxSubdir  = 24
	maxBase    = 88
)

// repoDir is the directory a repo's files live in: "owner--repo", shortened when long.
func repoDir(repo string) string {
	return shorten(legacyRepoDir(repo), maxRepoDir, repo)
}

// legacyRepoDir is the layout before the caps; installs made with it keep working.
func legacyRepoDir(repo string) string { return strings.ReplaceAll(repo, "/", "--") }

// splitGGUF matches a split model's shard suffix. llama.cpp finds shards 2..n from the
// first one's name, so the suffix must survive shortening and every shard must keep the
// same stem.
var splitGGUF = regexp.MustCompile(`-\d{5}-of-\d{5}\.gguf$`)

// fileRel is a repo file's path below its repo dir. Subdirectories collapse into one short
// component; an over-long base name keeps its extension (or split-shard suffix) and gets a
// shortened stem that is the same for every shard of a split model.
func fileRel(name string) string {
	dir, base := path.Split(name)
	dir = strings.Trim(dir, "/")
	if len(base) > maxBase {
		suffix := splitGGUF.FindString(base)
		if suffix == "" {
			if ext := path.Ext(base); len(ext) <= 12 {
				suffix = ext
			}
		}
		stem := strings.TrimSuffix(base, suffix)
		base = shorten(stem, maxBase-len(suffix), stem) + suffix
	}
	if dir == "" {
		return base
	}
	return shorten(strings.ReplaceAll(dir, "/", "--"), maxSubdir, dir) + "/" + base
}

// shorten returns s when it fits in max bytes, else a prefix of s plus "-" and 8 hex
// characters of sha256(key).
func shorten(s string, max int, key string) string {
	if len(s) <= max {
		return s
	}
	sum := sha256.Sum256([]byte(key))
	suffix := "-" + hex.EncodeToString(sum[:4])
	prefix := s[:max-len(suffix)]
	for len(prefix) > 0 && prefix[len(prefix)-1] >= 0x80 { // don't cut a UTF-8 sequence
		prefix = prefix[:len(prefix)-1]
	}
	return strings.TrimRight(prefix, "-._ ") + suffix
}

// usesLegacyLayout reports whether a downloaded repo model was placed before the caps.
func (m *Manager) usesLegacyLayout(mod Model) bool {
	if mod.SourceURL != "" || mod.Repo == "" || len(mod.Files) == 0 {
		return false
	}
	return mod.Path == filepath.Join(m.ModelsDir, legacyRepoDir(mod.Repo), filepath.FromSlash(mod.Files[0].Name))
}

// filePath is where one of a downloaded model's files lives.
func (m *Manager) filePath(mod Model, name string) string {
	if mod.SourceURL != "" || m.usesLegacyLayout(mod) {
		return filepath.Join(m.modelRoot(mod), filepath.FromSlash(name))
	}
	return filepath.Join(m.modelRoot(mod), filepath.FromSlash(fileRel(name)))
}
