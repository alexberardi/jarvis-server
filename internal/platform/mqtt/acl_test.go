package mqtt

import "testing"

func TestCanSubscribe(t *testing.T) {
	cases := []struct {
		filter string
		want   bool
	}{
		{"jarvis/nodes/n1/#", true},
		{"jarvis/nodes/n1/commands", true},
		{"jarvis/nodes/n1/command-data/+", true},
		{"jarvis/nodes/n1", true},
		{"jarvis/auth/+/ready", true},
		{"jarvis/auth/google/ready", true},
		{"jarvis/nodes/n2/#", false},
		{"jarvis/nodes/n1x/#", false},
		{"jarvis/nodes/+/commands", false},
		{"jarvis/nodes/#", false},
		{"jarvis/#", false},
		{"#", false},
		{"jarvis/auth/#", false},
		{"jarvis/auth/+/ready/x", false},
		{"jarvis/auth/+/other", false},
		{"$share/g/jarvis/nodes/n1/#", false},
		{"$SYS/#", false},
	}
	for _, c := range cases {
		if got := CanSubscribe("n1", c.filter); got != c.want {
			t.Errorf("CanSubscribe(n1, %q) = %v, want %v", c.filter, got, c.want)
		}
	}
}

func TestCanPublish(t *testing.T) {
	cases := []struct {
		topic string
		want  bool
	}{
		{"jarvis/nodes/n1/command-data/commands/response/c1", true},
		{"jarvis/nodes/n1/command-data/schema/response/c1", true},
		{"jarvis/nodes/n1/command-data/list/response/c1", true},
		{"jarvis/nodes/n1/command-data/get/response/c1", true},
		{"jarvis/nodes/n1/command-data/create/response/c1", true},
		{"jarvis/nodes/n1/command-data/update/response/c1", true},
		{"jarvis/nodes/n1/command-data/delete/response/c1", true},
		{"jarvis/nodes/n1/context/query/response/c1", true},
		{"jarvis/nodes/n1/command-data/bogus/response/c1", false},
		{"jarvis/nodes/n1/context/operations/response/c1", false},
		{"jarvis/nodes/n1/command-data/get/response/", false},
		{"jarvis/nodes/n1/command-data/get/response/c1/x", false},
		{"jarvis/nodes/n1/command-data/get", false},
		{"jarvis/nodes/n1/commands", false},
		{"jarvis/nodes/n1/settings/request", false},
		{"jarvis/nodes/n2/command-data/get/response/c1", false},
		{"jarvis/nodes/n2/commands", false},
		{"jarvis/auth/google/ready", false},
	}
	for _, c := range cases {
		if got := CanPublish("n1", c.topic); got != c.want {
			t.Errorf("CanPublish(n1, %q) = %v, want %v", c.topic, got, c.want)
		}
	}
}

func TestValidNodeID(t *testing.T) {
	for id, want := range map[string]bool{"n1": true, "": false, "a/b": false, "+": false, "#": false, "$x": false} {
		if got := validNodeID(id); got != want {
			t.Errorf("validNodeID(%q) = %v, want %v", id, got, want)
		}
	}
}
