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
		{"Next", "", true}, // a page that can't be told asks
		{"Place order", "https://shop.example.com/", true},
		{"Place your order", "https://www.amazon.com/gp/buy/spc/handlers/display.html", true},
		{"Place your order", "https://shop.example.com/", true},
		{"Place my order", "https://shop.example.com/", true},
		{"Confirm order", "https://shop.example.com/", true},
		{"Confirm purchase", "https://shop.example.com/", true},
		{"Complete purchase", "https://shop.example.com/", true},
		{"Pay now", "https://shop.example.com/", true},
		{"Continue", "https://shop.example.com/buy/1", true},
		{"Continue", "https://shop.example.com/purchase", true},
		{"Continue", "https://shop.example.com/order/review", true},
		{"Continue", "https://example.com/subscription", true},
		{"Authorize", "https://github.com/login/oauth/authorize?client_id=abc", true},
		{"Authorize app", "https://example.com/", true},
		{"Allow", "https://accounts.google.com/signin/oauth/consent?authuser=0", true},
		{"Grant access", "https://example.com/", true},
		{"Revoke", "https://example.com/", true},
		{"Disable", "https://example.com/", true},
		{"Turn off 2-Step Verification", "https://example.com/", true},
		{"Turn off two step", "https://example.com/", true},
		{"Turn off", "https://myaccount.google.com/signinoptions/two-step-verification", true},
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
		{"", "https://x.com/messages/123-456", true},
		{"", "https://www.facebook.com/messages/t/100001", true},
		{"", "https://www.linkedin.com/messaging/thread/2-abc/", true},
		{"", "https://www.instagram.com/direct/t/1234/", true},
		{"", "https://www.reddit.com/r/golang/comments/abc123/title/", true},
		{"", "https://example.com/inbox", true},
		{"", "https://example.com/chat/42", true},
		{"", "https://example.com/dm/42", true},
		{"", "", true},        // a page that can't be told asks
		{"search", "", false}, // unless the field is a search box
	} {
		if got := submitRisk(c.field, c.url) != ""; got != c.risky {
			t.Errorf("submit %q on %s: risky %v, want %v", c.field, c.url, got, c.risky)
		}
	}
}

// pageExt answers list_tabs with one current tab at url (an error when url is "") and every
// fill and click with success.
func pageExt(url string) func(Message) Message {
	return func(m Message) Message {
		switch m.Cmd {
		case "list_tabs":
			if url == "" {
				return Message{Error: "list_tabs failed", Code: "internal"}
			}
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
		{"page can't be told: focused field and submit", dictate.ConfirmSubmit, "", ToolFill, map[string]any{"text": "hi", "submit": true}, true},
		{"page can't be told: a click", dictate.ConfirmSubmit, "", ToolClick, map[string]any{"text": "Next"}, true},
		{"page can't be told: search and submit", dictate.ConfirmSubmit, "", ToolFill, map[string]any{"field": "search", "text": "dogs", "submit": true}, false},
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
