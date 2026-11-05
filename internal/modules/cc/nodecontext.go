package cc

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
)

// Plan-time context from the household's nodes (docs/decisions/2026-11-05-call-plan-node-context.md):
// a call plan asks every active node, over the node's context handler
// (jarvis/nodes/{id}/context/{operations,query}, internal/agent/nodesettings/context.go), for
// the calendar's availability and every command's call_context. Each node gets a few seconds;
// one that is offline or slow is skipped, so a plan never waits on a node. Never asked mid-call.

const (
	opAvailability = "availability"
	opCallContext  = "call_context"
)

// nodeContextTimeout bounds the whole gathering: a plan is waiting on it.
var nodeContextTimeout = 5 * time.Second

// nodeContext is phone.Availability and phone.NodeContext over the household's nodes.
type nodeContext struct{ m *Module }

var (
	_ phone.Availability = nodeContext{}
	_ phone.NodeContext  = nodeContext{}
)

// contextAnswer is a node's ContextResult as the handler sends it.
type contextAnswer struct {
	OK          bool            `json:"ok"`
	Error       string          `json:"error"`
	Data        json.RawMessage `json:"data"`
	CommandName string          `json:"command_name"`
	Providers   []struct {
		CommandName string `json:"command_name"`
		Operations  []struct {
			Name string `json:"name"`
		} `json:"operations"`
	} `json:"providers"`
}

// liveNodes are the household's active nodes with an MQTT session, in last-seen order.
func (n nodeContext) liveNodes(ctx context.Context, hh string) []string {
	if n.m.bus == nil || !n.m.bus.Available() || n.m.deps.DB == nil {
		return nil
	}
	rows, err := n.m.deps.DB.Read.QueryContext(ctx, `SELECT node_id FROM cc_nodes
		WHERE household_id = ? AND is_active = 1 ORDER BY last_seen DESC`, hh)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			continue
		}
		if up, known := n.m.bus.NodeSession(id); up || !known {
			out = append(out, id)
		}
	}
	return out
}

func (n nodeContext) ask(ctx context.Context, node string, op string, payload map[string]any) (contextAnswer, bool) {
	raw, err := n.m.bus.Request(ctx, node, "context/"+op, payload)
	if err != nil {
		return contextAnswer{}, false
	}
	var a contextAnswer
	if json.Unmarshal(raw, &a) != nil {
		return contextAnswer{}, false
	}
	return a, true
}

// providers lists the node's commands that offer operation.
func (n nodeContext) providers(ctx context.Context, node, operation string) []string {
	a, ok := n.ask(ctx, node, "operations", map[string]any{})
	if !ok || !a.OK {
		return nil
	}
	var out []string
	for _, p := range a.Providers {
		for _, o := range p.Operations {
			if o.Name == operation {
				out = append(out, p.CommandName)
			}
		}
	}
	return out
}

func userParam(uid *int64) any {
	if uid == nil {
		return nil
	}
	return *uid
}

// Availability is the first good calendar answer from the household's nodes, nodes seen most
// recently first.
func (n nodeContext) Availability(ctx context.Context, hh string, uid *int64, start, end time.Time) (phone.AvailabilityAnswer, error) {
	nodes := n.liveNodes(ctx, hh)
	if len(nodes) == 0 {
		return phone.AvailabilityAnswer{}, errNoNodeContext
	}
	ctx, cancel := context.WithTimeout(ctx, nodeContextTimeout)
	defer cancel()
	answers := make([]*phone.AvailabilityAnswer, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, ok := n.ask(ctx, node, "query", map[string]any{"operation": opAvailability,
				"params":  map[string]any{"start": start.Format("2006-01-02"), "end": end.Format("2006-01-02")},
				"user_id": userParam(uid)})
			if !ok || !a.OK {
				return
			}
			var d struct {
				Free []string `json:"free"`
				Busy []string `json:"busy"`
			}
			if json.Unmarshal(a.Data, &d) != nil {
				return
			}
			answers[i] = &phone.AvailabilityAnswer{OK: true, Free: d.Free, Busy: d.Busy, Source: a.CommandName}
		}()
	}
	wg.Wait()
	for _, a := range answers {
		if a != nil {
			return *a, nil
		}
	}
	return phone.AvailabilityAnswer{}, errNoNodeContext
}

// CallContext is every command's call_context fields for this business and goal, from every
// live node; the first answer for a key wins (nodes seen most recently first, then commands
// in the node's order).
func (n nodeContext) CallContext(ctx context.Context, hh string, uid *int64, business, goal string) []phone.ContextField {
	nodes := n.liveNodes(ctx, hh)
	if len(nodes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, nodeContextTimeout)
	defer cancel()
	perNode := make([][]phone.ContextField, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmds := n.providers(ctx, node, opCallContext)
			perCmd := make([][]phone.ContextField, len(cmds))
			var cw sync.WaitGroup
			for j, cmd := range cmds {
				cw.Add(1)
				go func() {
					defer cw.Done()
					a, ok := n.ask(ctx, node, "query", map[string]any{"command": cmd, "operation": opCallContext,
						"params": map[string]any{"business": business, "goal": goal}, "user_id": userParam(uid)})
					if !ok || !a.OK {
						return
					}
					var data any
					if json.Unmarshal(a.Data, &data) != nil {
						return
					}
					fields := phone.ParseCallContext(data)
					for k := range fields {
						fields[k].Source = cmd
					}
					perCmd[j] = fields
				}()
			}
			cw.Wait()
			for _, fs := range perCmd {
				perNode[i] = append(perNode[i], fs...)
			}
		}()
	}
	wg.Wait()
	var out []phone.ContextField
	seen := map[string]bool{}
	for _, fs := range perNode {
		for _, f := range fs {
			if !seen[f.Key] {
				seen[f.Key] = true
				out = append(out, f)
			}
		}
	}
	return out
}

type nodeContextError string

func (e nodeContextError) Error() string { return string(e) }

const errNoNodeContext = nodeContextError("no node answered")
