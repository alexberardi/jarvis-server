package sip

import (
	"strings"
	"testing"

	"github.com/emiago/diago/media"
)

const asteriskAnswer = "v=0\r\n" +
	"o=- 1953203458 1953203460 IN IP4 192.0.2.10\r\n" +
	"s=Asterisk\r\n" +
	"c=IN IP4 192.0.2.10\r\n" +
	"t=0 0\r\n" +
	"m=audio 15190 RTP/AVP 0 101\r\n" +
	"a=rtpmap:0 PCMU/8000\r\n" +
	"a=rtpmap:101 telephone-event/8000\r\n" +
	"a=fmtp:101 0-16\r\n" +
	"a=ptime:20\r\n" +
	"a=maxptime:140\r\n" +
	"a=sendrecv\r\n"

func TestCanonicalSDPReadsAnAsteriskAnswer(t *testing.T) {
	r, err := canonicalSDP([]byte(asteriskAnswer))
	if err != nil {
		t.Fatal(err)
	}
	if r.ip.String() != "192.0.2.10" || r.port != 15190 || r.dtmfPT != 101 {
		t.Fatalf("%s:%d dtmf %d", r.ip, r.port, r.dtmfPT)
	}
	if !strings.Contains(string(r.body), "a=sendrecv\r\n") || !strings.Contains(string(r.body), "a=rtpmap:101 telephone-event/8000\r\n") {
		t.Fatalf("canonical SDP lost lines:\n%s", r.body)
	}
}

func TestCanonicalSDPTakesTheMediaLevelAddress(t *testing.T) {
	// direct_media: the session-level c= is the PBX, the audio's own c= the phone it bridged
	// to. diago would take the first.
	in := strings.Replace(asteriskAnswer, "a=rtpmap:0", "c=IN IP4 198.51.100.7\r\na=rtpmap:0", 1)
	r, err := canonicalSDP([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if r.ip.String() != "198.51.100.7" {
		t.Fatalf("address %s, want the media-level 198.51.100.7", r.ip)
	}
	if n := strings.Count(string(r.body), "c="); n != 1 || !strings.Contains(string(r.body), "c=IN IP4 198.51.100.7\r\n") {
		t.Fatalf("canonical SDP has %d c= lines:\n%s", n, r.body)
	}
	ms := &media.MediaSession{Codecs: []media.Codec{media.CodecAudioUlaw, media.CodecTelephoneEvent8000}, Mode: "sendrecv"}
	if err := ms.RemoteSDP(r.body); err != nil {
		t.Fatal(err)
	}
	if ms.Raddr.IP.String() != "198.51.100.7" || ms.Raddr.Port != 15190 {
		t.Fatalf("diago reads %v", ms.Raddr)
	}
}

func TestCanonicalSDPTakesTheDTMFPayloadTypeOffered(t *testing.T) {
	in := strings.NewReplacer(" 0 101", " 8 96", "rtpmap:101", "rtpmap:96", "fmtp:101", "fmtp:96", "rtpmap:0 PCMU", "rtpmap:8 PCMA").Replace(asteriskAnswer)
	r, err := canonicalSDP([]byte(in))
	if err != nil || r.dtmfPT != 96 {
		t.Fatalf("dtmf %v %v", r, err)
	}
	// telephone-event mapped but not in the m= line's formats: not offered.
	in = strings.Replace(asteriskAnswer, " 0 101", " 0", 1)
	if r, _ = canonicalSDP([]byte(in)); r.dtmfPT != -1 {
		t.Fatalf("dtmf %d for a format the m= line does not list", r.dtmfPT)
	}
}

func TestCanonicalSDPKeepsOnlyTheAudioSection(t *testing.T) {
	in := asteriskAnswer + "m=video 15192 RTP/AVP 34\r\nc=IN IP4 203.0.113.9\r\na=rtpmap:34 H263/90000\r\n"
	r, err := canonicalSDP([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(r.body), "video") || strings.Contains(string(r.body), "203.0.113.9") || r.ip.String() != "192.0.2.10" {
		t.Fatalf("video leaked:\n%s", r.body)
	}
}

func TestCanonicalSDPDropsRepeatedAndBrokenRtpmaps(t *testing.T) {
	// A repeated rtpmap overran diago's codec slice (a panic in its parser).
	in := strings.Replace(asteriskAnswer, "a=rtpmap:101 telephone-event/8000\r\n",
		"a=rtpmap:101 telephone-event/8000\r\na=rtpmap:101 telephone-event/8000\r\na=rtpmap:101 x\r\na=rtpmap:abc PCMU/8000\r\n", 1)
	r, err := canonicalSDP([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(r.body), "rtpmap:101"); n != 1 {
		t.Fatalf("%d rtpmaps for 101", n)
	}
	ms := &media.MediaSession{Codecs: []media.Codec{media.CodecAudioUlaw, media.CodecTelephoneEvent8000}, Mode: "sendrecv"}
	if err := ms.RemoteSDP(r.body); err != nil {
		t.Fatal(err)
	}
}

func TestCanonicalSDPRefusesWhatItCannotUse(t *testing.T) {
	for name, in := range map[string]string{
		"empty":          "",
		"no audio":       "v=0\r\no=- 1 1 IN IP4 192.0.2.1\r\nc=IN IP4 192.0.2.1\r\nm=video 1 RTP/AVP 34\r\n",
		"short c=":       strings.Replace(asteriskAnswer, "c=IN IP4 192.0.2.10", "c=IN", 1),
		"bad c= address": strings.Replace(asteriskAnswer, "c=IN IP4 192.0.2.10", "c=IN IP4 nowhere", 1),
		"no c=":          strings.Replace(asteriskAnswer, "c=IN IP4 192.0.2.10\r\n", "", 1),
		"short m=":       strings.Replace(asteriskAnswer, "m=audio 15190 RTP/AVP 0 101", "m=audio 15190", 1),
		"bad port":       strings.Replace(asteriskAnswer, "15190", "99999", 1),
		"short o=":       strings.Replace(asteriskAnswer, "o=- 1953203458 1953203460 IN IP4 192.0.2.10", "o=-", 1),
		"no o=":          strings.Replace(asteriskAnswer, "o=- 1953203458 1953203460 IN IP4 192.0.2.10\r\n", "", 1),
		"garbage":        "hello\r\n",
	} {
		if _, err := canonicalSDP([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// FuzzSDP: canonicalSDP never panics, and what it accepts never panics diago's own parsing
// (MediaSession.RemoteSDP), which remote SDP has crashed before.
func FuzzSDP(f *testing.F) {
	f.Add([]byte(asteriskAnswer))
	f.Add([]byte(strings.Replace(asteriskAnswer, "a=rtpmap:0", "c=IN IP4 198.51.100.7\r\na=rtpmap:0", 1)))
	f.Add([]byte("v=0\r\no=- 1 1 IN IP4 1.2.3.4\r\nc=IN IP4 1.2.3.4\r\nm=audio 1 RTP/AVP 0 0 0 101 101\r\na=rtpmap:101 telephone-event/8000\r\n"))
	f.Add([]byte("v=0\nc=IN\nm=audio\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		r, err := canonicalSDP(b)
		if err != nil {
			return
		}
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("diago panicked on canonical SDP %q: %v", r.body, p)
			}
		}()
		ms := &media.MediaSession{Codecs: []media.Codec{media.CodecAudioUlaw, media.CodecAudioAlaw, media.CodecTelephoneEvent8000}, Mode: "sendrecv"}
		_ = ms.RemoteSDP(r.body)
	})
}
