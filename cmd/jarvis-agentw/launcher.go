// jarvis-agentw starts jarvis-agent on Windows without a console window (01-no-cli-plan item
// 2.3; DESIGN §13 "The console window"). jarvis-agent.exe is a console program, so the CLI
// (status, enroll's password prompt, the browsers' native-messaging pipes) works from any
// terminal; started by the Run key, a shortcut or the jarvis-agent:// handler, it would open a
// console window. This launcher is a GUI program (built with -ldflags -H=windowsgui): it starts
// the jarvis-agent.exe beside it with the same arguments in a console that has no window, and
// exits. The agent's own children (the Python sidecar, uv) share that hidden console, so they
// show none either.
//
// `jarvis-agent install` points the Run key, the shortcuts and the URL handler at this launcher
// when it sits beside jarvis-agent.exe (the Windows installer puts it there), and at
// jarvis-agent.exe otherwise. It holds no logic of its own and is never self-updated: any
// version starts any agent.
package main

import "path/filepath"

// agentName is the program the launcher starts, from its own directory.
const agentName = "jarvis-agent.exe"

// agentBeside is the jarvis-agent.exe next to the launcher at self.
func agentBeside(self string) string { return filepath.Join(filepath.Dir(self), agentName) }
