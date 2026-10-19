package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// leaderIs tells whether process pid, if it is still there, still runs argv0's program (by name,
// as Chromium may retitle itself; when the leader is gone and only its group is left, yes).
func leaderIs(pid int, argv0 string) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(b) == 0 {
		return true
	}
	return strings.Contains(string(b), filepath.Base(argv0))
}
