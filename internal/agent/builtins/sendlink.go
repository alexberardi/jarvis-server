// Package builtins are the node's built-in commands written in Go, served on a Pi and a
// computer alike: ports of jarvis-node-setup's commands/*.py, whose names, parameters and
// outputs they keep so command-center can't tell them apart (docs/decisions/
// 2026-10-27-go-builtins.md). They join the toolset after the package sidecar, so a Pantry
// package's command of the same name wins.
package builtins

import (
	"context"
	"net/http"
	"strings"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
)

// CC is command-center as the tools reach it: nil until the node is connected.
type CC func() *node.CC

var sendLinkSpec = node.Spec{
	Name: "send_link",
	Description: "Send a link (URL) to the speaker's phone as a push notification they can tap to open in " +
		"their browser. Use as a follow-up when the user asks to be sent / texted / shared an article, " +
		"recipe, search result, or any other link returned by a previous tool. Requires speaker " +
		"recognition — fails if Jarvis can't identify who's asking.",
	Params: []node.Param{
		{Name: "url", Type: "string", Required: true,
			Description: "Full http/https URL to send. Pull this from a previous tool result in the conversation history — do NOT invent or guess URLs."},
		{Name: "title", Type: "string",
			Description: "Short title shown in the push notification (e.g. article headline). Defaults to 'Link from Jarvis'."},
	},
	Keywords: []string{"send me", "send that", "send the link", "send it", "text me", "push me", "share that",
		"send to my phone", "to my phone", "open on my phone"},
	Examples: []node.Example{
		{VoiceCommand: "Send me that article", Primary: true,
			Params: map[string]any{"url": "https://www.barstoolsports.com/blog/123/example", "title": "Example Barstool article"}},
		{VoiceCommand: "Send the second one to my phone", Params: map[string]any{"url": "https://example.com/article", "title": "Example article"}},
		{VoiceCommand: "Text me that link", Params: map[string]any{"url": "https://example.com/link"}},
		{VoiceCommand: "Open that on my phone", Params: map[string]any{"url": "https://example.com/recipe"}},
	},
}

// SendLink is send_link: the URL as a tap-to-open push to the recognised speaker's phone,
// through jarvisd's /node/send-link (which applies the household's attention rules).
type SendLink struct{ CC CC }

func (t *SendLink) Available(context.Context) []node.ToolDef {
	return []node.ToolDef{sendLinkSpec.Render()}
}

// failed is a failure whose message is spoken.
func failed(details, message string) map[string]any {
	return map[string]any{"success": false, "error": details, "message": message}
}

func (t *SendLink) Call(ctx context.Context, name string, args map[string]any, req node.Request) (map[string]any, bool) {
	if name != sendLinkSpec.Name {
		return nil, false
	}
	url, _ := args["url"].(string)
	url = strings.TrimSpace(url)
	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if req.UserID == nil {
		// No target user: the push would be dropped or go to the whole household.
		return failed("I couldn't tell who's asking, so I can't send the link to your phone. Try again after Jarvis has recognized you.",
			"I couldn't tell who's asking, so I can't send the link to your phone."), true
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return failed("Refusing to send a non-http(s) URL.", "I don't have a valid link to send."), true
	}
	body := "Tap to open"
	if title == "" {
		title = "Link from Jarvis"
	} else {
		body = title
	}
	cc := t.CC()
	if cc == nil {
		return failed("not connected to command-center", "I can't find the server to send your link."), true
	}
	var resp struct {
		Sent bool `json:"sent"`
	}
	if err := cc.Do(ctx, http.MethodPost, "/api/v0/node/send-link",
		map[string]any{"user_id": *req.UserID, "url": url, "title": title, "body": body}, &resp); err != nil {
		return failed(err.Error(), "I couldn't reach the server right now."), true
	}
	if !resp.Sent {
		return failed("command-center returned sent=false", "The server got the link but didn't deliver it to your phone."), true
	}
	return map[string]any{"success": true, "message": "Sent to your phone."}, true
}
