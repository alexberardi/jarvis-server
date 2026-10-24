package browser

import (
	"context"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

func TestClickRisk(t *testing.T) {
	for _, c := range []struct {
		label, url string
		risky      bool
	}{
		{"Google Search", "https://www.google.com/", false},
		{"Next", "https://www.google.com/search?q=dogs", false},
		{"Images", "https://www.google.com/search?q=dogs", false},
		{"Under $50", "https://shop.example.com/s?k=lamp", false},
		{"Posts", "https://forum.example.com/", false},
		{"Sign in", "https://example.com/", false},
		{"", "", false},
		{"Place order", "https://shop.example.com/", true},
		{"Buy now", "https://shop.example.com/item/1", true},
		{"Add to cart", "https://shop.example.com/item/1", true},
		{"Send", "https://example.com/", true},
		{"Post", "https://x.com/home", true},
		{"Reply all", "https://example.com/", true},
		{"Delete", "https://example.com/", true},
		{"Move to trash", "https://example.com/", true},
		{"Share", "https://docs.example.com/d/1", true},
		{"Make public", "https://docs.example.com/d/1", true},
		{"Change password", "https://example.com/", true},
		{"Sign out of all devices", "https://example.com/", true},
		{"Cancel subscription", "https://example.com/", true},
		{"Submit", "https://forms.example.com/f/1", true},
		{"Continue", "https://shop.example.com/checkout/step2", true},
		{"Archive", "https://mail.google.com/mail/u/0/", true},
		{"Pay", "https://www.paypal.com/", true},
	} {
		if got := clickRisk(c.label, c.url) != ""; got != c.risky {
			t.Errorf("click %q on %s: risky %v, want %v", c.label, c.url, got, c.risky)
		}
	}
}

func TestSubmitRisk(t *testing.T) {
	for _, c := range []struct {
		field, url string
		risky      bool
	}{
		{"search", "https://www.google.com/", false},
		{"", "https://www.google.com/", false},
		{"Search Amazon", "https://www.amazon.com/", false},
		{"q", "https://duckduckgo.com/", false},
		{"search mail", "https://mail.google.com/mail/u/0/", false},
		{"city", "https://weather.example.com/", false},
		{"password", "https://example.com/login", true},
		{"message", "https://example.com/", true},
		{"comment", "https://blog.example.com/p/1", true},
		{"email", "https://example.com/newsletter", true},
		{"card number", "https://shop.example.com/", true},
		{"", "https://web.whatsapp.com/", true},
		{"", "https://shop.example.com/cart", true},
	} {
		if got := submitRisk(c.field, c.url) != ""; got != c.risky {
			t.Errorf("submit %q on %s: risky %v, want %v", c.field, c.url, got, c.risky)
		}
	}
}

// pageExt answers list_tabs with one current tab at url and every fill and click with success.
func pageExt(url string) func(Message) Message {
	return func(m Message) Message {
		switch m.Cmd {
		case "list_tabs":
			return ok(map[string]any{"tabs": []any{
				map[string]any{"tab_id": 7.0, "url": url, "title": "Page", "current": true},
			}, "count": 1})
		case "fill":
			return ok(map[string]any{"chars": 4, "submitted": m.Args["submit"]})
		case "click":
			return ok(map[string]any{"clicked": m.Args["text"]})
		}
		return ok(map[string]any{})
	}
}

// #578: in the default mode a search runs without a question; paying, sending, deleting and
// sharing still wait for the user's yes, and "all" still asks for everything.
func TestSubmitModeAsksOnlyBeforeRiskyActions(t *testing.T) {
	for _, c := range []struct {
		name, mode, url, tool string
		args                  map[string]any
		asks                  bool
	}{
		{"search and submit", dictate.ConfirmSubmit, "https://www.google.com/", ToolFill, map[string]any{"field": "search", "text": "dogs", "submit": true}, false},
		{"focused field and submit", dictate.ConfirmSubmit, "https://www.google.com/", ToolFill, map[string]any{"text": "dogs", "submit": true}, false},
		{"Google Search button", dictate.ConfirmSubmit, "https://www.google.com/", ToolClick, map[string]any{"text": "Google Search"}, false},
		{"a result link", dictate.ConfirmSubmit, "https://www.google.com/search?q=dogs", ToolClick, map[string]any{"text": "Dog - Wikipedia"}, false},
		{"place order", dictate.ConfirmSubmit, "https://shop.example.com/", ToolClick, map[string]any{"text": "Place order"}, true},
		{"send", dictate.ConfirmSubmit, "https://example.com/", ToolClick, map[string]any{"text": "Send"}, true},
		{"delete", dictate.ConfirmSubmit, "https://example.com/", ToolClick, map[string]any{"text": "Delete"}, true},
		{"next on a checkout page", dictate.ConfirmSubmit, "https://shop.example.com/checkout", ToolClick, map[string]any{"text": "Next"}, true},
		{"message and submit", dictate.ConfirmSubmit, "https://example.com/", ToolFill, map[string]any{"field": "message", "text": "hi", "submit": true}, true},
		{"anything in mail", dictate.ConfirmSubmit, "https://mail.google.com/mail/u/0/", ToolFill, map[string]any{"text": "hi", "submit": true}, true},
		{"all: a search", dictate.ConfirmAll, "https://www.google.com/", ToolFill, map[string]any{"field": "search", "text": "dogs", "submit": true}, true},
		{"all: a plain fill", dictate.ConfirmAll, "https://www.google.com/", ToolFill, map[string]any{"field": "search", "text": "dogs"}, true},
		{"off: place order", dictate.ConfirmOff, "https://shop.example.com/", ToolClick, map[string]any{"text": "Place order"}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, path := startHub(t)
			f := connectExt(t, h, path, "Firefox", true, pageExt(c.url))
			ts := NewTools(h)
			ts.Confirm = &dictate.Confirmer{Mode: c.mode}
			out, _ := ts.Call(context.Background(), c.tool, c.args, node.Request{})
			acted := 0
			for _, m := range f.received() {
				if m.Cmd == "fill" || m.Cmd == "click" {
					acted++
				}
			}
			if c.asks {
				if out["status"] != "awaiting_confirmation" || acted != 0 {
					t.Fatalf("not asked: %v (acted %d)", out, acted)
				}
				return
			}
			if out["success"] != true || acted != 1 {
				t.Fatalf("asked or failed: %v (acted %d)", out, acted)
			}
		})
	}
}
