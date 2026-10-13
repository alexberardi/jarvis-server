package control

import "os"

// mkShortTemp is a temp dir short enough for a Unix socket path (~104 bytes on macOS).
func mkShortTemp() (string, error) { return os.MkdirTemp("", "jc") }

func removeAll(d string) { _ = os.RemoveAll(d) }
