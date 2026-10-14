# Sourced by the dev scripts that build or vet Go (scripts/sandbox, scripts/check,
# scripts/desktop-shot). Uses the `go` on PATH when there is one. Otherwise it defines `go` and
# `gofmt` as the repo's pinned toolchain through mise (CLAUDE.md: `mise exec go@1.27 -- go`,
# GOTOOLCHAIN=local so no toolchain is downloaded behind its back). With neither, `go` fails
# when called, so the commands that need no Go (sandbox down, status) still work.
# Callers keep writing `go build ...` and `CGO_ENABLED=0 go vet ...`: bash passes such
# assignments on to a function's commands.
if ! command -v go >/dev/null 2>&1; then
  if command -v mise >/dev/null 2>&1; then
    go() { GOTOOLCHAIN=local mise exec go@1.27 -- go "$@"; }
    gofmt() { GOTOOLCHAIN=local mise exec go@1.27 -- gofmt "$@"; }
  else
    go() { echo "$(basename "$0"): go is not on PATH and mise is not installed (want: mise exec go@1.27 -- go)" >&2; return 127; }
    gofmt() { go; }
  fi
fi
