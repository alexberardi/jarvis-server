package firstrun

import "github.com/alexberardi/jarvis-server/internal/agent/browser"

// Store is the browser extension's page in one browser's store.
type Store struct {
	URL string `json:"url"`
	// Published: the listing is public. Until then the button links to Help instead.
	Published bool `json:"published"`
}

// Stores are the extension's store pages. Chrome's covers Edge, Brave, Arc and Vivaldi.
type Stores struct {
	Firefox Store `json:"firefox"`
	Chrome  Store `json:"chrome"`
	// Help explains browser control while a listing isn't public yet.
	Help string `json:"help"`
}

// DefaultStores are the real listings (docs/agent/store-listing.md). Neither is public yet
// (the 0.2.0 uploads are the user's step, install plan 4.2): flip Published when each one is.
// AMO resolves an add-on by its id, so the Firefox link works before the slug is known.
var DefaultStores = Stores{
	Firefox: Store{URL: "https://addons.mozilla.org/firefox/addon/" + browser.FirefoxExtensionID + "/"},
	Chrome:  Store{URL: "https://chromewebstore.google.com/detail/" + browser.ChromeStoreExtensionID},
	Help:    "https://docs.jarvisautomation.dev/",
}
