//go:build !windows

package dictate

import "context"

func nativeSendInput() func([]KeyInput) (int, error) { return nil }

func nativeForeground() func(context.Context) (string, error) { return nil }
