package service

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// recordVersion against a scratch key under HKCU (HKLM needs an administrator).
func TestRecordVersion(t *testing.T) {
	const path = `Software\jarvisd-test\Uninstall\` + LaunchdLabel + `_is1`
	t.Cleanup(func() {
		_ = registry.DeleteKey(registry.CURRENT_USER, path)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\jarvisd-test\Uninstall`)
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\jarvisd-test`)
	})
	if ok, err := recordVersion(registry.CURRENT_USER, path, "v0.3.0"); ok || err != nil {
		t.Fatalf("no entry: %v %v", ok, err)
	}
	if _, err := registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE); err == nil {
		t.Fatal("created the entry")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, path, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	_ = k.SetStringValue("DisplayName", "Jarvis Automation Server")
	_ = k.SetStringValue("DisplayVersion", "0.2.0")
	k.Close()
	if ok, err := recordVersion(registry.CURRENT_USER, path, "v0.3.0-rc1"); !ok || err != nil {
		t.Fatalf("recordVersion = %v %v", ok, err)
	}
	k, _ = registry.OpenKey(registry.CURRENT_USER, path, registry.QUERY_VALUE)
	defer k.Close()
	if v, _, _ := k.GetStringValue("DisplayVersion"); v != "0.3.0-rc1" {
		t.Fatalf("DisplayVersion %q", v)
	}
}
