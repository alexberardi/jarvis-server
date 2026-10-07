//go:build !linux && !darwin && !windows

package sysinfo

func totalMemory() uint64         { return 0 }
func release() string             { return "" }
func diskFree(path string) uint64 { return 0 }
