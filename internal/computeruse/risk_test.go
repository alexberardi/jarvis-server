package computeruse

import "testing"

func TestClassify(t *testing.T) {
	web := Target{Kind: "browser", Site: "shop.example"}
	desk := Target{Kind: "desktop", App: "com.apple.Notes"}
	press := Action{Kind: "press", Intent: "x", Ref: "e1"}
	click := Action{Kind: "click", Intent: "x", X: ip(5), Y: ip(5)}
	typ := func(s string) Action { return Action{Kind: "type", Intent: "x", Text: s} }
	set := func(s string) Action { return Action{Kind: "set_value", Intent: "x", Ref: "e2", Text: s} }
	btn := func(name string) *Element { return &Element{Role: "button", Name: name} }
	link := func(name string) *Element { return &Element{Role: "link", Name: name} }

	cases := []struct {
		name string
		a    Action
		el   *Element
		t    Target
		want string // category, "" = none
		lvl  string
	}{
		// pay
		{"place order", press, btn("Place your order"), web, CatPay, LevelConfirm},
		{"buy now", press, btn("Buy Now"), web, CatPay, LevelConfirm},
		{"pay amount", press, btn("Pay $12.99"), web, CatPay, LevelConfirm},
		{"confirm purchase", click, btn("Confirm purchase"), web, CatPay, LevelConfirm},
		{"complete order", press, btn("COMPLETE ORDER"), web, CatPay, LevelConfirm},
		{"submit payment", press, btn("Submit payment"), web, CatPay, LevelConfirm},
		{"card number into cc field", set("4111 1111 1111 1111"), &Element{Role: "textbox", Name: "Card number", Autocomplete: "cc-number"}, web, CatPay, LevelConfirm},
		{"expiry into cc field", set("12/29"), &Element{Role: "textbox", Autocomplete: "billing cc-exp"}, web, CatPay, LevelConfirm},
		// a form's newsletter option sends or subscribes nothing until the form is sent, and
		// sending it asks; the other categories still ask on a checkbox, radio or switch
		{"newsletter checkbox", press, &Element{Role: "checkbox", Name: "Send me the monthly newsletter"}, web, "", LevelNone},
		{"subscribe checkbox", click, &Element{Role: "checkbox", Name: "Subscribe to offers"}, web, "", LevelNone},
		{"unsubscribe radio", press, &Element{Role: "radio", Name: "Unsubscribe from all"}, web, "", LevelNone},
		{"delete radio", press, &Element{Role: "radio", Name: "Delete old backups"}, web, CatDelete, LevelConfirm},
		{"share switch", press, &Element{Role: "switch", Name: "Share my location"}, web, CatSharing, LevelConfirm},
		{"subscribe switch", press, &Element{Role: "switch", Name: "Subscribe"}, web, CatSubscribe, LevelConfirm},
		{"send button still asks", press, btn("Send message"), web, CatSend, LevelConfirm},
		// pay near misses
		{"payment methods link", press, link("Payment methods"), web, "", LevelNone},
		{"add to cart", press, btn("Add to cart"), web, "", LevelNone},
		{"proceed to checkout", press, btn("Proceed to checkout"), web, "", LevelNone},
		{"paypal logo", press, link("PayPal"), web, "", LevelNone},
		{"name into name field", set("Alex"), &Element{Role: "textbox", Name: "Name on card", Autocomplete: "name"}, web, "", LevelNone},
		// send
		{"send", press, btn("Send"), web, CatSend, LevelConfirm},
		{"post comment", press, btn("Post comment"), web, CatSend, LevelConfirm},
		{"publish", click, btn("Publish"), desk, CatSend, LevelConfirm},
		{"reply all", press, btn("Reply all"), web, CatSend, LevelConfirm},
		{"posts tab", press, link("Posts"), web, "", LevelNone},
		// delete
		{"delete", press, btn("Delete"), web, CatDelete, LevelConfirm},
		{"remove from cart", press, btn("Remove"), web, CatDelete, LevelConfirm},
		{"empty trash", click, btn("Empty Trash…"), desk, CatDelete, LevelConfirm},
		{"erase", press, btn("Erase disk"), web, CatDelete, LevelConfirm},
		{"deleted items folder", press, link("Deleted Items"), web, "", LevelNone},
		// subscribe
		{"subscribe", press, btn("Subscribe"), web, CatSubscribe, LevelConfirm},
		{"unsubscribe", press, link("Unsubscribe"), web, CatUnsubscribe, LevelConfirm},
		{"cancel subscription", press, btn("Cancel subscription"), web, CatUnsubscribe, LevelConfirm},
		// sharing
		{"share", press, btn("Share"), web, CatSharing, LevelConfirm},
		{"permissions", press, link("Manage permissions"), web, CatSharing, LevelConfirm},
		{"invite", press, btn("Invite people"), web, CatSharing, LevelConfirm},
		{"api key", press, btn("Create API key"), web, CatSharing, LevelConfirm},
		// install
		{"install", press, btn("Install"), web, CatInstall, LevelConfirm},
		{"run", click, btn("Run"), desk, CatInstall, LevelConfirm},
		{"open downloaded installer", Action{Kind: "double_click", Intent: "x", X: ip(1), Y: ip(1)}, &Element{Role: "cell", Name: "Setup-1.2.dmg"}, Target{Kind: "desktop", App: "com.apple.finder"}, CatInstall, LevelConfirm},
		{"running shoes", press, link("Running shoes"), web, "", LevelNone},
		// security settings
		{"change password", press, btn("Change password"), web, CatSecuritySettings, LevelConfirm},
		{"two-factor", press, link("Two-factor authentication"), web, CatSecuritySettings, LevelConfirm},
		{"vpn", click, btn("Turn off VPN"), desk, CatSecuritySettings, LevelConfirm},
		// captcha
		{"captcha checkbox", click, &Element{Role: "checkbox", Name: "I'm not a robot"}, web, CatCaptcha, LevelConfirm},
		{"recaptcha iframe", click, &Element{Role: "iframe", Type: "iframe", Name: "reCAPTCHA"}, web, CatCaptcha, LevelConfirm},
		{"hcaptcha iframe", click, &Element{Role: "iframe", Name: "Widget containing checkbox for hCaptcha security challenge"}, web, CatCaptcha, LevelConfirm},
		{"typing into captcha", typ("xk3f9"), &Element{Role: "textbox", Name: "Enter the captcha"}, web, CatCaptcha, LevelConfirm},
		// submit to a third party
		{"submit form", press, &Element{Role: "button", Type: "submit", Name: "Submit"}, web, CatSubmitThirdParty, LevelConfirm},
		{"sign up", press, btn("Sign up"), web, CatSubmitThirdParty, LevelConfirm},
		{"register", press, btn("Register"), web, CatSubmitThirdParty, LevelConfirm},
		{"enter on submit", Action{Kind: "keypress", Intent: "x", Keys: []string{"Enter"}}, &Element{Role: "button", Type: "submit", Name: "Submit application"}, web, CatSubmitThirdParty, LevelConfirm},
		{"log in", press, &Element{Role: "button", Type: "submit", Name: "Log in"}, web, "", LevelNone},
		{"sign in", press, btn("Sign in"), web, "", LevelNone},
		{"search", press, &Element{Role: "button", Type: "submit", Name: "Search"}, web, "", LevelNone},
		{"submit search", press, &Element{Role: "button", Type: "submit", Name: "Submit search"}, web, "", LevelNone},
		// sensitive input
		{"card number typed", typ("my card is 4111-1111-1111-1111"), &Element{Role: "textbox", Name: "Message"}, web, CatSensitiveInput, LevelConfirm},
		{"card number set by coordinates", typ("4242424242424242"), nil, desk, CatSensitiveInput, LevelConfirm},
		{"not luhn", typ("4111 1111 1111 1112"), nil, web, "", LevelNone},
		{"ssn", set("078-05-1120"), &Element{Role: "textbox", Name: "Notes"}, web, CatSensitiveInput, LevelConfirm},
		{"ssn plain in ssn field", set("078051120"), &Element{Role: "textbox", Name: "Social Security number"}, web, CatSensitiveInput, LevelConfirm},
		{"nine digits elsewhere", set("078051120"), &Element{Role: "textbox", Name: "Order number"}, web, "", LevelNone},
		{"iban", typ("GB82 WEST 1234 5698 7654 32"), nil, web, CatSensitiveInput, LevelConfirm},
		{"iban mid-sentence", typ("pay to gb82 west 1234 5698 7654 32 thanks"), nil, web, CatSensitiveInput, LevelConfirm},
		{"iban bad checksum", typ("GB83 WEST 1234 5698 7654 32"), nil, web, "", LevelNone},
		{"ordinary text", typ("running shoes size 10"), &Element{Role: "searchbox", Name: "Search"}, web, "", LevelNone},
		// nothing to judge
		{"coordinates only", click, nil, web, "", LevelNone},
		{"navigate", Action{Kind: "navigate", Intent: "x", URL: "https://shop.example/"}, nil, web, "", LevelNone},
		{"scroll on delete button", Action{Kind: "scroll", Intent: "x", DY: 100}, btn("Delete"), web, "", LevelNone},
		// deny comes first
		{"terminal", typ("ls"), nil, Target{Kind: "desktop", App: "com.apple.Terminal"}, CatDenied, LevelDeny},
		{"lock chord", Action{Kind: "keypress", Intent: "x", Keys: []string{"super", "l"}}, nil, desk, CatDenied, LevelDeny},
	}
	for _, c := range cases {
		r := Classify(c.a, c.el, c.t)
		if r.Category != c.want || r.Level != c.lvl {
			t.Errorf("%s: Classify = %+v, want %q/%q", c.name, r, c.lvl, c.want)
		}
		if r.Level != LevelNone && r.Reason == "" {
			t.Errorf("%s: no reason", c.name)
		}
	}
}

func TestMatchPhrase(t *testing.T) {
	cases := []struct {
		text, phrase string
		want         bool
	}{
		{"Pay now", "pay", true},
		{"Payment", "pay", false},
		{"place  your ORDER", "place your order", true},
		{"place order later", "place your order", false},
		{"Privacy & Security", "privacy security", true},
	}
	for _, c := range cases {
		if got := hasPhrase(words(c.text), c.phrase); got != c.want {
			t.Errorf("hasPhrase(%q, %q) = %v", c.text, c.phrase, got)
		}
	}
}
