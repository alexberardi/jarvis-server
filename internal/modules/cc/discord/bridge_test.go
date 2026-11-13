package discord_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/discord"
)

type sent struct{ channel, text string }

type fakeOut struct {
	mu      sync.Mutex
	reacts  []string
	sends   []sent
	voices  []string
	threads []string
	dms     []sent
}

func (f *fakeOut) React(_ context.Context, ch, msg, e string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reacts = append(f.reacts, ch+"/"+msg+" "+e)
	return nil
}
func (f *fakeOut) Typing(context.Context, string) error { return nil }
func (f *fakeOut) Send(_ context.Context, ch, t string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, sent{ch, t})
	return nil
}
func (f *fakeOut) SendVoice(_ context.Context, ch string, _ []byte, _ float64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.voices = append(f.voices, ch)
	return nil
}
func (f *fakeOut) StartThread(_ context.Context, ch, msg, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads = append(f.threads, ch+"/"+msg+" "+name)
	return "thread-" + msg, nil
}
func (f *fakeOut) SendDM(_ context.Context, u, t string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dms = append(f.dms, sent{u, t})
	return nil
}

type chatCall struct {
	user             int64
	node, conv, text string
	images           int
}

type env struct {
	b     *discord.Bridge
	out   *fakeOut
	calls []chatCall
	reply string
	err   error
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{out: &fakeOut{}, reply: "It is noon."}
	st := newStore(t)
	e.b = &discord.Bridge{
		HouseholdID: "hh", ChannelID: "chan", BotUserID: "bot",
		Store: st, Codes: &discord.Codes{}, Out: e.out,
		Chat: func(_ context.Context, uid int64, _, node, conv, text string, imgs []discord.Image) (string, string, error) {
			e.calls = append(e.calls, chatCall{uid, node, conv, text, len(imgs)})
			return e.reply, "conv-1", e.err
		},
		Fetch: func(context.Context, string, int64) ([]byte, error) { return []byte("data"), nil },
		Nodes: func(context.Context, string) ([]discord.NodeInfo, error) {
			return []discord.NodeInfo{{ID: "n1", Name: "Kitchen"}, {ID: "n2", Name: "Office"}}, nil
		},
	}
	if err := st.Put(context.Background(), discord.Link{HouseholdID: "hh", DiscordUserID: "alex", UserID: 7, NodeID: "n1"}); err != nil {
		t.Fatal(err)
	}
	return e
}

var bg = context.Background()

func TestIgnoresEveryoneItShould(t *testing.T) {
	e := newEnv(t)
	for name, m := range map[string]discord.Message{
		"a bot":                     {ID: "1", ChannelID: "chan", AuthorID: "other", AuthorBot: true, Content: "hi"},
		"itself":                    {ID: "1", ChannelID: "chan", AuthorID: "bot", Content: "hi"},
		"a stranger in the channel": {ID: "1", ChannelID: "chan", AuthorID: "stranger", Content: "hi"},
		"a stranger in a DM":        {ID: "1", ChannelID: "dm1", DM: true, AuthorID: "stranger", Content: "hi"},
		"a linked user elsewhere":   {ID: "1", ChannelID: "other-chan", AuthorID: "alex", Content: "hi"},
		"an empty message":          {ID: "1", ChannelID: "chan", AuthorID: "alex", Content: "  "},
	} {
		e.b.HandleMessage(bg, m)
		if len(e.out.sends)+len(e.out.reacts)+len(e.calls) != 0 {
			t.Fatalf("%s got a response: sends=%v reacts=%v", name, e.out.sends, e.out.reacts)
		}
	}
}

func TestDMReply(t *testing.T) {
	e := newEnv(t)
	e.b.HandleMessage(bg, discord.Message{ID: "m1", ChannelID: "dm1", DM: true, AuthorID: "alex", Content: "what time is it"})
	if len(e.calls) != 1 || e.calls[0].user != 7 || e.calls[0].node != "n1" || e.calls[0].text != "what time is it" {
		t.Fatalf("chat call %+v", e.calls)
	}
	if len(e.out.sends) != 1 || e.out.sends[0] != (sent{"dm1", "It is noon."}) || len(e.out.threads) != 0 {
		t.Fatalf("sends %v threads %v", e.out.sends, e.out.threads)
	}
	if got := strings.Join(e.out.reacts, ","); got != "dm1/m1 👀,dm1/m1 ✅" {
		t.Fatalf("reactions %q", got)
	}
	// The conversation carries on.
	e.b.HandleMessage(bg, discord.Message{ID: "m2", ChannelID: "dm1", DM: true, AuthorID: "alex", Content: "and tomorrow?"})
	if e.calls[1].conv != "conv-1" {
		t.Fatalf("second turn conversation %q", e.calls[1].conv)
	}
}

func TestChannelRepliesInAThread(t *testing.T) {
	e := newEnv(t)
	e.b.HandleMessage(bg, discord.Message{ID: "m1", ChannelID: "chan", AuthorID: "alex", Content: "<@bot> turn the lights off"})
	if len(e.out.threads) != 1 || !strings.HasPrefix(e.out.threads[0], "chan/m1 turn the lights off") {
		t.Fatalf("threads %v", e.out.threads)
	}
	if e.calls[0].text != "turn the lights off" {
		t.Fatalf("mention not stripped: %q", e.calls[0].text)
	}
	if len(e.out.sends) != 1 || e.out.sends[0].channel != "thread-m1" {
		t.Fatalf("reply went to %v", e.out.sends)
	}
	// A follow up inside the thread answers in the thread without starting another, same conversation.
	e.b.HandleMessage(bg, discord.Message{ID: "m2", ChannelID: "thread-m1", ParentID: "chan", AuthorID: "alex", Content: "and the fan"})
	if len(e.out.threads) != 1 || e.out.sends[1].channel != "thread-m1" || e.calls[1].conv != "conv-1" {
		t.Fatalf("thread follow up: threads=%v sends=%v calls=%+v", e.out.threads, e.out.sends, e.calls)
	}
}

func TestChatFailureIsToldBriefly(t *testing.T) {
	e := newEnv(t)
	e.err = errors.New("Node n1 not found in household hh")
	e.b.HandleMessage(bg, discord.Message{ID: "m1", ChannelID: "dm1", DM: true, AuthorID: "alex", Content: "hi"})
	if len(e.out.sends) != 1 || !strings.Contains(e.out.sends[0].text, "could not answer") {
		t.Fatalf("sends %v", e.out.sends)
	}
	if !strings.HasSuffix(strings.Join(e.out.reacts, ","), "❌") {
		t.Fatalf("reactions %v", e.out.reacts)
	}
}

func TestLongReplySplits(t *testing.T) {
	e := newEnv(t)
	e.reply = strings.Repeat("word ", 1000)
	e.b.HandleMessage(bg, discord.Message{ID: "m1", ChannelID: "dm1", DM: true, AuthorID: "alex", Content: "tell me a lot"})
	if len(e.out.sends) < 3 {
		t.Fatalf("only %d messages", len(e.out.sends))
	}
	for _, s := range e.out.sends {
		if len([]rune(s.text)) > 1900 {
			t.Fatalf("a message is %d characters", len([]rune(s.text)))
		}
	}
}

func TestSplit(t *testing.T) {
	if got := discord.Split("short", 10); len(got) != 1 || got[0] != "short" {
		t.Fatalf("%v", got)
	}
	got := discord.Split("aaaa bbbb\n\ncccc dddd", 12)
	if len(got) != 2 || got[0] != "aaaa bbbb" || got[1] != "cccc dddd" {
		t.Fatalf("%q", got)
	}
	if got := discord.Split(strings.Repeat("é", 25), 10); len(got) != 3 || len([]rune(got[0])) != 10 {
		t.Fatalf("multibyte %q", got)
	}
}

func TestImagesAreForwarded(t *testing.T) {
	e := newEnv(t)
	e.b.HandleMessage(bg, discord.Message{ID: "m1", ChannelID: "dm1", DM: true, AuthorID: "alex", Content: "what is this",
		Attachments: []discord.Attachment{{URL: "https://cdn.discordapp.com/a.png", ContentType: "image/png"}, {URL: "x", ContentType: "text/plain"}}})
	if e.calls[0].images != 1 {
		t.Fatalf("images %d", e.calls[0].images)
	}
}

func TestVoiceClipInVoiceReplyOut(t *testing.T) {
	e := newEnv(t)
	e.b.STT = func(context.Context, []byte, string) (string, error) { return "turn on the lamp", nil }
	e.b.TTS = func(context.Context, string) ([]byte, float64, error) { return []byte("ogg"), 2.5, nil }
	clip := discord.Attachment{URL: "https://cdn.discordapp.com/v.ogg", ContentType: "audio/ogg", VoiceMessage: true, Seconds: 3}
	e.b.HandleMessage(bg, discord.Message{ID: "m1", ChannelID: "dm1", DM: true, AuthorID: "alex", Attachments: []discord.Attachment{clip}})
	if e.calls[0].text != "turn on the lamp" {
		t.Fatalf("transcript not used: %q", e.calls[0].text)
	}
	if !strings.HasPrefix(e.out.sends[0].text, "Heard: turn on the lamp") {
		t.Fatalf("transcript not echoed: %q", e.out.sends[0].text)
	}
	if len(e.out.voices) != 1 {
		t.Fatalf("a voice clip should get a voice reply: %v", e.out.voices)
	}

	// A typed message in auto mode gets text only.
	e.b.HandleMessage(bg, discord.Message{ID: "m2", ChannelID: "dm1", DM: true, AuthorID: "alex", Content: "and now"})
	if len(e.out.voices) != 1 {
		t.Fatal("typed message got a voice reply in auto mode")
	}
	// A clip that is too long is declined before anything is downloaded.
	long := clip
	long.Seconds = 500
	n := len(e.calls)
	e.b.HandleMessage(bg, discord.Message{ID: "m3", ChannelID: "dm1", DM: true, AuthorID: "alex", Attachments: []discord.Attachment{long}})
	if len(e.calls) != n || !strings.Contains(e.out.sends[len(e.out.sends)-1].text, "too long") {
		t.Fatalf("long clip: %v", e.out.sends)
	}
	// No STT configured: declined politely.
	e.b.STT = nil
	e.b.HandleMessage(bg, discord.Message{ID: "m4", ChannelID: "dm1", DM: true, AuthorID: "alex", Attachments: []discord.Attachment{clip}})
	if !strings.Contains(e.out.sends[len(e.out.sends)-1].text, "not set up") {
		t.Fatalf("no STT: %v", e.out.sends)
	}
}

func TestSlashCommands(t *testing.T) {
	e := newEnv(t)
	dm := func(name string, args map[string]string) discord.Reply {
		return e.b.HandleCommand(bg, discord.Command{UserID: "alex", ChannelID: "dm1", DM: true, Name: name, Args: args})
	}
	if r := dm("ask", map[string]string{"message": "hi"}); r.Text != "It is noon." || r.Ephemeral {
		t.Fatalf("ask %+v", r)
	}
	if r := dm("new", nil); !r.Ephemeral {
		t.Fatalf("new %+v", r)
	}
	dm("ask", map[string]string{"message": "again"})
	if e.calls[1].conv != "" {
		t.Fatalf("/jarvis new did not start a fresh conversation: %q", e.calls[1].conv)
	}
	if r := dm("node", map[string]string{"name": "office"}); !strings.Contains(r.Text, "Office") {
		t.Fatalf("node %+v", r)
	}
	dm("ask", map[string]string{"message": "x"})
	if e.calls[2].node != "n2" {
		t.Fatalf("node not switched: %q", e.calls[2].node)
	}
	if r := dm("node", map[string]string{"name": "garage"}); !strings.Contains(r.Text, "Kitchen, Office") {
		t.Fatalf("unknown node %+v", r)
	}
	if r := dm("voice", map[string]string{"mode": "loud"}); !strings.Contains(r.Text, "auto") {
		t.Fatalf("voice %+v", r)
	}
	dm("voice", map[string]string{"mode": "never"})
	dm("notify", map[string]string{"state": "on"})
	if r := dm("status", nil); !strings.Contains(r.Text, "never") || !strings.Contains(r.Text, "Reminder messages: on") {
		t.Fatalf("status %+v", r)
	}
	// Wrong place: refused privately.
	// The voice channel's own text chat is a place too (where a person runs /jarvis join), once configured.
	vcAsk := discord.Command{UserID: "alex", ChannelID: "vc1", Name: "ask", Args: map[string]string{"message": "hi"}}
	if r := e.b.HandleCommand(bg, vcAsk); !r.Ephemeral || !strings.Contains(r.Text, "direct messages") {
		t.Fatalf("voice chat before it is configured %+v", r)
	}
	e.b.VoiceChannelID = "vc1"
	if r := e.b.HandleCommand(bg, vcAsk); r.Ephemeral || r.Text != "It is noon." {
		t.Fatalf("voice chat %+v", r)
	}
	e.b.VoiceChannelID = ""
	r := e.b.HandleCommand(bg, discord.Command{UserID: "alex", ChannelID: "elsewhere", Name: "ask", Args: map[string]string{"message": "hi"}})
	if !r.Ephemeral || !strings.Contains(r.Text, "direct messages") {
		t.Fatalf("wrong place %+v", r)
	}
	// Unlinked: told how to link, and nothing runs.
	n := len(e.calls)
	r = e.b.HandleCommand(bg, discord.Command{UserID: "stranger", ChannelID: "dm2", DM: true, Name: "ask", Args: map[string]string{"message": "hi"}})
	if !r.Ephemeral || !strings.Contains(r.Text, "Link") || len(e.calls) != n {
		t.Fatalf("unlinked %+v", r)
	}
}

func TestLinkCommand(t *testing.T) {
	e := newEnv(t)
	code := e.b.Codes.New("hh", 9, "n2")
	r := e.b.HandleCommand(bg, discord.Command{UserID: "sam", ChannelID: "dm9", DM: true, Name: "link", Args: map[string]string{"code": code}})
	if !r.Ephemeral || !strings.HasPrefix(r.Text, "Linked") {
		t.Fatalf("link %+v", r)
	}
	l, ok, _ := e.b.Store.ByDiscord(bg, "hh", "sam")
	if !ok || l.UserID != 9 || l.NodeID != "n2" || l.Proactive {
		t.Fatalf("stored link %+v", l)
	}
	// The same code does not work twice, and a wrong one says so.
	r = e.b.HandleCommand(bg, discord.Command{UserID: "eve", ChannelID: "dm8", DM: true, Name: "link", Args: map[string]string{"code": code}})
	if !strings.Contains(r.Text, "wrong or has expired") {
		t.Fatalf("reused code %+v", r)
	}
	// A code made for another household does not link here.
	other := e.b.Codes.New("other-hh", 11, "n1")
	r = e.b.HandleCommand(bg, discord.Command{UserID: "eve", ChannelID: "dm8", DM: true, Name: "link", Args: map[string]string{"code": other}})
	if _, ok, _ := e.b.Store.ByDiscord(bg, "hh", "eve"); ok || !strings.Contains(r.Text, "wrong or has expired") {
		t.Fatalf("cross household link: %+v", r)
	}
}

func TestNotifyOnlyForOptedIn(t *testing.T) {
	e := newEnv(t)
	if was, err := e.b.Notify(bg, 7, "Reminder: take the bins out"); was || err != nil || len(e.out.dms) != 0 {
		t.Fatalf("a user who never opted in got a message: %v %v %v", was, err, e.out.dms)
	}
	e.b.HandleCommand(bg, discord.Command{UserID: "alex", ChannelID: "dm1", DM: true, Name: "notify", Args: map[string]string{"state": "on"}})
	if was, err := e.b.Notify(bg, 7, "Reminder: take the bins out"); !was || err != nil || len(e.out.dms) != 1 || e.out.dms[0] != (sent{"alex", "Reminder: take the bins out"}) {
		t.Fatalf("opted-in user: %v %v %v", was, err, e.out.dms)
	}
	// Nothing for a user with no link at all, and nothing goes to the channel.
	if was, _ := e.b.Notify(bg, 99, "x"); was || len(e.out.sends) != 0 {
		t.Fatal("notified someone with no link, or posted in a channel")
	}
	e.b.HandleCommand(bg, discord.Command{UserID: "alex", ChannelID: "dm1", DM: true, Name: "notify", Args: map[string]string{"state": "off"}})
	if was, _ := e.b.Notify(bg, 7, "again"); was || len(e.out.dms) != 1 {
		t.Fatal("opt out ignored")
	}
}

type fakeVoice struct {
	joined []string
	left   int
	err    error
}

func (f *fakeVoice) Join(_ context.Context, u string) (string, error) {
	f.joined = append(f.joined, u)
	return "Joined. Talk whenever you like.", f.err
}
func (f *fakeVoice) Leave(context.Context) string { f.left++; return "Left the voice channel." }

func TestVoiceCommands(t *testing.T) {
	e := newEnv(t)
	cmd := func(name string, user string) discord.Reply {
		return e.b.HandleCommand(bg, discord.Command{UserID: user, ChannelID: "chan", Name: name})
	}
	// No voice channel set up: told so, privately.
	if r := cmd("join", "alex"); !r.Ephemeral || !strings.Contains(r.Text, "not set up") {
		t.Fatalf("no voice: %+v", r)
	}
	v := &fakeVoice{}
	e.b.VoiceCh = v
	if r := cmd("join", "alex"); r.Ephemeral || len(v.joined) != 1 || v.joined[0] != "alex" {
		t.Fatalf("join %+v %v", r, v.joined)
	}
	// A stranger cannot make it join, or leave.
	n := len(v.joined)
	if r := cmd("join", "stranger"); !strings.Contains(r.Text, "Link") || len(v.joined) != n {
		t.Fatalf("stranger join %+v", r)
	}
	if r := cmd("leave", "stranger"); v.left != 0 || !strings.Contains(r.Text, "Link") {
		t.Fatalf("stranger leave %+v", r)
	}
	if r := cmd("leave", "alex"); v.left != 1 || r.Text != "Left the voice channel." {
		t.Fatalf("leave %+v", r)
	}
	// The join error reaches the person, privately.
	v.err = errors.New("Join the Jarvis voice channel first, then run this again.")
	if r := cmd("join", "alex"); !r.Ephemeral || !strings.Contains(r.Text, "Join the Jarvis voice channel first") {
		t.Fatalf("join error %+v", r)
	}
}

// A phone photo is bigger than the chat accepts, so it is shrunk before it is sent on; a file that
// is not an image, or still too big, is declined with a clear reason.
func TestPhotosAreShrunk(t *testing.T) {
	e := newEnv(t)
	var got []discord.Image
	e.b.Chat = func(_ context.Context, _ int64, _, _, _, _ string, imgs []discord.Image) (string, string, error) {
		got = imgs
		return "It says: hello.", "conv-1", nil
	}
	big := make([]byte, 6<<20) // a 6 MB photo straight from a phone
	e.b.Fetch = func(_ context.Context, _ string, max int64) ([]byte, error) {
		if int64(len(big)) > max {
			t.Fatalf("downloaded with a cap of %d, smaller than a normal phone photo", max)
		}
		return big, nil
	}
	e.b.Shrink = func(raw []byte) ([]byte, bool) { return []byte("small jpeg"), true }
	photo := discord.Attachment{URL: "https://cdn.discordapp.com/a.heic", ContentType: "image/heic"}
	e.b.HandleMessage(bg, discord.Message{ID: "m1", ChannelID: "dm1", DM: true, AuthorID: "alex", Content: "translate this", Attachments: []discord.Attachment{photo}})
	if len(got) != 1 || string(got[0].Data) != "small jpeg" || got[0].MediaType != "image/jpeg" {
		t.Fatalf("image sent on: %+v", got)
	}
	if e.out.sends[0].text != "It says: hello." {
		t.Fatalf("reply %v", e.out.sends)
	}

	// An unreadable file.
	got = nil
	e.b.Shrink = func([]byte) ([]byte, bool) { return nil, false }
	e.b.HandleMessage(bg, discord.Message{ID: "m2", ChannelID: "dm1", DM: true, AuthorID: "alex", Content: "translate this", Attachments: []discord.Attachment{photo}})
	last := e.out.sends[len(e.out.sends)-1].text
	if got != nil || !strings.Contains(last, "couldn't read that image") {
		t.Fatalf("unreadable image: sent=%v reply=%q", got, last)
	}
	// Still too big after shrinking.
	e.b.Shrink = func([]byte) ([]byte, bool) { return make([]byte, 3<<20), true }
	e.b.HandleMessage(bg, discord.Message{ID: "m3", ChannelID: "dm1", DM: true, AuthorID: "alex", Content: "translate this", Attachments: []discord.Attachment{photo}})
	if last := e.out.sends[len(e.out.sends)-1].text; got != nil || !strings.Contains(last, "too big") {
		t.Fatalf("oversized image: sent=%v reply=%q", got, last)
	}
	// A photo with no words still goes through (it may just be "translate this" in the picture).
	e.b.Shrink = func([]byte) ([]byte, bool) { return []byte("small jpeg"), true }
	e.b.HandleMessage(bg, discord.Message{ID: "m4", ChannelID: "dm1", DM: true, AuthorID: "alex", Attachments: []discord.Attachment{photo}})
	if len(got) != 1 {
		t.Fatalf("photo without text not sent on: %+v", got)
	}
}

// "/jarvis ..." typed as plain text is a common slip (the command was not picked from the list).
// Anyone who does it, linked or not, is told what to do, and it is not sent to Jarvis as chat.
func TestTypedCommandIsExplained(t *testing.T) {
	e := newEnv(t)
	for _, who := range []string{"alex", "stranger"} {
		before := len(e.out.sends)
		e.b.HandleMessage(bg, discord.Message{ID: "t1", ChannelID: "chan", AuthorID: who, Content: "/jarvis link code:K7M2QXP9"})
		if len(e.out.sends) != before+1 || !strings.Contains(e.out.sends[before].text, "went in as a normal message") {
			t.Fatalf("%s: sends %v", who, e.out.sends)
		}
		if e.out.sends[before].channel != "chan" {
			t.Fatalf("%s: explained in %q, not where they typed", who, e.out.sends[before].channel)
		}
	}
	if len(e.calls) != 0 {
		t.Fatalf("a typed command went to Jarvis as chat: %+v", e.calls)
	}
	// Elsewhere, and from bots, it is still ignored.
	n := len(e.out.sends)
	e.b.HandleMessage(bg, discord.Message{ID: "t2", ChannelID: "elsewhere", AuthorID: "alex", Content: "/jarvis new"})
	e.b.HandleMessage(bg, discord.Message{ID: "t3", ChannelID: "chan", AuthorID: "other", AuthorBot: true, Content: "/jarvis new"})
	if len(e.out.sends) != n {
		t.Fatal("answered outside the places it answers, or answered a bot")
	}
}
