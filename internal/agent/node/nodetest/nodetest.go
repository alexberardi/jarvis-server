// Package nodetest holds test helpers shared by the node's toolsets.
package nodetest

import (
	"context"
	_ "embed"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

//go:embed ordinary-speech.txt
var ordinarySpeech string

// OrdinarySpeech is the never-direct corpus: room talk and sentences that share a command's
// words, which funnel stage 1 must leave to the model.
func OrdinarySpeech() []string {
	var out []string
	for _, l := range strings.Split(ordinarySpeech, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

// NeverDirect fails t for every corpus line d carries out itself.
func NeverDirect(t *testing.T, d node.Director) {
	t.Helper()
	for _, said := range OrdinarySpeech() {
		if out, ok := d.Direct(context.Background(), said); ok {
			t.Errorf("%q was carried out on the node: %v", said, out)
		}
	}
}
