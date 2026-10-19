//go:build darwin

package input

import (
	"context"
	"time"
)

// NativeMonitor is the HID system state's last input event (CGEventSourceSecondsSinceLastEventType):
// the user's mouse and keyboard, apart from our own events, which post from a private source.
func NativeMonitor() Monitor { return hidMonitor{} }

type hidMonitor struct{}

func (hidMonitor) Since(context.Context) (time.Time, error) {
	if err := loadCG(); err != nil {
		return time.Time{}, err
	}
	secs := cgSecondsSinceLastEvent(cgStateHIDSystem, cgAnyInputEvent)
	return time.Now().Add(-time.Duration(secs * float64(time.Second))), nil
}
