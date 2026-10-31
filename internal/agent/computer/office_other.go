//go:build !linux && !darwin

package computer

import (
	"context"
	"errors"
)

// shortcut: no documents on Windows yet; add LibreOffice's program\python.exe when Windows desktop tasks land.
func nativeOffice(context.Context, map[string]any) (map[string]any, error) {
	return nil, errors.New("documents aren't supported on this computer yet")
}
