package main

import (
	"context"
	"image"
	"image/color"
	"strings"
	"testing"
)

// A trimmed dump of the app's Home tab, as uiautomator prints it (with the trailing line some
// Android versions add on stdout).
const homeDump = `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">
<node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="com.jarvis.nodemobile" content-desc="" clickable="false" enabled="true" focused="false" scrollable="false" password="false" bounds="[0,0][1080,2400]">
  <node index="0" text="Jarvis" resource-id="" class="android.widget.TextView" content-desc="" clickable="false" enabled="true" bounds="[42,160][215,240]"/>
  <node index="1" text="" resource-id="settings-button" class="android.widget.Button" content-desc="" clickable="true" enabled="true" bounds="[790,150][890,250]">
    <node index="0" text="&#984468;" resource-id="" class="android.widget.TextView" content-desc="" bounds="[810,170][870,230]"/>
  </node>
  <node index="2" text="" resource-id="chat-list" class="android.widget.ScrollView" content-desc="" scrollable="true" bounds="[0,400][1080,2000]">
    <node index="0" text="what is 12 times 7" resource-id="" class="android.widget.TextView" content-desc="" bounds="[700,1560][1040,1650]"/>
    <node index="1" text="Jarvis" resource-id="" class="android.widget.TextView" content-desc="" bounds="[60,1700][200,1740]"/>
    <node index="2" text="12 times 7 is 84." resource-id="" class="android.widget.TextView" content-desc="" bounds="[60,1760][340,1810]"/>
    <node index="3" text="a very long label that runs off the edge" resource-id="" class="android.widget.TextView" content-desc="" bounds="[600,1900][1300,1950]"/>
    <node index="4" text="the next page" resource-id="" class="android.widget.TextView" content-desc="" bounds="[1080,1900][2160,1950]"/>
    <node index="5" text="collapsed" resource-id="" class="android.widget.TextView" content-desc="" bounds="[0,0][0,0]"/>
  </node>
  <node index="3" text="" resource-id="" class="android.widget.EditText" content-desc="" hint="Message Jarvis..." bounds="[160,2060][780,2180]"/>
  <node index="4" text="" resource-id="com.jarvis.nodemobile:id/send-button" class="android.widget.Button" content-desc="" bounds="[950,2070][1050,2170]"/>
  <node index="5" text="" resource-id="" class="android.view.View" content-desc="Home, tab, 1 of 5" clickable="true" bounds="[0,2210][216,2330]">
    <node index="0" text="Home" resource-id="" class="android.widget.TextView" content-desc="" bounds="[60,2290][150,2320]"/>
  </node>
  <node index="6" text="Devices" resource-id="" class="android.widget.TextView" content-desc="" bounds="[250,600][400,650]"/>
  <node index="7" text="Devices" resource-id="" class="android.widget.TextView" content-desc="" bounds="[250,2290][400,2320]"/>
</node>
</hierarchy>
UI hierchary dumped to: /dev/tty`

func mustParse(t *testing.T, s string) *Screen {
	t.Helper()
	sc, err := ParseDump([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestParseDump(t *testing.T) {
	sc := mustParse(t, homeDump)
	if sc.W != 1080 || sc.H != 2400 {
		t.Fatalf("display %dx%d, want 1080x2400", sc.W, sc.H)
	}
	if len(sc.Nodes) != 17 {
		t.Fatalf("%d nodes, want 17", len(sc.Nodes))
	}
	box, ok := editText(sc)
	if !ok || box.Hint != "Message Jarvis..." {
		t.Fatalf("composer: %+v %v", box, ok)
	}
	if _, err := ParseDump([]byte("ERROR: could not get idle state.")); err == nil {
		t.Fatal("a uiautomator error parsed")
	}
	if _, err := ParseDump([]byte(`<hierarchy><node bounds="[0,0][1,1]">`)); err == nil {
		t.Fatal("a truncated dump parsed")
	}
}

func TestParseBounds(t *testing.T) {
	r, err := parseBounds("[-5,10][1300,1950]")
	if err != nil || r != (Rect{-5, 10, 1300, 1950}) {
		t.Fatalf("%v %v", r, err)
	}
	if x, y := r.Center(); x != 647 || y != 980 {
		t.Fatalf("center %d,%d", x, y)
	}
	for _, bad := range []string{"", "[1,2]", "1,2,3,4"} {
		if _, err := parseBounds(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestSelectors(t *testing.T) {
	sc := mustParse(t, homeDump)
	cases := []struct {
		sel  string
		want string // the match's text, desc or id; "" = no match
	}{
		{"id=settings-button", "settings-button"},
		{"id=send-button", "com.jarvis.nodemobile:id/send-button"}, // package prefix ignored
		{"text=12 times 7 is 84.", "12 times 7 is 84."},
		{"text~is 84", "12 times 7 is 84."},
		{"desc~^Home, tab", "Home, tab, 1 of 5"},
		{"hint~^Message Jarvis", "Message Jarvis..."},
		{"text=collapsed", ""}, // zero-size views are not on screen
		{"text=nope", ""},
	}
	for _, c := range cases {
		n, ok := sc.Find(MustSelector(c.sel))
		got := ""
		if ok {
			got = n.Text
			if got == "" {
				got = n.Desc
			}
			if got == "" {
				got = n.Hint
			}
			if got == "" {
				got = n.ID
			}
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.sel, got, c.want)
		}
	}
	for _, bad := range []string{"text", "=x", "class=Button", "text~("} {
		if _, err := ParseSelector(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestTab(t *testing.T) {
	sc := mustParse(t, homeDump)
	n, ok := sc.Tab("Home")
	if !ok || n.Desc != "Home, tab, 1 of 5" {
		t.Fatalf("Home tab: %+v", n)
	}
	// No content description: the lowest "Devices" is the tab, not the heading above it.
	n, ok = sc.Tab("Devices")
	if !ok || n.Bounds.Y1 != 2290 {
		t.Fatalf("Devices tab: %+v", n)
	}
	if _, ok := sc.Tab("Pantry"); ok {
		t.Fatal("found a Pantry tab")
	}
}

func TestTextsOverflowBusy(t *testing.T) {
	sc := mustParse(t, homeDump)
	texts := strings.Join(visibleTexts(sc), "|")
	if strings.Count(texts, "Jarvis|") != 1 || strings.Contains(texts, "collapsed") || strings.ContainsRune(texts, 0xF0694) {
		t.Fatalf("texts: %s", texts)
	}
	of := sc.Overflow()
	if len(of) != 1 || !strings.Contains(of[0], "runs off the edge") {
		t.Fatalf("overflow: %v (a wholly off-screen page doesn't count)", of)
	}
	if sc.Busy() {
		t.Fatal("busy without a spinner")
	}
	spin := strings.Replace(homeDump, `class="android.view.View" content-desc="Home`, `class="android.widget.ProgressBar" content-desc="Home`, 1)
	if !mustParse(t, spin).Busy() {
		t.Fatal("a ProgressBar is busy")
	}
	loading := strings.Replace(homeDump, `hint="Message Jarvis..."`, `hint="Loading tools…"`, 1)
	if !mustParse(t, loading).Busy() {
		t.Fatal("a Loading placeholder is busy")
	}
}

func TestReplyAfter(t *testing.T) {
	sc := mustParse(t, homeDump)
	got := replyAfter(sc, "what is 12 times 7")
	if !strings.HasPrefix(got, "Jarvis 12 times 7 is 84.") {
		t.Fatalf("reply %q", got)
	}
	if replyAfter(sc, "never sent") != "" {
		t.Fatal("a reply to a message that isn't there")
	}
}

func TestInputQuoting(t *testing.T) {
	if got := shellQuote(`it's`); got != `'it'\''s'` {
		t.Fatalf("shellQuote: %s", got)
	}
	if got := inputText("DevPass123! a"); got != `'DevPass123!%sa'` {
		t.Fatalf("inputText: %s", got)
	}
}

func TestParseConfig(t *testing.T) {
	base := []string{"--serial", "emulator-5580", "--out", "/tmp/x"}
	c, err := parseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.screens) != len(allScreens) || c.expect != `\b84\b` || c.pages != 3 {
		t.Fatalf("defaults: %+v", c)
	}
	c, err = parseConfig(append(base, "--no-chat", "--screens", "home, chat", "--font-scales", "1.0,1.3"))
	if err != nil || strings.Join(c.screens, ",") != "home" || len(c.scales) != 2 {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = parseConfig(append(base, "--chat", "hello"))
	if err != nil || c.expect != "" {
		t.Fatalf("a custom message has no default expectation: %+v %v", c, err)
	}
	for _, bad := range [][]string{
		{"--out", "/tmp/x"},
		append(base, "--screens", "home,bogus"),
		append(base, "--themes", "sepia"),
		append(base, "--font-scales", "big"),
		append(base, "--chat-expect", "("),
	} {
		if _, err := parseConfig(bad); err == nil {
			t.Errorf("%v parsed", bad)
		}
	}
}

func TestShotName(t *testing.T) {
	for _, c := range []struct {
		screen, theme, scale string
		page                 int
		want                 string
	}{
		{"home", "dark", "1.0", 1, "home__dark"},
		{"settings", "light", "1.3", 1, "settings__light__f1.3"},
		{"settings", "light", "1.0", 2, "settings__light.p2"},
	} {
		if got := shotName(c.screen, c.theme, c.scale, c.page); got != c.want {
			t.Errorf("got %s, want %s", got, c.want)
		}
	}
}

func TestLuminance(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 40, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 40; x++ {
			c := color.RGBA{18, 18, 24, 255} // a dark app
			if y < 10 {
				c = color.RGBA{255, 255, 255, 255} // a light status bar, skipped
			}
			img.Set(x, y, c)
		}
	}
	lum := Luminance(img, 10, 0)
	if lum > 0.1 || !ThemeMatches("dark", lum) || ThemeMatches("light", lum) {
		t.Fatalf("dark screen: %.3f", lum)
	}
	if !ThemeMatches("light", 0.93) || ThemeMatches("dark", 0.93) {
		t.Fatal("light screen")
	}
}

func TestDeviceAlwaysNamesItsSerial(t *testing.T) {
	if _, err := NewDevice(userSerial, nil); err == nil {
		t.Fatal("the user's emulator was accepted")
	}
	if _, err := NewDevice("", nil); err == nil {
		t.Fatal("an empty serial was accepted")
	}
	var calls [][]string
	fake := func(_ context.Context, args ...string) ([]byte, error) {
		calls = append(calls, args)
		return nil, nil
	}
	d, err := NewDevice("emulator-5580", fake)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	_ = d.Tap(ctx, Node{Bounds: Rect{0, 0, 100, 50}})
	_ = d.Type(ctx, "a b")
	_ = d.Look(ctx, "dark", "1.3")
	_ = d.Launch(ctx, "com.jarvis.nodemobile")
	for _, c := range calls {
		if len(c) < 2 || c[0] != "-s" || c[1] != "emulator-5580" {
			t.Fatalf("adb call without -s emulator-5580: %v", c)
		}
	}
	want := []string{"input tap 50 25", "input text 'a%sb'", "cmd uimode night yes", "settings put system font_scale '1.3'"}
	for i, w := range want {
		if got := calls[i][len(calls[i])-1]; got != w {
			t.Errorf("call %d: %q, want %q", i, got, w)
		}
	}
}

func TestScreenOrder(t *testing.T) {
	want := "landing,login,home,devices,pantry,routines,nodes,settings,inbox,inbox_detail,chat"
	if got := strings.Join(allScreens, ","); got != want {
		t.Fatalf("allScreens %s, want %s", got, want)
	}
	if modals["inbox_detail"] != "inbox-button" {
		t.Fatalf("inbox_detail opens from %q, want inbox-button", modals["inbox_detail"])
	}
}

// inboxDump is the inbox modal in select mode: the checkboxes and select-all must not count as
// items.
func inboxDump(items string) string {
	return `<?xml version='1.0' encoding='UTF-8' standalone='yes' ?><hierarchy rotation="0">
<node index="0" text="" resource-id="" class="android.widget.FrameLayout" package="com.jarvis.nodemobile" content-desc="" bounds="[0,0][1080,2400]">
  <node index="0" text="Inbox" resource-id="" class="android.widget.TextView" content-desc="" bounds="[42,160][215,240]"/>
  <node index="1" text="" resource-id="inbox-select-all" class="android.widget.Button" content-desc="" bounds="[790,150][890,250]"/>
  <node index="2" text="" resource-id="inbox-checkbox-7" class="android.widget.CheckBox" content-desc="" bounds="[20,400][100,480]"/>
` + items + `</node>
</hierarchy>`
}

const inboxItems = `  <node index="3" text="" resource-id="inbox-item-9" class="android.view.ViewGroup" content-desc="" clickable="true" bounds="[0,400][1080,600]">
    <node index="0" text="Weather in Boston" resource-id="" class="android.widget.TextView" content-desc="" bounds="[120,420][900,480]"/>
  </node>
  <node index="4" text="" resource-id="com.jarvis.nodemobile:id/inbox-item-7" class="android.view.ViewGroup" content-desc="" clickable="true" bounds="[0,600][1080,800]"/>
`

func TestFirstInboxItem(t *testing.T) {
	if n, ok := firstInboxItem(mustParse(t, inboxDump(""))); ok {
		t.Fatalf("an empty inbox has an item: %+v", n)
	}
	n, ok := firstInboxItem(mustParse(t, inboxDump(inboxItems)))
	if !ok || n.ID != "inbox-item-9" {
		t.Fatalf("first item %+v %v, want inbox-item-9", n, ok)
	}
}

func TestInboxDetailSkipsAnEmptyInbox(t *testing.T) {
	var calls []string
	fake := func(_ context.Context, args ...string) ([]byte, error) {
		last := args[len(args)-1]
		calls = append(calls, last)
		if strings.HasPrefix(last, "cat ") {
			return []byte(inboxDump("")), nil
		}
		return nil, nil
	}
	d, err := NewDevice("emulator-5580", fake)
	if err != nil {
		t.Fatal(err)
	}
	r := &runner{c: &config{pages: 1}, d: d, idx: &Index{Failures: []string{}}}
	if err := r.inboxDetail(context.Background(), look{"dark", "1.3"}); err != nil {
		t.Fatal(err)
	}
	want := []Skip{{Screen: "inbox_detail", Theme: "dark", FontScale: "1.3", Reason: "no inbox items"}}
	if len(r.idx.Skipped) != 1 || r.idx.Skipped[0] != want[0] {
		t.Fatalf("skipped %+v, want %+v", r.idx.Skipped, want)
	}
	if len(r.idx.Failures) != 0 || len(r.idx.Shots) != 0 {
		t.Fatalf("an empty inbox failed or shot: %+v", r.idx)
	}
	for _, c := range calls {
		if strings.HasPrefix(c, "input ") || strings.HasPrefix(c, "screencap") {
			t.Fatalf("an empty inbox tapped or shot: %q", c)
		}
	}
}
