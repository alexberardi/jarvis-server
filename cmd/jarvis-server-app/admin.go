package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

// shellJoin quotes each word for /bin/sh.
func shellJoin(words ...string) string {
	q := make([]string, len(words))
	for i, w := range words {
		if w != "" && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./=:@") == "" {
			q[i] = w
			continue
		}
		q[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return strings.Join(q, " ")
}

// appleScriptString is s as an AppleScript string literal.
func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// adminScript is the AppleScript that runs script as root behind the standard administrator
// password dialog (00-installers §8.3, install-from-app). Line endings are kept as they are, so
// stdout's last line is the URL.
func adminScript(prompt, script string) string {
	return "do shell script " + appleScriptString(script) + " with prompt " + appleScriptString(prompt) +
		" with administrator privileges without altering line endings"
}

// osascriptError is "0:123: execution error: <message> (<number>)".
var osascriptError = regexp.MustCompile(`(?s)execution error: (.*) \((-?\d+)\)\s*$`)

// adminError turns osascript's stderr into the error to show: errCanceled for the dialog's
// Cancel (-128), else the message, which for a failed script is what it wrote to stderr.
func adminError(stderr string, err error) error {
	m := osascriptError.FindStringSubmatch(strings.TrimSpace(stderr))
	switch {
	case m == nil:
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr))
	case m[2] == "-128":
		return errCanceled
	default:
		return errors.New(strings.TrimSpace(m[1]))
	}
}

// osascriptAdmin runs script as root after the administrator password dialog.
func osascriptAdmin(ctx context.Context, prompt, script string) (string, error) {
	var out, errb bytes.Buffer
	c := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", adminScript(prompt, script))
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.String(), adminError(errb.String(), err)
	}
	return out.String(), nil
}

// sudoAdmin is the non-interactive mode's root: sudo without a password (a CI runner, MDM).
func sudoAdmin(ctx context.Context, _ string, script string) (string, error) {
	var out, errb bytes.Buffer
	c := exec.CommandContext(ctx, "sudo", "-n", "/bin/sh", "-c", script)
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// runUser runs a program as this user: stdout is returned, stderr folded into the error.
func runUser(ctx context.Context, name string, args ...string) ([]byte, error) {
	var out, errb bytes.Buffer
	c := exec.CommandContext(ctx, name, args...)
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.Bytes(), fmt.Errorf("%w: %s", err, strings.TrimSpace(errb.String()))
	}
	return out.Bytes(), nil
}
