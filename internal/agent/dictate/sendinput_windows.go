//go:build windows

package dictate

import (
	"context"
	"strconv"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procSendInput           = user32.NewProc("SendInput")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
)

const inputKeyboard = 1

// keybdInput and input mirror KEYBDINPUT and INPUT: 40 bytes on 64-bit (the union is sized by
// MOUSEINPUT), 28 on 32-bit.
type keybdInput struct {
	vk, scan    uint16
	flags, time uint32
	extra       uintptr
}

type input struct {
	typ uint32
	ki  keybdInput
	_   [8]byte
}

func sendInput(in []KeyInput) (int, error) {
	if len(in) == 0 {
		return 0, nil
	}
	buf := make([]input, len(in))
	for i, k := range in {
		buf[i] = input{typ: inputKeyboard, ki: keybdInput{vk: k.VK, scan: k.Scan, flags: k.Flags}}
	}
	r, _, err := procSendInput.Call(uintptr(len(buf)), uintptr(unsafe.Pointer(&buf[0])), unsafe.Sizeof(buf[0]))
	if r == 0 {
		return 0, err
	}
	return int(r), nil
}

func nativeSendInput() func([]KeyInput) (int, error) { return sendInput }

// foreground is the HWND in front, as the decimal id the host backend uses.
func foreground(context.Context) (string, error) {
	h, _, _ := procGetForegroundWindow.Call()
	if h == 0 {
		return "", nil
	}
	return strconv.FormatUint(uint64(h), 10), nil
}

func nativeForeground() func(context.Context) (string, error) { return foreground }
