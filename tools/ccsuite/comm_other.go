//go:build !darwin

package main

// procComm has no source here (Linux reads /proc instead).
func procComm(int) string { return "" }
