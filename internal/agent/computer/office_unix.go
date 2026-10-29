//go:build linux || darwin

package computer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// macOffice is LibreOffice's app on macOS: its soffice and the Python it bundles with uno.
const macOffice = "/Applications/LibreOffice.app/Contents"

// nativeOffice runs office.py with LibreOffice's Python, in its own process group so a timeout
// stops LibreOffice too.
func nativeOffice(ctx context.Context, req map[string]any) (map[string]any, error) {
	soffice, python, err := findOffice()
	if err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(cache, "jarvis-agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	script := filepath.Join(dir, "office.py")
	if err := os.WriteFile(script, officeScript, 0o600); err != nil {
		return nil, err
	}
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	req["soffice"], req["profile"], req["pipe"] = soffice, filepath.Join(dir, "office-profile"), "jarvis-office-"+hex.EncodeToString(id)
	in, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, python, script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Stdin = bytes.NewReader(in)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("LibreOffice took too long")
		}
		return nil, fmt.Errorf("LibreOffice script: %v: %.300s", err, stderr.String())
	}
	return decodeOffice(stdout.Bytes())
}

func findOffice() (soffice, python string, err error) {
	if runtime.GOOS == "darwin" {
		soffice, python = macOffice+"/MacOS/soffice", macOffice+"/Resources/python"
		if _, err := os.Stat(soffice); err != nil {
			return "", "", errors.New("LibreOffice isn't installed")
		}
		return soffice, python, nil
	}
	if soffice, err = exec.LookPath("soffice"); err != nil {
		return "", "", errors.New("LibreOffice isn't installed")
	}
	// shortcut: Linux uses the system python3 with LibreOffice's uno module (python3-uno / libreoffice-*); a missing one fails the script with its import error.
	if python, err = exec.LookPath("python3"); err != nil {
		return "", "", errors.New("no python3 for LibreOffice's scripting")
	}
	return soffice, python, nil
}
