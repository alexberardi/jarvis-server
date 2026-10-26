//go:build !linux

package main

import "log/slog"

// regroupInput: only Linux has the input group.
func regroupInput(*slog.Logger) {}
