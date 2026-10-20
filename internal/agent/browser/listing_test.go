package browser

import (
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// fullDescription is the "Full description (both stores)" block of store/listing.md: the text
// pasted into both the Chrome Web Store and AMO.
func fullDescription(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("store/listing.md")
	if err != nil {
		t.Fatal(err)
	}
	return descriptionBlock(t, string(b))
}

func descriptionBlock(t *testing.T, listing string) string {
	t.Helper()
	// Windows checkouts convert the listing to CRLF; the markers below match LF.
	listing = strings.ReplaceAll(listing, "\r\n", "\n")
	_, after, ok := strings.Cut(listing, "## Full description (both stores)\n")
	if !ok {
		t.Fatal("listing.md has no Full description section")
	}
	_, block, ok := strings.Cut(after, "```\n")
	if !ok {
		t.Fatal("Full description has no code block")
	}
	block, _, ok = strings.Cut(block, "```")
	if !ok {
		t.Fatal("Full description's code block is not closed")
	}
	return block
}

// A Windows checkout (CRLF) finds the same description as a Linux one.
func TestListingCRLF(t *testing.T) {
	lf := fullDescription(t)
	b, err := os.ReadFile("store/listing.md")
	if err != nil {
		t.Fatal(err)
	}
	crlf := strings.ReplaceAll(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n", "\r\n")
	if got := descriptionBlock(t, crlf); got != lf {
		t.Errorf("CRLF listing gives a different description (%d vs %d bytes)", len(got), len(lf))
	}
}

// The description both stores show says how to turn website access on (Firefox can install an
// MV3 extension with it off), for each browser family, and what doesn't work without it. The
// wording is the admin Add-ons page's too (web/admin/src/lib/addons.ts WEBSITE_ACCESS, whose
// vitest checks this block has it verbatim).
func TestListingAfterInstalling(t *testing.T) {
	d := fullDescription(t)
	i := strings.Index(d, "AFTER INSTALLING")
	if i < 0 {
		t.Fatal("full description has no AFTER INSTALLING section")
	}
	if p := strings.Index(d, "PRIVATE BY DESIGN"); p < i {
		t.Errorf("AFTER INSTALLING comes after PRIVATE BY DESIGN; keep it near the top")
	}
	for _, want := range []string{
		"Firefox: open about:addons, click Jarvis Browser Control, open Permissions",
		`"Access your data for all websites"`,
		"Chrome, Edge, Brave and other Chromium browsers: open Extensions, click Details",
		`Site access to "On all sites"`,
		"macro recordings capture only page loads",
		"https://docs.jarvisautomation.dev/clients/desktop-agent/#turn-on-website-access",
	} {
		if !strings.Contains(d[i:], want) {
			t.Errorf("AFTER INSTALLING lacks %q", want)
		}
	}
	// The Chrome Web Store's detailed description takes at most 16,000 characters; AMO's
	// description has no smaller limit.
	if n := utf8.RuneCountInString(d); n > 16000 {
		t.Errorf("full description is %d characters (Chrome Web Store: at most 16,000)", n)
	}
}
