package selfupdate

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/appid"
)

// appIdentifier is the agent's code-signing identifier and bundle id.
const appIdentifier = appid.ID

// Codesigner reads and checks a macOS app's code signature.
type Codesigner interface {
	// Verify checks the signature seals the whole bundle (codesign --verify --deep --strict).
	Verify(app string) error
	// Info returns the signing identifier and team ("" for an ad-hoc signature).
	Info(app string) (identifier, team string, err error)
}

// codesignTool is Apple's, by absolute path (launchd's PATH is minimal).
const codesignTool = "/usr/bin/codesign"

// NativeCodesign is the codesign tool.
type NativeCodesign struct{}

func (NativeCodesign) run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var out bytes.Buffer
	c := exec.CommandContext(ctx, codesignTool, args...)
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	return out.String(), err
}

func (n NativeCodesign) Verify(app string) error {
	if out, err := n.run("--verify", "--deep", "--strict", app); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

func (n NativeCodesign) Info(app string) (string, string, error) {
	out, err := n.run("-d", "--verbose=2", app)
	if err != nil {
		return "", "", fmt.Errorf("codesign -d %s: %w: %s", app, err, strings.TrimSpace(out))
	}
	return parseCodesignInfo(out)
}

// parseCodesignInfo reads `codesign -d --verbose=2` output: Identifier= and TeamIdentifier=
// ("not set" for an ad-hoc signature).
func parseCodesignInfo(out string) (string, string, error) {
	var id, team string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if v, ok := strings.CutPrefix(l, "Identifier="); ok {
			id = v
		}
		if v, ok := strings.CutPrefix(l, "TeamIdentifier="); ok && v != "not set" {
			team = v
		}
	}
	if id == "" {
		return "", "", fmt.Errorf("codesign named no identifier")
	}
	return id, team, nil
}
