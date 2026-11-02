package sidecar

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/devicetypes"
)

// jarvisd's embedded device-type catalogue is the SDK's file, byte for byte: change it in the
// SDK, move the pin, copy it here.
func TestCatalogueMatchesTheSDK(t *testing.T) {
	py := python(t)
	out, err := exec.Command(py, "-c", "import importlib.resources as r; print(r.files('jarvis_command_sdk').joinpath('device_types.json'))").Output()
	if err != nil {
		t.Fatal(err)
	}
	sdk, err := os.ReadFile(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("the SDK has no device_types.json (pin older than 0.14.0?): %v", err)
	}
	if !bytes.Equal(sdk, devicetypes.JSON()) {
		t.Fatal("internal/devicetypes/device_types.json differs from the SDK's")
	}
}
