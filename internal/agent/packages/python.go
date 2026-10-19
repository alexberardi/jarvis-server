package packages

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

// uvVersion is the uv the agent downloads when JARVIS_AGENT_UV doesn't name one. Pinned with
// its checksums: the agent runs what it verified, never "latest".
const uvVersion = "0.12.24"

// uvAssets are uvVersion's release archives and their SHA-256, by GOOS/GOARCH.
var uvAssets = map[string]struct{ file, sha256 string }{
	"darwin/arm64":  {"uv-aarch64-apple-darwin.tar.gz", "0c4346de7abdb49495b393b9ec809fe387aa43e586be20fecb972216c1e71732"},
	"darwin/amd64":  {"uv-x86_64-apple-darwin.tar.gz", "4fa82e37cb94767661f532b001e470b67a186c7260e305bd84ddb78fd545c0b6"},
	"linux/arm64":   {"uv-aarch64-unknown-linux-gnu.tar.gz", "5231be65f496304623895dacdbf1de8504fec90303684bdf05805aa34414dd21"},
	"linux/amd64":   {"uv-x86_64-unknown-linux-gnu.tar.gz", "b4dfaef47d491a7296981f8374a4595f55dbf84e8937c8ecd2983574d8bb3da6"},
	"linux/arm":     {"uv-armv7-unknown-linux-gnueabihf.tar.gz", "7ae5772338fb28f33052c06f82d1acb245df7bc1cb7628184a9baf91e9a101cd"},
	"windows/amd64": {"uv-x86_64-pc-windows-msvc.zip", "7c38608c8a18ee137d748a1773053b07ec8f3a30fab49aebaa6f4e4efeceb019"},
	"windows/arm64": {"uv-aarch64-pc-windows-msvc.zip", "4b783bda5cc44bbae0651a837223873a7acee31152381aef5fc86ef6bef25997"},
}

// uvReleaseBase is where uv's release archives are; a test points it at a fake.
var uvReleaseBase = "https://github.com/astral-sh/uv/releases/download/"

// SDKRequirement is the jarvis-command-sdk every venv gets, pinned to a commit (the SDK isn't
// on PyPI; CI's sidecar test installs the same tree). JARVIS_AGENT_SDK overrides it, e.g. with
// a local checkout's path. Move it forward when the sidecar script needs a newer SDK.
const SDKRequirement = "jarvis-command-sdk @ https://github.com/alexberardi/jarvis-command-sdk/archive/708193cc3e74d23294cf05625dabdac61336d57e.tar.gz"

// NodeCommandsRequirement is the Pi's built-in commands: jarvis-node-setup pinned to a commit
// (its pyproject packages commands*, core*, services*, …, and needs only the SDK >= 0.13.0).
// JARVIS_AGENT_NODE_COMMANDS overrides it, e.g. with a local checkout's path.
const NodeCommandsRequirement = "jarvis-node-setup @ https://github.com/alexberardi/jarvis-node-setup/archive/962bb56f256208cf029c16565a7ce5df0709ea8d.tar.gz"

// PiBuiltins are the built-in commands the Pi profile loads: every module of jarvis-node-setup's
// commands package but those that still call the Python node's own code
// (docs/decisions/2026-10-18-pi-builtin-commands.md).
func PiBuiltins(requirement string) *Builtins {
	if requirement == "" {
		requirement = NodeCommandsRequirement
	}
	return &Builtins{Requirement: requirement, Package: "commands",
		Skip: []string{"control_device_command", "routine_command", "send_link_command"}}
}

// pythonVersion is the interpreter uv installs for the venv: its own standalone build, so the
// sidecar doesn't depend on (or break with) a system or Homebrew Python.
const pythonVersion = "3.12"

// Runner runs one program and returns its combined output. Tests replace it; ExecRunner runs it.
type Runner func(ctx context.Context, env []string, name string, args ...string) ([]byte, error)

func ExecRunner(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = env
	return cmd.CombinedOutput()
}

// Python is the sidecar's interpreter: a uv-managed venv under Root/venv, uv's own Pythons
// under Root/python, and uv itself at Root/bin unless UV names one.
type Python struct {
	Root   string
	UV     string // a uv to use instead of downloading uvVersion
	SDK    string // the SDK requirement; "" is SDKRequirement
	HTTP   *http.Client
	Run    Runner
	GOOS   string
	GOARCH string
}

func (p *Python) exe(name string) string {
	if p.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// Interpreter is the venv's python.
func (p *Python) Interpreter() string {
	if p.GOOS == "windows" {
		return filepath.Join(p.Root, "venv", "Scripts", "python.exe")
	}
	return filepath.Join(p.Root, "venv", "bin", "python")
}

// Ready reports whether the venv exists.
func (p *Python) Ready() bool {
	_, err := os.Stat(p.Interpreter())
	return err == nil
}

func (p *Python) env() []string {
	return append(os.Environ(),
		"UV_PYTHON_INSTALL_DIR="+filepath.Join(p.Root, "python"),
		"UV_PYTHON_PREFERENCE=only-managed",
		"UV_NO_PROGRESS=1",
	)
}

// Install makes the venv if it is missing and installs the SDK and reqs into it.
func (p *Python) Install(ctx context.Context, reqs []string) error {
	uv, err := p.uv(ctx)
	if err != nil {
		return err
	}
	if !p.Ready() {
		if out, err := p.Run(ctx, p.env(), uv, "venv", "--python", pythonVersion, filepath.Join(p.Root, "venv")); err != nil {
			return fmt.Errorf("making the Python environment: %v: %s", err, tail(out))
		}
	}
	sdk := p.SDK
	if sdk == "" {
		sdk = SDKRequirement
	}
	args := append([]string{"pip", "install", "--python", p.Interpreter(), "--quiet", sdk}, reqs...)
	if out, err := p.Run(ctx, p.env(), uv, args...); err != nil {
		return fmt.Errorf("installing Python packages: %v: %s", err, tail(out))
	}
	return nil
}

// uv is the uv to run: UV, else uvVersion under Root/bin, downloaded and checked on first use.
func (p *Python) uv(ctx context.Context) (string, error) {
	if p.UV != "" {
		return p.UV, nil
	}
	bin := filepath.Join(p.Root, "bin", p.exe("uv"))
	if _, err := os.Stat(bin); err == nil {
		return bin, nil
	}
	asset, ok := uvAssets[p.GOOS+"/"+p.GOARCH]
	if !ok {
		return "", fmt.Errorf("packages aren't supported on %s/%s yet", p.GOOS, p.GOARCH)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uvReleaseBase+uvVersion+"/"+asset.file, nil)
	if err != nil {
		return "", err
	}
	resp, err := p.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("downloading uv: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading uv: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 128<<20))
	if err != nil {
		return "", fmt.Errorf("downloading uv: %w", err)
	}
	if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != asset.sha256 {
		return "", errors.New("downloading uv: checksum mismatch")
	}
	exe, err := findInArchive(b, asset.file, p.exe("uv"))
	if err != nil {
		return "", fmt.Errorf("uv archive: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		return "", err
	}
	tmp := bin + ".tmp"
	if err := os.WriteFile(tmp, exe, 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, bin); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return bin, nil
}

// findInArchive returns the file called base from a .tar.gz or .zip.
func findInArchive(b []byte, archive, base string) ([]byte, error) {
	if strings.HasSuffix(archive, ".zip") {
		zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Base(f.Name) == base && !f.FileInfo().IsDir() {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(io.LimitReader(rc, 256<<20))
			}
		}
		return nil, fmt.Errorf("no %s in it", base)
	}
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("no %s in it", base)
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeReg && path.Base(h.Name) == base {
			return io.ReadAll(io.LimitReader(tr, 256<<20))
		}
	}
}

// tail is the end of a tool's output, for an error message the app can show.
func tail(out []byte) string {
	s := strings.TrimSpace(string(out))
	if len(s) > 600 {
		s = "…" + s[len(s)-600:]
	}
	return s
}
