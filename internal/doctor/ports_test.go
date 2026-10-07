package doctor

import (
	"reflect"
	"testing"
)

func TestExposurePorts(t *testing.T) {
	e := Exposure{Listeners: []string{"auth", "cc", "off", "dup"}, MQTTAddr: ":1884", MQTTWSAddr: "bad", MDNS: true}
	got := e.Ports(map[string]int{"auth": 7701, "cc": 7703, "off": 0, "dup": 7701})
	want := []Port{{"auth", 7701, "tcp"}, {"cc", 7703, "tcp"}, {"mqtt", 1884, "tcp"}, {"mdns", 5353, "udp"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	if p := (Exposure{}).Ports(nil); len(p) != 0 {
		t.Fatalf("empty exposure: %v", p)
	}
}

func TestWorst(t *testing.T) {
	for _, c := range []struct {
		in   []Check
		want Status
	}{
		{nil, OK},
		{[]Check{{Status: OK}, {Status: Warn}}, Warn},
		{[]Check{{Status: Fail}, {Status: Warn}}, Fail},
	} {
		if got := Worst(c.in); got != c.want {
			t.Errorf("%v: %s, want %s", c.in, got, c.want)
		}
	}
}
