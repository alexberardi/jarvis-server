//go:build !windows

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "jarvis-agentw is the Windows launcher of jarvis-agent; run jarvis-agent instead")
	os.Exit(2)
}
