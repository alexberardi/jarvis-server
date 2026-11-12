package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/alexberardi/jarvis-server/internal/platform/config"
)

// --llm cloud: the suite's sandbox comes up without a model, then its live and cloud labels
// point at Anthropic with vision on, and computer.model is cloud, so computer tasks run on the
// optional cloud slot as a household that picked it would (docs/agent/PLAN-computer-use.md
// task 1). The key never reaches a log, a report or an error.
type cloud struct {
	Key, Model, URL string
}

const (
	cloudDefaultModel = "claude-sonnet-5-5"
	cloudDefaultURL   = "https://api.anthropic.com"
)

// cloudConfig reads JARVIS_TEST_ANTHROPIC_{KEY,MODEL,URL} (the live tests' names) from the
// environment, else from the cloud env file ($JARVIS_CLOUD_ENV, default
// ~/.config/jarvis-harness/cloud.env).
func cloudConfig() (cloud, error) {
	file := os.Getenv("JARVIS_CLOUD_ENV")
	if file == "" {
		home, _ := os.UserHomeDir()
		file = filepath.Join(home, ".config", "jarvis-harness", "cloud.env")
	}
	vals := map[string]string{}
	if f, err := os.Open(file); err == nil {
		kvs, err := config.ParseEnv(f)
		f.Close()
		if err != nil {
			return cloud{}, fmt.Errorf("%s: %w", file, err)
		}
		for _, kv := range kvs {
			vals[kv[0]] = kv[1]
		}
	}
	get := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		if v := vals[k]; v != "" {
			return v
		}
		return def
	}
	c := cloud{Key: get("JARVIS_TEST_ANTHROPIC_KEY", ""), Model: get("JARVIS_TEST_ANTHROPIC_MODEL", cloudDefaultModel),
		URL: get("JARVIS_TEST_ANTHROPIC_URL", cloudDefaultURL)}
	if c.Key == "" {
		return cloud{}, errors.New("--llm cloud: set JARVIS_TEST_ANTHROPIC_KEY, or put it in " + file)
	}
	return c, nil
}

// labels is the PUT /v1/models/labels body: live (the conversation and the planner) and cloud
// (computer tasks) on the provider, with vision on.
func (c cloud) labels() map[string]map[string]any {
	l := func() map[string]any {
		return map[string]any{"engine": "remote", "remote_url": c.URL, "remote_model": c.Model,
			"remote_api_key": c.Key, "remote_api": "auto", "remote_vision": true}
	}
	return map[string]map[string]any{"live": l(), "cloud": l()}
}

// String is the config without its key, for logs.
func (c cloud) String() string { return c.Model + " at " + c.URL }

// useCloud points the sandbox's labels at the provider and computer tasks at the cloud slot.
func (e *Env) useCloud(ctx context.Context, c cloud) error {
	e.logf("llm: cloud, %s", c)
	llmURL := e.Sbx["SBX_LLM"]
	if llmURL == "" {
		return errors.New("sandbox.env has no SBX_LLM")
	}
	// The error carries the status and the server's answer, never the request body.
	if err := e.api(ctx, "PUT", llmURL+"/v1/models/labels", c.labels(), nil); err != nil {
		return fmt.Errorf("cloud labels: %w", err)
	}
	if err := e.api(ctx, "PUT", e.Sbx["SBX_CC"]+"/settings/computer.model", map[string]any{"value": "cloud"}, nil); err != nil {
		return fmt.Errorf("computer.model: %w", err)
	}
	e.Model, e.Label = c.Model, "cloud"
	return nil
}
