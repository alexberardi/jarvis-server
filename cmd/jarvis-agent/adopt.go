package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/pinode"
)

// adoptPython takes over the Pi's provisioned Python node's identity and state when this agent
// has no node.json (go-pi-node.md, Retirement step 2): false when there is none to adopt.
func adoptPython(ctx context.Context, log *slog.Logger, path string, py pinode.PythonNode) (bool, error) {
	c, err := py.Adopt(ctx, path)
	if errors.Is(err, pinode.ErrNotProvisioned) || errors.Is(err, pinode.ErrEnrolled) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("adopting the Python node's identity: %w", err)
	}
	log.Info("adopted the Python node's identity "+c.NodeID, "room", c.Room)
	return true, nil
}

// adoptPythonCmd is `jarvis-agent adopt-python`, install-pi.sh --activate's first step.
func adoptPythonCmd(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("adopt-python", flag.ContinueOnError)
	credsFlag := fs.String("creds", "", "credentials file (default: the user config dir's jarvis-agent/node.json)")
	py, err := pinode.DefaultPythonNode()
	if err != nil {
		return err
	}
	fs.StringVar(&py.InstallDir, "python-dir", py.InstallDir, "the Python node's install dir (config.json, jarvis_node.db, .venv)")
	fs.StringVar(&py.SecretDir, "secret-dir", py.SecretDir, "the Python node's secret dir (.provisioned, db.key, K1, K2)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	path, err := credsPath(*credsFlag)
	if err != nil {
		return err
	}
	_, err = adoptPython(ctx, logger(), path, py)
	return err
}
