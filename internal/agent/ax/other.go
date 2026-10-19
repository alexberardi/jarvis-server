//go:build !darwin

package ax

import "github.com/alexberardi/jarvis-server/internal/computeruse"

func frontWindow(int) (axNode, error) { return nil, ErrUnsupported }

func elementAt(float64, float64) (*computeruse.Element, error) { return nil, ErrUnsupported }

func recordedAt(float64, float64) (*Recorded, error) { return nil, ErrUnsupported }

func focusedElement() (*Recorded, error) { return nil, ErrUnsupported }

func forgetApps() {}
