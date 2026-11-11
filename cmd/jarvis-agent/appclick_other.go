//go:build !linux

package main

import (
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/targets"
)

// newAppClick: voice clicks in desktop apps are Linux-only for now (milestone 2: UIA, AX).
func newAppClick(func() bool, targets.Picker, *dictate.Confirmer) node.Toolset { return nil }
