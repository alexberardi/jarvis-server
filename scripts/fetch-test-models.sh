#!/usr/bin/env bash
# Download the models used by the voice integration tests into .models/ (gitignored).
# Then: JARVIS_SHERPA_MODELS=$PWD/.models go test ./internal/voice/...
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
DEST="$ROOT/.models"
R="https://github.com/k2-fsa/sherpa-onnx/releases/download"
mkdir -p "$DEST"
cd "$DEST"
[ -f 3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx ] || \
  curl -fsSLO "$R/speaker-recongition-models/3dspeaker_speech_eres2net_sv_en_voxceleb_16k.onnx"
[ -d kokoro-multi-lang-v1_0 ] || curl -fsSL "$R/tts-models/kokoro-multi-lang-v1_0.tar.bz2" | tar xj
# openWakeWord (internal/voice/oww): the feature models plus hey_jarvis, as openwakeword 0.6.0
# downloads them.
OWW="https://github.com/dscripka/openWakeWord/releases/download/v0.5.1"
mkdir -p openwakeword
for m in melspectrogram.onnx embedding_model.onnx hey_jarvis_v0.1.onnx; do
  [ -f "openwakeword/$m" ] || curl -fsSL -o "openwakeword/$m" "$OWW/$m"
done
ls "$DEST"
