//go:build !linux && !darwin

package main

// leaderIs: elsewhere there is no way to tell, so yes.
func leaderIs(int, string) bool { return true }
