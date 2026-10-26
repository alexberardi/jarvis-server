//go:build !linux

package views

import (
	"github.com/alexberardi/jarvis-server/internal/agent/desktop"
	"github.com/alexberardi/jarvis-server/internal/agent/host"
)

// Native is nil off Linux for now: macOS (AX) and Windows (UI Automation) come next (Q26).
func Native(*host.Host) desktop.ViewSource { return nil }
