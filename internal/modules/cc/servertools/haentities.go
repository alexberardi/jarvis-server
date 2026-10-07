package servertools

import (
	"context"
	"errors"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/parse"
	"github.com/alexberardi/jarvis-server/internal/modules/llm/pyjson"
)

// HAEntities is the get_ha_entities tool: device lookup by domain, area or floor over the
// node's agent payload from /conversation/start (legacy get_ha_entities_tool.py), so the
// prompt need not list every device.
type HAEntities struct{}

// NewHAEntities builds the tool.
func NewHAEntities() *HAEntities { return &HAEntities{} }

func (t *HAEntities) Name() string               { return "get_ha_entities" }
func (t *HAEntities) Definition() *pyjson.Object { return LegacyDefinition("get_ha_entities") }

// deviceAgentData is context_builders.device_agent_data: the HA integration's payload when
// present (even empty), else the built-in device agent's.
func deviceAgentData(agents *pyjson.Object) *pyjson.Object {
	if agents == nil {
		return nil
	}
	if v, ok := agents.Get("home_assistant"); ok {
		o, _ := v.(*pyjson.Object)
		return o
	}
	v, _ := agents.Get("device_agent")
	o, _ := v.(*pyjson.Object)
	return o
}

// pyTruthy is Python truthiness for decoded JSON values.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case []any:
		return len(x) > 0
	case *pyjson.Object:
		return x.Len() > 0
	case float64:
		return x != 0
	}
	return pyjson.Repr(v) != "0"
}

// fstr is f"{v}".
func fstr(v any) string {
	if v == nil {
		return "None"
	}
	return pyjson.Str(v)
}

// getDefault is d.get(k, def).
func getDefault(o *pyjson.Object, k string, def any) any {
	if o == nil {
		return def
	}
	if v, ok := o.Get(k); ok {
		return v
	}
	return def
}

type haDevice struct{ entityID, name, state, area any }

// line is _format_device_line.
func (d haDevice) line() string {
	if pyTruthy(d.area) {
		return "- " + fstr(d.area) + " — " + fstr(d.name) + ": " + fstr(d.entityID) + " (currently " + fstr(d.state) + ")"
	}
	return "- " + fstr(d.name) + ": " + fstr(d.entityID) + " (currently " + fstr(d.state) + ")"
}

func deviceFrom(o *pyjson.Object, name any) haDevice {
	if name == nil {
		name = getDefault(o, "name", "")
	}
	return haDevice{
		entityID: getDefault(o, "entity_id", ""), name: name,
		state: getDefault(o, "state", "unknown"), area: getDefault(o, "area", ""),
	}
}

func (t *HAEntities) Execute(_ context.Context, call Call, turn Turn) (any, error) {
	domainV, has := call.Arg("domain")
	if !has {
		return nil, errors.New("GetHAEntitiesTool.execute() missing 1 required positional argument: 'domain'")
	}
	domain := fstr(domainV)
	area, floor := call.Str("area"), call.Str("floor")

	if turn.ConversationID == "" {
		return Obj("error", "missing_conversation_id", "message", "Conversation ID is required but was not provided"), nil
	}
	ha := deviceAgentData(turn.Agents)
	if ha == nil || ha.Len() == 0 {
		return Obj("error", "no_ha_data", "message", "This node has no Home Assistant data"), nil
	}

	var allowed map[string]bool // nil = no filter
	floors, _ := getDefault(ha, "floors", nil).(*pyjson.Object)
	if floor != "" {
		fl := parse.PyLower(floor)
		if floors != nil {
			for _, name := range floors.Keys() {
				if parse.PyLower(name) != fl {
					continue
				}
				allowed = map[string]bool{}
				v, _ := floors.Get(name)
				list, _ := v.([]any)
				for _, a := range list {
					allowed[parse.PyLower(fstr(a))] = true
				}
				break
			}
		}
		if allowed == nil {
			var names []string
			if floors != nil {
				names = floors.Keys()
			}
			return Obj("error", "floor_not_found",
				"message", "Floor '"+floor+"' not found. Available floors: "+strings.Join(names, ", ")), nil
		}
	}
	if area != "" {
		al := parse.PyLower(area)
		if allowed == nil {
			allowed = map[string]bool{al: true}
		} else {
			allowed = map[string]bool{al: allowed[al]}
		}
	}

	lightControls, _ := getDefault(ha, "light_controls", nil).(*pyjson.Object)
	deviceControls, _ := getDefault(ha, "device_controls", nil).(*pyjson.Object)
	domainList := func(d string) []any {
		v, _ := getDefault(deviceControls, d, nil).([]any)
		return v
	}
	var devices []haDevice
	addControls := func(list []any, skip map[string]bool) {
		for _, e := range list {
			dev, _ := e.(*pyjson.Object)
			if dev == nil {
				continue
			}
			id := getDefault(dev, "entity_id", nil)
			if skip != nil && skip[fstr(id)] {
				continue
			}
			if s, _ := getDefault(dev, "state", nil).(string); s == "unavailable" {
				continue
			}
			devices = append(devices, deviceFrom(dev, nil))
		}
	}
	if domain == "light" {
		groupIDs := map[string]bool{}
		if lightControls != nil {
			for _, name := range lightControls.Keys() {
				v, _ := lightControls.Get(name)
				info, _ := v.(*pyjson.Object)
				devices = append(devices, deviceFrom(info, name))
				groupIDs[fstr(getDefault(info, "entity_id", nil))] = true
			}
		}
		addControls(domainList("light"), groupIDs)
	} else {
		addControls(domainList(domain), nil)
	}

	if allowed != nil {
		kept := devices[:0]
		for _, d := range devices {
			a, _ := d.area.(string)
			if allowed[parse.PyLower(a)] {
				kept = append(kept, d)
			}
		}
		devices = kept
	}
	lines := make([]any, len(devices))
	for i, d := range devices {
		lines[i] = d.line()
	}
	return Obj("domain", domainV, "count", len(devices), "devices", lines), nil
}
