# sherpa-onnx (vendored header + fetched libraries)

- `include/c-api.h` is the sherpa-onnx **v1.13.8** C API header, used only by the struct-layout test. Licensed under Apache-2.0 by k2-fsa.
- `internal/voice/sherpa/libs/<goos>-<goarch>/` (it must sit beside the package for `go:embed`) holds the shared libraries that `jarvisd` embeds: sherpa-onnx-c-api and onnxruntime. They are **not committed**. Fetch them with `scripts/fetch-sherpa-libs.sh`; CI runs it before building.

To bump the version: update `SHERPA_VERSION` in the fetch script, replace the header, and run `go test ./internal/voice/sherpa/ -run Layout`. The layout test fails if any mirrored struct drifted.
