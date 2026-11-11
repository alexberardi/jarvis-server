//go:build !linux

package main

import (
	"context"
	"log/slog"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/targets"
)

// newAppClick: voice clicks in desktop apps are Linux-only for now (milestone 2: UIA, AX).
func newAppClick(func() bool, targets.Picker, *dictate.Confirmer) node.Toolset { return nil }

func enableAppElements(context.Context, *slog.Logger) {}

// appElementsStatus: "" where status has no such line (macOS reports its Accessibility grant).
func appElementsStatus(context.Context) string { return "" }
