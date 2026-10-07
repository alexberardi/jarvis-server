//go:build !unix

package main

// restrictUmask is a no-op: Windows has no umask; the home directory's ACL (set by
// `jarvisd service install`) protects the data.
func restrictUmask() {}
