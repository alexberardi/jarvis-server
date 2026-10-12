package main

import "runtime"

// AppKit runs only on the main thread: keep the main goroutine there, so `run --voice` can hand
// it to the island's event loop (island.Run).
func init() { runtime.LockOSThread() }
