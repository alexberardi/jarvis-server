package computeruse

// English word lists for Classify. Each entry is a phrase matched case-insensitively as whole
// words (see words and hasPhrase): "pay" matches "Pay $12.99" but not "Payment methods" or
// "PayPal". Order inside a list does not matter; the order of the lists does (riskWords).

var payWords = []string{
	"place order", "place your order", "buy now", "buy it now", "pay", "pay now",
	"confirm purchase", "complete purchase", "complete order", "confirm order",
	"submit payment", "submit order", "confirm and pay", "order now",
}

var deleteWords = []string{
	"delete", "remove", "erase", "empty trash", "empty bin", "move to trash", "move to bin",
	"empty recycle bin", "permanently delete",
}

var securityWords = []string{
	"change password", "reset password", "update password", "new password",
	"two factor", "2fa", "mfa", "multi factor", "security settings", "security key",
	"passkey", "passkeys", "recovery codes", "recovery code", "vpn", "firewall",
}

var sharingWords = []string{
	"share", "permissions", "permission", "invite", "api key", "api keys", "access token",
	"access tokens", "make public", "add collaborator", "add collaborators",
}

var installWords = []string{
	"install", "run", "open downloaded", "run anyway", "install anyway",
}

// installerSuffixes mark a downloaded installer or script when the element's name ends with one
// (a file in Finder or a downloads list): opening it is "installing or running downloaded software".
var installerSuffixes = []string{
	".dmg", ".pkg", ".mpkg", ".exe", ".msi", ".appimage", ".deb", ".rpm", ".flatpakref",
	".sh", ".command", ".bat", ".ps1",
}

var unsubscribeWords = []string{
	"unsubscribe", "cancel subscription", "cancel membership", "cancel plan", "opt out",
}

var subscribeWords = []string{
	"subscribe", "start subscription", "start free trial", "start trial", "free trial",
}

var sendWords = []string{
	"send", "post", "publish", "reply", "reply all", "tweet", "send message",
}

// submitWords name a button that submits a form to the site. Login and search forms are
// pre-approved by the request itself (COMPUTER-CONTROL §Safety), so preApprovedSubmitWords win.
var submitWords = []string{
	"submit", "sign up", "register", "apply", "enroll", "enrol", "rsvp",
}

var preApprovedSubmitWords = []string{
	"log in", "login", "sign in", "signin", "search", "go", "find",
}

var captchaWords = []string{
	"captcha", "recaptcha", "hcaptcha", "not a robot", "i am human", "verify you are human",
}

// ssnFieldWords mark a field that asks for a social security number, where nine bare digits are one.
var ssnFieldWords = []string{"ssn", "social security", "social security number"}

// riskWords is the order Classify tries the name lists in: the first match wins, so "Submit
// payment" is pay and "Delete API key" is delete.
var riskWords = []struct {
	category string
	words    []string
}{
	{CatPay, payWords},
	{CatDelete, deleteWords},
	{CatSecuritySettings, securityWords},
	{CatSharing, sharingWords},
	{CatInstall, installWords},
	{CatUnsubscribe, unsubscribeWords},
	{CatSubscribe, subscribeWords},
	{CatSend, sendWords},
}
