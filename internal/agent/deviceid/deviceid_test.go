package deviceid

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func fixed(mid, uid string) sources {
	return sources{
		machineID: func() (string, error) { return mid, nil },
		userID:    func() (string, error) { return uid, nil },
	}
}

var hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func TestStablePerMachineAndUser(t *testing.T) {
	const mid = "4c4c4544004a3510804bb2c04f4d3133"
	a := fixed(mid, "1000").get()
	if !hex64.MatchString(a) {
		t.Fatalf("id %q is not 64 hex chars", a)
	}
	if b := fixed(mid, "1000").get(); b != a {
		t.Fatalf("not stable: %q then %q", a, b)
	}
	// Whitespace and case in the OS's answer don't change it (ioreg is upper case).
	if b := fixed(" "+strings.ToUpper(mid)+"\n", "1000 ").get(); b != a {
		t.Fatalf("case/whitespace changed the id: %q vs %q", a, b)
	}
	if b := fixed(mid, "1001").get(); b == a {
		t.Fatal("another OS user on the same computer got the same id")
	}
	if b := fixed("0000000000000000000000000000000a", "1000").get(); b == a {
		t.Fatal("another computer got the same id")
	}
	if strings.Contains(a, mid) {
		t.Fatal("the raw machine id leaks into the device id")
	}
}

func TestEmptyWhenTheOSDoesNotSay(t *testing.T) {
	boom := func() (string, error) { return "", errors.New("boom") }
	for name, s := range map[string]sources{
		"no machine id":    {machineID: boom, userID: func() (string, error) { return "1000", nil }},
		"no user":          {machineID: func() (string, error) { return "abc", nil }, userID: boom},
		"blank machine id": fixed("  ", "1000"),
		"blank user":       fixed("abc", ""),
	} {
		if got := s.get(); got != "" {
			t.Errorf("%s: got %q, want empty", name, got)
		}
	}
}

func TestParseIOReg(t *testing.T) {
	out := `+-o J314sAP  <class IOPlatformExpertDevice, id 0x100000202, registered>
    {
      "IOPlatformSerialNumber" = "XXXXXXXXXX"
      "IOPlatformUUID" = "1F2E3D4C-5B6A-7980-A1B2-C3D4E5F60718"
    }`
	got, err := parseIOReg(out)
	if err != nil || got != "1F2E3D4C-5B6A-7980-A1B2-C3D4E5F60718" {
		t.Fatalf("parseIOReg = %q, %v", got, err)
	}
	if _, err := parseIOReg("nothing here"); err == nil {
		t.Fatal("no UUID should be an error")
	}
}

func TestThisComputer(t *testing.T) {
	// Whatever this OS answers, Get never errors: an id or "".
	if id := Get(); id != "" && !hex64.MatchString(id) {
		t.Fatalf("Get() = %q", id)
	}
}
