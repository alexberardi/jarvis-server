package phone

import (
	"net/http/httptest"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
)

// Twilio signs the URL it requested: under the config port's /cc mount that is the prefix plus
// the stripped path.
func TestMediaSignatureURL(t *testing.T) {
	for _, c := range []struct {
		name, public, prefix, want string
	}{
		{name: "own port", want: "https://jarvis.lan:7703" + MediaPath + "tok"},
		{name: "mounted", prefix: "/cc", want: "https://jarvis.lan:7703/cc" + MediaPath + "tok"},
		{name: "public URL", public: "https://phone.example/", want: "https://phone.example" + MediaPath + "tok"},
		// On a single-port server the public URL ends in /cc already (the stream URL is built from it).
		{name: "public URL single port", public: "https://phone.example/cc", prefix: "/cc", want: "https://phone.example/cc" + MediaPath + "tok"},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := &Service{Options: Options{PublicURL: c.public}}
			r := httptest.NewRequest("GET", MediaPath+"tok", nil)
			r.Host = "jarvis.lan:7703"
			if c.prefix != "" {
				r = httpx.WithMountPrefix(r, c.prefix)
			}
			if got := newTwilioMedia(s).signatureURL(r); got != c.want {
				t.Errorf("signatureURL = %q, want %q", got, c.want)
			}
		})
	}
}
