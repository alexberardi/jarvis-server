package browser

import (
	"net/url"
	"strings"
)

// What a browser fill or click does decides whether --confirm-dictation=submit asks first, not
// that it submits (#578, decision 2026-10-23-browser-confirm-by-risk): a search, a navigation
// link or a filter runs; paying, sending, posting, deleting, sharing and account or security
// changes wait for the user's yes. Page text can steer the model, so this list is the
// prompt-injection guard: anything that could cost money, speak for the user or lose data
// stays on it.

// riskyWords are single words (whole words of the label or field, lowercased) that mark an
// action as risky.
var riskyWords = map[string]string{
	// paying
	"buy": "pay", "pay": "pay", "purchase": "pay", "checkout": "pay", "subscribe": "pay",
	"donate": "pay", "upgrade": "pay", "bid": "pay",
	// sending or posting as the user
	"send": "send", "post": "send", "publish": "send", "reply": "send", "tweet": "send",
	"retweet": "send", "repost": "send", "comment": "send", "submit": "send", "confirm": "send",
	// deleting
	"delete": "delete", "remove": "delete", "discard": "delete", "trash": "delete",
	"erase": "delete", "wipe": "delete", "unsubscribe": "delete", "deactivate": "delete",
	// sharing
	"share": "share", "invite": "share", "transfer": "share",
	// account and security
	"password": "account", "passcode": "account", "passphrase": "account", "2fa": "account",
	"security": "account", "reset": "account",
}

// riskyPhrases are word sequences that mark an action as risky where their words alone don't.
var riskyPhrases = map[string]string{
	"place order": "pay", "order now": "pay", "complete order": "pay", "submit order": "pay",
	"check out": "pay", "book now": "pay", "add to cart": "pay", "add payment": "pay",
	"cancel subscription": "delete", "cancel membership": "delete", "cancel order": "delete",
	"close account": "delete", "empty trash": "delete",
	"make public": "share",
	"two factor":  "account", "sign out of all": "account", "log out of all": "account",
	"change email": "account",
}

// riskyFields are words of a field name whose submit sends something as the user.
var riskyFields = map[string]string{
	"message": "send", "comment": "send", "reply": "send", "post": "send", "tweet": "send",
	"body": "send", "subject": "send", "to": "send", "recipient": "send", "email": "send",
	"card": "pay", "cvv": "pay", "cvc": "pay", "amount": "pay",
}

// searchFields are words of a field name that make a fill a search: never risky (unless it is
// a password field).
var searchFields = map[string]bool{"search": true, "q": true, "query": true, "find": true, "lookup": true}

// riskyPaths are words of a page's path where a click or a submit acts on money, mail or the
// account; riskyHosts the sites (and host prefixes) where one sends a message or moves money.
var (
	riskyPaths = map[string]string{
		"checkout": "pay", "cart": "pay", "basket": "pay", "payment": "pay", "payments": "pay",
		"billing": "pay", "pay": "pay", "compose": "send", "security": "account", "password": "account",
	}
	riskyHosts = map[string]string{
		"outlook.live.com": "send", "outlook.office.com": "send", "outlook.office365.com": "send",
		"web.whatsapp.com": "send", "messenger.com": "send", "app.slack.com": "send", "discord.com": "send",
		"web.telegram.org": "send", "teams.microsoft.com": "send", "teams.live.com": "send",
		"paypal.com": "pay", "venmo.com": "pay",
	}
)

// words splits s into lowercase words (letters and digits).
func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < '0' || r > '9') && r < 0x80
	})
}

// wordRisk is the risk of a label or field name by its words and phrases ("" none).
func wordRisk(s string, single map[string]string) string {
	ws := words(s)
	for _, w := range ws {
		if r := single[w]; r != "" {
			return r
		}
	}
	joined := " " + strings.Join(ws, " ") + " "
	for p, r := range riskyPhrases {
		if strings.Contains(joined, " "+p+" ") {
			return r
		}
	}
	return ""
}

// urlRisk is the risk of acting on a page at u by its site and path ("" none or unknown).
func urlRisk(u string) string {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(p.Hostname()), "www.")
	if strings.HasPrefix(host, "mail.") {
		return "send"
	}
	if r := riskyHosts[host]; r != "" {
		return r
	}
	for _, w := range words(p.Path) {
		if r := riskyPaths[w]; r != "" {
			return r
		}
	}
	return ""
}

// clickRisk is why clicking label on the page at pageURL needs the user's yes ("" it doesn't).
func clickRisk(label, pageURL string) string {
	if r := wordRisk(label, riskyWords); r != "" {
		return r
	}
	return urlRisk(pageURL)
}

// submitRisk is why filling field and submitting it on the page at pageURL needs the user's yes
// ("" it doesn't). A search box never does.
func submitRisk(field, pageURL string) string {
	if isPasswordField(field) {
		return "account"
	}
	for _, w := range words(field) {
		if searchFields[w] {
			return ""
		}
	}
	if r := wordRisk(field, riskyFields); r != "" {
		return r
	}
	return urlRisk(pageURL)
}
