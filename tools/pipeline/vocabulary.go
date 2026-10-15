package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alexberardi/jarvis-server/internal/agent/apps"
)

const (
	// desktopToolset is the copy of the desktop agent's tools a desktop scenario uses.
	desktopToolset = "tools/pipeline/toolsets/desktop.json"
	// desktopApps is a typical desktop agent's app list, next to its tools.
	desktopApps = "tools/pipeline/toolsets/desktop.apps.json"
	// maxVocabulary is the agent's cut (dictate.MaxVocabulary; a test keeps them equal).
	maxVocabulary = 400
)

// DesktopVocabulary is the node_context.stt_vocabulary a typical desktop agent sends: the
// fixture's app list made into the agent's string by the agent's own apps.Vocabulary (open
// windows first, then the catalogue's one- and two-word names, cut at maxVocabulary).
func DesktopVocabulary(root string) (string, error) {
	path := filepath.Join(root, desktopApps)
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var in struct {
		Open []string `json:"open"`
		Apps []string `json:"apps"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	list := make([]apps.App, 0, len(in.Apps))
	for _, n := range in.Apps {
		list = append(list, apps.App{Name: n})
	}
	return apps.Vocabulary(in.Open, list, maxVocabulary), nil
}

// resolveToolSpec is a scenario's tool set with a file path made relative to the scenario's
// file: what LoadToolSet takes.
// A "desktop" node with no tool set sends the desktop agent's ("desktop"), as a real one does.
func resolveToolSpec(sc Scenario) string {
	spec := sc.Tools
	if spec == "" && sc.Node == "desktop" {
		return "desktop"
	}
	if spec != "" && spec != "node" && spec != "client" && spec != "none" && spec != "desktop" && sc.File != "" && !filepath.IsAbs(spec) {
		spec = filepath.Join(filepath.Dir(sc.File), spec)
	}
	return spec
}

// scenarioVocabulary is what a scenario sends as node_context.stt_vocabulary: its own when it
// sets one ("" sends none), else, for a desktop scenario (the desktop toolset or "node":
// "desktop"), the typical desktop agent's (DesktopVocabulary). A real desktop agent always
// sends its app names, and a desktop scenario without them tests a turn no user has.
func scenarioVocabulary(root string, sc Scenario, toolSpec string) (string, error) {
	if sc.STTVocabulary != nil {
		return *sc.STTVocabulary, nil
	}
	if sc.Node == "desktop" || isDesktopToolset(root, toolSpec) {
		return DesktopVocabulary(root)
	}
	return "", nil
}

func isDesktopToolset(root, spec string) bool {
	switch spec {
	case "", "node", "client", "none":
		return false
	case "desktop":
		return true
	}
	a, err1 := filepath.Abs(spec)
	b, err2 := filepath.Abs(filepath.Join(root, desktopToolset))
	return err1 == nil && err2 == nil && a == b
}
