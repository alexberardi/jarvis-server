#!/usr/bin/env bash
# Fetch sherpa-onnx + onnxruntime shared libraries for embedding into jarvisd.
# Usage: scripts/fetch-sherpa-libs.sh [goos-goarch ...]   (default: host platform)
set -euo pipefail

SHERPA_VERSION="1.13.8"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/internal/voice/sherpa/libs"
BASE="https://github.com/k2-fsa/sherpa-onnx/releases/download/v${SHERPA_VERSION}"

# goos-goarch -> release tarball (CPU, shared libs). Windows uses the MT (static CRT) build so no
# VC++ redistributable is needed on the target machine.
declare -A TARBALL=(
  [linux-amd64]="sherpa-onnx-v${SHERPA_VERSION}-linux-x64-shared-lib"
  [linux-arm64]="sherpa-onnx-v${SHERPA_VERSION}-linux-aarch64-shared-cpu-lib"
  [darwin-arm64]="sherpa-onnx-v${SHERPA_VERSION}-osx-arm64-shared-lib"
  [windows-amd64]="sherpa-onnx-v${SHERPA_VERSION}-win-x64-shared-MT-Release-lib"
)

host="$(go env GOOS 2>/dev/null || mise exec go@1.25 -- go env GOOS)-$(go env GOARCH 2>/dev/null || mise exec go@1.25 -- go env GOARCH)"
platforms=("$@")
[ ${#platforms[@]} -eq 0 ] && platforms=("$host")

for p in "${platforms[@]}"; do
  name="${TARBALL[$p]:?unsupported platform $p}"
  out="$DEST/$p"
  if [ -f "$out/.version" ] && [ "$(cat "$out/.version")" = "$SHERPA_VERSION" ]; then
    echo "$p: up to date"; continue
  fi
  tmp="$(mktemp -d)"
  echo "$p: downloading $name"
  curl -fsSL "$BASE/$name.tar.bz2" | tar xj -C "$tmp"
  rm -rf "$out" && mkdir -p "$out"
  # Keep only the two runtime libraries we load.
  find "$tmp" -type f \( -name 'libonnxruntime*.so*' -o -name 'libsherpa-onnx-c-api.so' \
       -o -name 'libonnxruntime*.dylib' -o -name 'libsherpa-onnx-c-api.dylib' \
       -o -name 'onnxruntime.dll' -o -name 'sherpa-onnx-c-api.dll' \) -exec cp {} "$out/" \;
  echo "$SHERPA_VERSION" > "$out/.version"
  rm -rf "$tmp"
  ls -la "$out"
done
