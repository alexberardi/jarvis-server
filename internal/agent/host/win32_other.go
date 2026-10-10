//go:build !windows

package host

import "errors"

var errNoWin32 = errors.New("win32 is only available on windows")

// noWin32 is the Win32 of a non-Windows build: nothing is available.
type noWin32 struct{}

// NewWin32 returns the platform Win32. Off Windows it is a stub reporting nothing available.
func NewWin32() Win32 { return noWin32{} }

func (noWin32) AudioAvailable() bool           { return false }
func (noWin32) VolumeScalar() (float32, error) { return 0, errNoWin32 }
func (noWin32) SetVolumeScalar(float32) error  { return errNoWin32 }
func (noWin32) DesktopAvailable() bool         { return false }
func (noWin32) EnumWindows() ([]Window, error) { return nil, errNoWin32 }
func (noWin32) IsWindow(uintptr) bool          { return false }
func (noWin32) Minimize(uintptr) error         { return errNoWin32 }
func (noWin32) Focus(uintptr) error            { return errNoWin32 }
func (noWin32) PostClose(uintptr) error        { return errNoWin32 }
func (noWin32) ShellOpen(string) error         { return errNoWin32 }
