package tts

import "strings"

// kokoroVoices is the speaker table of sherpa-onnx's kokoro-multi-lang-v1_0 voices.bin, in
// speaker-id order (the index is the sid). bm_george is 26 (PLAN §3.3; confirmed in the voice
// spike by embedding match against the Python output). em_santa (53) was appended later; ids
// 0-52 are unchanged across the v1.0 packs.
var kokoroVoices = []string{
	"af_alloy", "af_aoede", "af_bella", "af_heart", "af_jessica", "af_kore", "af_nicole", "af_nova",
	"af_river", "af_sarah", "af_sky", "am_adam", "am_echo", "am_eric", "am_fenrir", "am_liam",
	"am_michael", "am_onyx", "am_puck", "am_santa", "bf_alice", "bf_emma", "bf_isabella", "bf_lily",
	"bm_daniel", "bm_fable", "bm_george", "bm_lewis", "ef_dora", "em_alex", "ff_siwis", "hf_alpha",
	"hf_beta", "hm_omega", "hm_psi", "if_sara", "im_nicola", "jf_alpha", "jf_gongitsune", "jf_nezumi",
	"jf_tebukuro", "jm_kumo", "pf_dora", "pm_alex", "pm_santa", "zf_xiaobei", "zf_xiaoni", "zf_xiaoxiao",
	"zf_xiaoyi", "zm_yunjian", "zm_yunxi", "zm_yunxia", "zm_yunyang", "em_santa",
}

// DefaultVoice is the legacy and prod voice.
const DefaultVoice = "bm_george"

// voice is a resolved Kokoro voice: its speaker id and the front end it needs.
type voice struct {
	Name    string
	SID     int
	Lexicon string // file(s) inside the model directory, comma-separated; "" = espeak-ng only
	Lang    string // espeak-ng language for out-of-lexicon words
}

// voiceByName resolves a voice name (case-insensitive) to its sid and front end. The first
// letter is the language, as in Kokoro's own lang_code ('a' American, 'b' British, ...): the
// legacy provider derived KPipeline's lang_code the same way.
func voiceByName(name string) (voice, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for sid, v := range kokoroVoices {
		if v == name {
			lex, lang := frontEnd(name[0])
			return voice{Name: name, SID: sid, Lexicon: lex, Lang: lang}, true
		}
	}
	return voice{}, false
}

// frontEnd maps a Kokoro language letter to a lexicon and an espeak-ng language. British
// voices use the GB lexicon with espeak "en" (validated by ear and by transcript in the voice
// spike); American ones the US lexicon with "en-us". The rest have no lexicon in the pack and
// go through espeak-ng alone (not validated).
func frontEnd(lang byte) (lexicon, espeak string) {
	switch lang {
	case 'a':
		return "lexicon-us-en.txt", "en-us"
	case 'b':
		return "lexicon-gb-en.txt", "en"
	case 'e':
		return "", "es"
	case 'f':
		return "", "fr"
	case 'h':
		return "", "hi"
	case 'i':
		return "", "it"
	case 'j':
		return "", "ja"
	case 'p':
		return "", "pt-br"
	case 'z':
		return "lexicon-zh.txt", "cmn"
	}
	return "lexicon-us-en.txt", "en-us"
}

// Voices lists the Kokoro voice names jarvisd knows, in speaker-id order.
func Voices() []string { return append([]string(nil), kokoroVoices...) }
