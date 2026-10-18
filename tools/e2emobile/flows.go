package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// skipTag marks a flow the runner does not run (biometrics, a dev-client build, ...); the flow
// says why in properties.skip_reason.
const skipTag = "skip"

// Flow is one top-level Maestro flow file of the mobile checkout's .maestro/.
type Flow struct {
	Path       string // absolute
	File       string // relative to the checkout, e.g. .maestro/login-admin.yaml
	Slug       string // the file name without .yaml: the per-flow artifact directory
	Name       string // the header's name, else Slug
	Tags       []string
	SkipReason string // set when the flow is tagged skip
}

func (f Flow) Skipped() bool { return contains(f.Tags, skipTag) }

// header is the part of a flow's config section (the YAML document before `---`) the runner reads.
type header struct {
	AppID      string            `yaml:"appId"`
	Name       string            `yaml:"name"`
	Tags       []string          `yaml:"tags"`
	Properties map[string]string `yaml:"properties"`
}

// docSep is a line that is exactly `---`: the end of a flow's config section.
var docSep = regexp.MustCompile(`(?m)^---[ \t]*$`)

// parseHeader reads a flow's config section. A file without `---` has none (Maestro then
// treats the whole file as commands); its header is empty.
func parseHeader(raw []byte) (header, error) {
	var h header
	loc := docSep.FindIndex(raw)
	if loc == nil {
		return h, nil
	}
	if err := yaml.Unmarshal(raw[:loc[0]], &h); err != nil {
		return h, err
	}
	return h, nil
}

// Discover lists the flows in dir (a checkout's .maestro/): its top-level .yaml/.yml files,
// sorted by name, except Maestro's workspace config.yaml. Subdirectories hold subflows
// (`runFlow: subflows/login.yaml`) and are never run on their own. rel is the prefix for
// Flow.File (".maestro"). filter, when set, keeps the flows whose name or file name matches.
func Discover(dir, rel string, filter *regexp.Regexp) ([]Flow, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var flows []Flow
	for _, e := range ents {
		n := e.Name()
		ext := filepath.Ext(n)
		if e.IsDir() || (ext != ".yaml" && ext != ".yml") || strings.TrimSuffix(n, ext) == "config" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		h, err := parseHeader(bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		f := Flow{
			Path: filepath.Join(dir, n),
			File: filepath.ToSlash(filepath.Join(rel, n)),
			Slug: strings.TrimSuffix(n, ext),
			Name: strings.TrimSpace(h.Name),
			Tags: h.Tags,
		}
		if f.Name == "" {
			f.Name = f.Slug
		}
		if f.Skipped() {
			f.SkipReason = strings.TrimSpace(h.Properties["skip_reason"])
			if f.SkipReason == "" {
				f.SkipReason = "tagged skip (no properties.skip_reason given)"
			}
		}
		if filter != nil && !filter.MatchString(f.Name) && !filter.MatchString(f.Slug) {
			continue
		}
		flows = append(flows, f)
	}
	sort.Slice(flows, func(i, j int) bool { return flows[i].Slug < flows[j].Slug })
	return flows, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
