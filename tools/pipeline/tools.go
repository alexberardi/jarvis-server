package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// goldenInputs holds the named tool sets the G1 prompt fixtures were exported with
// (fixtures/golden/README.md): node_dev is a real Pi node's report, server tools included.
const goldenInputs = "fixtures/golden/prompts/_inputs.json"

// LoadToolSet returns the client tools a node sends at /conversation/start:
//
//	"node" or ""  a real Pi node's commands (golden node_dev without the server tools,
//	              which command-center adds itself)
//	"client"      the five-tool client set of the G1 fixtures
//	"none"        no client tools
//	otherwise     a path to a JSON array of OpenAI tool definitions
func LoadToolSet(root, spec string) ([]json.RawMessage, error) {
	switch spec {
	case "none":
		return []json.RawMessage{}, nil
	case "", "node", "client":
		b, err := os.ReadFile(filepath.Join(root, goldenInputs))
		if err != nil {
			return nil, err
		}
		var in struct {
			ToolSets map[string][]json.RawMessage `json:"tool_sets"`
		}
		if err := json.Unmarshal(b, &in); err != nil {
			return nil, fmt.Errorf("%s: %w", goldenInputs, err)
		}
		if spec == "client" {
			return in.ToolSets["client"], nil
		}
		server := toolNames(in.ToolSets["server"])
		var out []json.RawMessage
		for _, t := range in.ToolSets["node_dev"] {
			if n := toolNames([]json.RawMessage{t}); len(n) == 1 && !slices.Contains(server, n[0]) {
				out = append(out, t)
			}
		}
		return out, nil
	default:
		b, err := os.ReadFile(spec)
		if err != nil {
			return nil, err
		}
		var out []json.RawMessage
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, fmt.Errorf("%s: %w", spec, err)
		}
		return out, nil
	}
}

// LoadHome returns a node's agents payload (node_context.agents): "" or a name is
// tools/pipeline/homes/<name>.json ("" = avery), "none" is no payload, anything with a slash
// or a .json suffix is a file.
func LoadHome(root, spec string) (json.RawMessage, error) {
	switch {
	case spec == "none":
		return nil, nil
	case spec == "":
		spec = "avery"
	}
	path := spec
	if !strings.ContainsRune(spec, '/') && !strings.HasSuffix(spec, ".json") {
		path = filepath.Join(root, "tools/pipeline/homes", spec+".json")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("%s: not JSON", path)
	}
	return b, nil
}
