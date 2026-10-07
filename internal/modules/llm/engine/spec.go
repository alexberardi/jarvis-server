package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Placement-and-load identity of an engine instance (LD1): two labels whose configs produce
// the same key share one supervised process. The port and API key are per instance, not part
// of the key.
type instanceKey struct {
	Kind        Kind    `json:"kind"`
	Binary      string  `json:"binary"`
	Flavour     Flavour `json:"flavour"`
	Model       string  `json:"model"`
	MMProj      string  `json:"mmproj,omitempty"`
	Context     int     `json:"ctx,omitempty"`
	Parallel    int     `json:"np,omitempty"`
	GPULayers   int     `json:"ngl"`
	Devices     string  `json:"devices,omitempty"`
	SplitMode   string  `json:"sm,omitempty"`
	TensorSplit string  `json:"ts,omitempty"`
	KVCacheType string  `json:"kv,omitempty"`
	FlashAttn   string  `json:"fa,omitempty"`
	Embedding   bool    `json:"embedding,omitempty"`
	// ChatTemplate is the pinned template's file, named by its digest, so another template is
	// another instance (ID12).
	ChatTemplate string `json:"tpl,omitempty"`
	ExtraArgs    string `json:"extra,omitempty"`
}

func (k instanceKey) hash() string {
	b, _ := json.Marshal(k)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func keyFor(c LabelConfig, f Flavour, binary string) instanceKey {
	k := instanceKey{
		Kind: c.Kind, Binary: binary, Flavour: f, Model: c.ModelPath, MMProj: c.MMProjPath,
		GPULayers: c.GPULayers, Devices: normList(c.GPUDevices), ExtraArgs: strings.TrimSpace(c.ExtraArgs),
		FlashAttn: c.FlashAttn,
	}
	if c.Kind == KindLlama {
		k.Context, k.Parallel = c.Context, c.Parallel
		k.SplitMode, k.TensorSplit = c.SplitMode, normList(c.TensorSplit)
		k.KVCacheType, k.Embedding = c.KVCacheType, c.Embedding
	}
	if f == FlavourCPU {
		k.GPULayers, k.Devices, k.SplitMode, k.TensorSplit = 0, "", "", ""
	}
	return k
}

func normList(s string) string {
	var parts []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, ",")
}

// args builds the engine command line for an instance key (01 §11 "Local slot spec").
func (k instanceKey) args(port int, alias string) ([]string, error) {
	extra, err := SplitArgs(k.ExtraArgs)
	if err != nil {
		return nil, fmt.Errorf("extra_args: %w", err)
	}
	a := []string{"-m", k.Model, "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	switch k.Kind {
	case KindWhisper:
		if k.GPULayers == 0 || k.Flavour == FlavourCPU {
			a = append(a, "--no-gpu")
		}
		if k.FlashAttn == "off" {
			// -nfa; whisper-server enables flash attention by default.
			a = append(a, "-nfa")
		}
	default:
		a = append(a, "--alias", alias, "--no-webui")
		if k.Context > 0 {
			a = append(a, "-c", strconv.Itoa(k.Context))
		}
		if k.Parallel > 0 {
			a = append(a, "-np", strconv.Itoa(k.Parallel))
		}
		a = append(a, "-ngl", strconv.Itoa(k.GPULayers))
		if k.GPULayers == 0 && k.Flavour != FlavourCPU {
			// A GPU build told to stay on the CPU: don't touch the devices at all.
			a = append(a, "-dev", "none")
		}
		if k.Embedding {
			// MiniLM takes at most 512 tokens; a whole input must fit one ubatch.
			a = append(a, "--embedding", "--pooling", "mean", "-b", "2048", "-ub", "2048")
		} else {
			a = append(a, "--jinja")
			if k.ChatTemplate != "" {
				// Before the extra args, so an operator's own --chat-template-file wins.
				a = append(a, "--chat-template-file", k.ChatTemplate)
			}
		}
		if k.MMProj != "" {
			a = append(a, "--mmproj", k.MMProj)
		}
		if k.KVCacheType != "" && k.KVCacheType != "f16" {
			a = append(a, "-ctk", k.KVCacheType, "-ctv", k.KVCacheType)
		}
		if k.FlashAttn != "" && k.FlashAttn != "auto" {
			a = append(a, "-fa", k.FlashAttn)
		}
		if k.SplitMode != "" {
			a = append(a, "-sm", k.SplitMode)
		}
		if k.TensorSplit != "" {
			a = append(a, "-ts", k.TensorSplit)
		}
	}
	return append(a, extra...), nil
}

// SplitArgs splits a flag string like a POSIX shell would, honouring single and double
// quotes and backslash escapes outside single quotes. Prod's live engine needs
// `--chat-template-kwargs '{"enable_thinking": false}'`.
func SplitArgs(s string) ([]string, error) {
	var (
		out     []string
		cur     strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '\\':
				escaped = true
			default:
				cur.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case unicode.IsSpace(r):
			if inWord {
				out = append(out, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated %c quote", quote)
	}
	if escaped {
		return nil, fmt.Errorf("trailing backslash")
	}
	if inWord {
		out = append(out, cur.String())
	}
	return out, nil
}
