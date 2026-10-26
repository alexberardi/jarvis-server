//go:build linux

package atspi

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

const (
	rolePageTabList   = 38
	roleDocumentFrame = 82
	roleDocumentWeb   = 95

	ifSelection = "org.a11y.atspi.Selection"

	// maxTabVisits bounds the walk for tabs: a window's chrome is small, its documents are pruned.
	maxTabVisits = 4000
)

// tabWalk collects the page tabs of one window, depth-first. It doesn't descend into a tab (a
// GTK notebook's tab holds its page) or a document (a web page can be thousands of nodes).
type tabWalk struct {
	ctx    context.Context
	visits int
	tabs   []obj
}

func (w *tabWalk) visit(o obj, depth int) {
	if depth > maxDepth || w.visits >= maxTabVisits {
		return
	}
	w.visits++
	r, err := o.role(w.ctx)
	if err != nil {
		return
	}
	switch r {
	case rolePageTab:
		w.tabs = append(w.tabs, o)
		return
	case roleDocumentWeb, roleDocumentFrame:
		return
	}
	kids, err := o.children(w.ctx)
	if err != nil {
		return
	}
	for _, k := range kids {
		w.visit(k, depth+1)
	}
}

// appTabs are the page tabs of every window of the app with pid, the active window's first:
// a tab the user names may be in another window of the same browser.
func appTabs(ctx context.Context, pid int) ([]obj, []string, error) {
	c, err := bus(ctx)
	if err != nil {
		return nil, nil, err
	}
	app, err := findApp(ctx, c, pid)
	if err != nil {
		return nil, nil, err
	}
	wins, err := app.children(ctx)
	if err != nil {
		return nil, nil, callErr("GetChildren", err)
	}
	slices.SortStableFunc(wins, func(a, b obj) int {
		aa, ba := a.state(ctx).has(stateActive), b.state(ctx).has(stateActive)
		switch {
		case aa && !ba:
			return -1
		case ba && !aa:
			return 1
		}
		return 0
	})
	var tabs []obj
	var windows []string
	for _, win := range wins {
		w := &tabWalk{ctx: ctx}
		w.visit(win, 0)
		title := win.name(ctx)
		for _, t := range w.tabs {
			tabs = append(tabs, t)
			windows = append(windows, title)
		}
	}
	return tabs, windows, nil
}

func tabs(ctx context.Context, pid int) ([]Tab, error) {
	objs, windows, err := appTabs(ctx, pid)
	if err != nil {
		return nil, err
	}
	out := make([]Tab, 0, len(objs))
	for i, o := range objs {
		out = append(out, Tab{Index: i, Name: o.name(ctx), Selected: o.state(ctx).has(stateSelected), Window: windows[i]})
	}
	return out, nil
}

// selectTab presses the tab's own action (Firefox: "switch"), else selects it in its tab list
// (a GTK notebook). The tab must still be at index with the same name: the strip may have
// changed since it was listed.
func selectTab(ctx context.Context, pid, index int, name string) error {
	objs, _, err := appTabs(ctx, pid)
	if err != nil {
		return err
	}
	if index < 0 || index >= len(objs) || objs[index].name(ctx) != name {
		return &Error{Code: CodeStaleRef, Msg: "the tabs changed since they were listed (" + strconv.Quote(name) + ")"}
	}
	o := objs[index]
	var acts []struct{ Name, Desc, Key string }
	if o.call(ctx, ifAction+".GetActions").Store(&acts) == nil {
		for _, want := range []string{"switch", "select", "activate", "click", "press"} {
			for i, a := range acts {
				if strings.EqualFold(a.Name, want) {
					return o.do(ifAction+".DoAction", int32(i))
				}
			}
		}
	}
	parent, err := o.parent(ctx)
	if err != nil {
		return err
	}
	kids, err := parent.children(ctx)
	if err != nil {
		return callErr("GetChildren", err)
	}
	for i, k := range kids {
		if k.ref == o.ref {
			return parent.do(ifSelection+".SelectChild", int32(i))
		}
	}
	return &Error{Code: CodeUnsupported, Msg: "the tab " + strconv.Quote(name) + " has no switch action"}
}

func (o obj) parent(ctx context.Context) (obj, error) {
	v, err := o.prop(ctx, "Parent")
	if err != nil {
		return obj{}, callErr("Parent", err)
	}
	var r objRef
	switch p := v.(type) {
	case []any: // a (so) in a variant
		if len(p) == 2 {
			r.Name, _ = p[0].(string)
			r.Path, _ = p[1].(dbus.ObjectPath)
		}
	}
	if r.null() {
		return obj{}, &Error{Code: CodeNotFound, Msg: "the tab has no parent"}
	}
	return obj{o.c, r}, nil
}
