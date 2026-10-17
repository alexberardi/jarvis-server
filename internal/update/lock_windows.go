package update

import (
	"errors"

	"golang.org/x/sys/windows"
)

// stillActive is GetExitCodeProcess's code for a process that hasn't exited (STILL_ACTIVE).
const stillActive = 259

// processAlive reports whether pid is running. A process this account may not open is alive.
func processAlive(pid int) bool {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return !errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return true
	}
	return code == stillActive
}
