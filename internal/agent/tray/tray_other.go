//go:build !linux && !darwin

package tray

// Start shows nothing here yet.
func Start(o Options) (*Tray, error) { return nil, ErrUnsupported }

// Probe says, for `status`, whether this desktop shows the icon.
func Probe() string { return "not on this OS yet" }
