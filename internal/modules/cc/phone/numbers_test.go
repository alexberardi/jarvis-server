package phone

import "testing"

func TestNormalizeUS(t *testing.T) {
	cases := []struct {
		in, want, wantErr string
	}{
		{in: "(732) 592-4183", want: "+17325924183"},
		{in: "+1 908 555 1234", want: "+19085551234"},
		{in: "1-732-592-4183", want: "+17325924183"},
		{in: "911", wantErr: "Emergency and service numbers can't be called."},
		{in: "+988", wantErr: "Emergency and service numbers can't be called."},
		{in: "12345", wantErr: "Short codes and service numbers can't be called."},
		{in: "900-555-1234", wantErr: "Premium-rate numbers can't be called."},
		{in: "976-555-1234", wantErr: "Premium-rate numbers can't be called."},
		{in: "123-456-7890", wantErr: "That doesn't look like a valid US number."},
		{in: "023-456-7890", wantErr: "That doesn't look like a valid US number."},
		{in: "+44 20 7946 0958", wantErr: "Only US numbers are supported right now (10 digits)."},
		{in: "   ", wantErr: "No phone number provided."},
	}
	for _, c := range cases {
		got, err := NormalizeUS(c.in)
		if c.wantErr != "" {
			if err == nil || err.Error() != c.wantErr || !IsNumberError(err) {
				t.Errorf("%q: got (%q, %v), want error %q", c.in, got, err, c.wantErr)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got (%q, %v), want %q", c.in, got, err, c.want)
		}
	}
}

func TestNormalizeName(t *testing.T) {
	if got := NormalizeName("  Tony's Pizzeria! "); got != "tonys pizzeria" {
		t.Fatalf("got %q", got)
	}
}

func TestExtractNumber(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Call us at (732) 592-4183 today", "+17325924183"},
		{"Phone: 732-592-4183", "+17325924183"},
		{"Tel 732.592.4183", "+17325924183"},
		{"+1 732 592 4183", "+17325924183"},
		{"Reach us: 1-732-592-4183", "+17325924183"},
		{"Fax 900-555-1212 or call (732) 592-4183", "+17325924183"},
		{"In an emergency dial 911", ""},
		{"Order 7325924183 shipped", ""},
		{"We are open 9 to 5 daily", ""},
		{"", ""},
	} {
		if got := ExtractNumber(c.in, nil); got != c.want {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
	// D40 11.Q4: a do-not-call number is skipped, the next candidate wins.
	skip := map[string]bool{"+17325924183": true}
	if got := ExtractNumber("Call (732) 592-4183 or 908-555-1234", skip); got != "+19085551234" {
		t.Fatalf("skip: got %q", got)
	}
}

func TestExtractAddress(t *testing.T) {
	if got := ExtractAddress("Visit us at 742 Evergreen Ave, Springfield, IL 62704 today"); got == "" ||
		!contains(got, "742 Evergreen Ave") {
		t.Fatalf("got %q", got)
	}
	for _, prose := range []string{
		"Call for hours",
		"I have been a patient for 2 yrs and am so pleased with the care",
		"been going here 3 years and am still very happy",
		"call us at 5 pm and we will drop by",
		"Open 7 days a week and we are so pleasant",
		"20 years in business, 4 doctors on staff",
		"I waited 45 minutes and was still not seen",
		"we are 3 doors down from Main Street",
	} {
		if got := ExtractAddress(prose); got != "" {
			t.Errorf("%q: got address %q", prose, got)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"1200 Route 70, Brick, NJ 08724", "1200 Route 70, Brick, NJ 08724"},
		{"Located at 55 Main St, Brick NJ", "55 Main St, Brick NJ"},
		{"Office: 8 Kings Hwy Suite 3, Cherry Hill, NJ 08034", "8 Kings Hwy Suite 3, Cherry Hill, NJ 08034"},
		{"15 Chambers Bridge Rd, Brick Township, NJ 08723", "15 Chambers Bridge Rd, Brick Township, NJ 08723"},
		{"742 Evergreen Ave 62704", "742 Evergreen Ave 62704"},
	} {
		if got := ExtractAddress(c.in); got != c.want {
			t.Errorf("%q: got %q want %q", c.in, got, c.want)
		}
	}
}

func TestLocationMismatch(t *testing.T) {
	w := LocationMismatch("12800 Frederick Rd, West Friendship, MD 21794", "Springfield, IL 62704")
	if w != "⚠️ This result is in MD but your household is in IL — check it's the right location before calling." {
		t.Fatalf("got %q", w)
	}
	for _, c := range [][2]string{
		{"742 Evergreen Ave, Springfield, IL 62704", "Springfield, IL 62704"},
		{"1 Beach Ave, Cape May, NJ 08204", "Newark, NJ"},
		{"", "Springfield, IL"},
		{"742 Evergreen Ave, Springfield, IL", ""},
		{"742 Evergreen Ave", "Springfield, IL"},
		{"742 Evergreen Ave, Springfield, IL 62704", "62704"},
		{"12 Main St in the plaza", "Springfield, IL"},
		{"5 Water St, Portland, OR 97204", "Portland, OR"},
	} {
		if got := LocationMismatch(c[0], c[1]); got != "" {
			t.Errorf("%q vs %q: got %q", c[0], c[1], got)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
