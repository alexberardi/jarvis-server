package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/llm/engine"
)

// HF is a minimal Hugging Face Hub client: repo file listings and download URLs.
type HF struct {
	Endpoint func() string // default https://huggingface.co (llm.hf_endpoint, HF_ENDPOINT)
	Token    func() string // llm.hf_token, for gated repos
	Client   *http.Client
}

var repoRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*/[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidRepo reports whether s looks like "owner/name".
func ValidRepo(s string) bool { return repoRe.MatchString(s) && !strings.Contains(s, "..") }

// ErrGated is returned when a repo needs a token (or a different one).
var ErrGated = errors.New("repo is gated or private: set llm.hf_token to a token with access")

// ErrRepoNotFound is returned for an unknown repo or revision.
var ErrRepoNotFound = errors.New("repo or revision not found")

func (h *HF) endpoint() string {
	ep := ""
	if h.Endpoint != nil {
		ep = h.Endpoint()
	}
	if ep == "" {
		ep = "https://huggingface.co"
	}
	return strings.TrimRight(ep, "/")
}

// AuthHeader is the header downloads send (nil without a token).
func (h *HF) AuthHeader() http.Header {
	if h.Token == nil {
		return nil
	}
	if t := strings.TrimSpace(h.Token()); t != "" {
		return http.Header{"Authorization": {"Bearer " + t}}
	}
	return nil
}

// RepoFile is one file in a repo.
type RepoFile struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Repo is a repo listing at a pinned revision.
type Repo struct {
	ID       string     `json:"repo"`
	Revision string     `json:"revision"`
	Gated    bool       `json:"gated"`
	Files    []RepoFile `json:"files"`
}

// Repo lists a repo's files at revision ("" = main), resolving the revision to a commit so a
// download can't drift.
func (h *HF) Repo(ctx context.Context, repo, revision string) (Repo, error) {
	if !ValidRepo(repo) {
		return Repo{}, fmt.Errorf("invalid repo %q (want owner/name)", repo)
	}
	u := h.endpoint() + "/api/models/" + repo
	if revision != "" {
		u += "/revision/" + url.PathEscape(revision)
	}
	u += "?blobs=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Repo{}, err
	}
	for k, v := range h.AuthHeader() {
		req.Header[k] = v
	}
	c := h.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return Repo{}, fmt.Errorf("hugging face: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return Repo{}, ErrGated
	case http.StatusNotFound:
		return Repo{}, ErrRepoNotFound
	default:
		return Repo{}, fmt.Errorf("hugging face: %s", resp.Status)
	}
	var body struct {
		SHA      string `json:"sha"`
		Gated    any    `json:"gated"` // false, or "auto"/"manual"
		Siblings []struct {
			Name string `json:"rfilename"`
			Size int64  `json:"size"`
			LFS  *struct {
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"lfs"`
		} `json:"siblings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return Repo{}, fmt.Errorf("hugging face: %w", err)
	}
	r := Repo{ID: repo, Revision: body.SHA}
	if r.Revision == "" {
		r.Revision = revision
	}
	switch g := body.Gated.(type) {
	case bool:
		r.Gated = g
	case string:
		r.Gated = g != "" && g != "false"
	}
	for _, s := range body.Siblings {
		f := RepoFile{Name: s.Name, Size: s.Size}
		if s.LFS != nil {
			f.SHA256 = s.LFS.SHA256
			if f.Size == 0 {
				f.Size = s.LFS.Size
			}
		}
		r.Files = append(r.Files, f)
	}
	return r, nil
}

// FileURL is the download URL of a repo file at a revision.
func (h *HF) FileURL(repo, revision, file string) string {
	parts := strings.Split(file, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	if revision == "" {
		revision = "main"
	}
	return h.endpoint() + "/" + repo + "/resolve/" + url.PathEscape(revision) + "/" + strings.Join(parts, "/")
}

var (
	shardRe = regexp.MustCompile(`^(.*)-(\d{5})-of-(\d{5})\.gguf$`)
	quantRe = regexp.MustCompile(`(?i)(?:^|[-_.])((?:UD-)?(?:IQ\d_[A-Z0-9]+(?:_[A-Z]+)?|Q\d_K(?:_[A-Z]+)?|Q\d_\d|Q\d_K|TQ\d_\d|MXFP4(?:_MOE)?|BF16|F16|F32))(?:[-_.]|$)`)
)

// Quant extracts the quantisation from a file name ("Q4_K_M", "UD-Q4_K_XL", "BF16", …).
func Quant(name string) string {
	m := quantRe.FindStringSubmatch(path.Base(name))
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// Choice is an installable unit in a repo: a single model file, or the shards of a split
// GGUF (load the first; llama-server finds the rest beside it).
type Choice struct {
	File   string     `json:"file"` // what to pass as "file" to install: the first shard
	Kind   string     `json:"kind"` // guessed: llm | mmproj | embedding | stt
	Quant  string     `json:"quant,omitempty"`
	Size   int64      `json:"size"` // all shards
	Shards []RepoFile `json:"shards"`
}

// Choices groups a repo's model files (GGUFs and whisper ggml-*.bin), sorted by size.
func Choices(r Repo) []Choice {
	shards := map[string][]RepoFile{}
	var out []Choice
	embedRepo := regexp.MustCompile(`(?i)embed|minilm|bge-|e5-|gte-|nomic`).MatchString(r.ID)
	for _, f := range r.Files {
		base := path.Base(f.Name)
		switch {
		case shardRe.MatchString(f.Name):
			m := shardRe.FindStringSubmatch(f.Name)
			shards[m[1]] = append(shards[m[1]], f)
		case strings.HasSuffix(strings.ToLower(base), ".gguf"):
			kind := engine.ModelLLM
			if strings.Contains(strings.ToLower(base), "mmproj") {
				kind = engine.ModelMMProj
			} else if embedRepo {
				kind = engine.ModelEmbedding
			}
			out = append(out, Choice{File: f.Name, Kind: kind, Quant: Quant(base), Size: f.Size, Shards: []RepoFile{f}})
		case strings.HasPrefix(base, "ggml-") && strings.HasSuffix(base, ".bin"):
			out = append(out, Choice{File: f.Name, Kind: engine.ModelSTT, Quant: Quant(strings.TrimSuffix(base, ".bin")), Size: f.Size, Shards: []RepoFile{f}})
		}
	}
	for _, fs := range shards {
		slices.SortFunc(fs, func(a, b RepoFile) int { return strings.Compare(a.Name, b.Name) })
		m := shardRe.FindStringSubmatch(fs[0].Name)
		var size int64
		for _, f := range fs {
			size += f.Size
		}
		c := Choice{File: fs[0].Name, Kind: engine.ModelLLM, Quant: Quant(path.Base(m[1]) + ".gguf"), Size: size, Shards: fs}
		if embedRepo {
			c.Kind = engine.ModelEmbedding
		}
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Choice) int {
		if a.Size != b.Size {
			if a.Size < b.Size {
				return -1
			}
			return 1
		}
		return strings.Compare(a.File, b.File)
	})
	return out
}

// shardSet returns every file an install of file needs: all shards of a split GGUF, or the
// file alone.
func shardSet(r Repo, file string) ([]RepoFile, error) {
	for _, c := range Choices(r) {
		if c.File == file {
			return c.Shards, nil
		}
		for _, s := range c.Shards {
			if s.Name == file {
				return c.Shards, nil
			}
		}
	}
	for _, f := range r.Files {
		if f.Name == file {
			return []RepoFile{f}, nil
		}
	}
	return nil, fmt.Errorf("%s has no file %q", r.ID, file)
}
