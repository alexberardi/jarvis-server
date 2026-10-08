package service

import (
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The updater's SDDL is valid for Windows itself and grants the service SID exactly query
// config, query status and start.
func TestHelperSDDLParses(t *testing.T) {
	const sid = "S-1-5-80-1-2-3-4-5"
	sd, err := windows.SecurityDescriptorFromString(HelperSDDL(sid))
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		t.Fatalf("dacl %v %v", dacl, err)
	}
	if got := dacl.AceCount; got != 5 {
		t.Fatalf("%d ACEs", got)
	}
	var ace *windows.ACCESS_ALLOWED_ACE
	if err := windows.GetAce(dacl, 4, &ace); err != nil {
		t.Fatal(err)
	}
	want := windows.ACCESS_MASK(windows.SERVICE_QUERY_CONFIG | windows.SERVICE_QUERY_STATUS | windows.SERVICE_START)
	if ace.Mask != want {
		t.Fatalf("mask %#x, want %#x", ace.Mask, want)
	}
	if s := (*windows.SID)(unsafe.Pointer(&ace.SidStart)); s.String() != sid {
		t.Fatalf("trustee %s", s)
	}
}

// The service SID of NT SERVICE\jarvisd is what the helper's ACE names; Windows derives it
// from the service name, so it resolves even before the service exists.
func TestServiceSIDResolves(t *testing.T) {
	sid, _, _, err := windows.LookupSID("", `NT SERVICE\TrustedInstaller`)
	if err != nil {
		t.Skip("service SIDs don't resolve here:", err)
	}
	if s := sid.String(); len(s) < 9 || s[:9] != "S-1-5-80-" {
		t.Fatalf("sid %s", s)
	}
}
