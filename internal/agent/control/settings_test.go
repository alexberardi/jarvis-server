package control

import (
	"errors"
	"net"
	"os"
	"testing"
)

func TestSettingsDoesNotTreatPermissionFailureAsStopped(t *testing.T) {
	_, err := AskSettings(func() (net.Conn, error) { return nil, os.ErrPermission }, nil)
	if err == nil || errors.Is(err, ErrNotRunning) {
		t.Fatalf("unsafe fallback: %v", err)
	}
	_, err = AskSettings(func() (net.Conn, error) { return nil, os.ErrNotExist }, nil)
	if !errors.Is(err, ErrNotRunning) {
		t.Fatal(err)
	}
}
