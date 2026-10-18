package doctor

import (
	"context"
	"net"
	"strings"
	"testing"
)

func TestMQTTAnonymousCheck(t *testing.T) {
	o := opts(nil, []*net.IPNet{})
	if c := find(t, Run(context.Background(), o), "mqtt anonymous"); c.Status != OK {
		t.Errorf("off: %+v", c)
	}
	o.MQTTAnonymous = true
	c := find(t, Run(context.Background(), o), "mqtt anonymous")
	if c.Status != Warn || !strings.Contains(c.Detail, "unset JARVIS_MQTT_ALLOW_ANONYMOUS") {
		t.Errorf("on: %+v", c)
	}
}
