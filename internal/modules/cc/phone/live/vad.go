package live

// VADConfig tunes the RMS endpointer (audio/vad.py; defaults from the P0 spike: threshold 250,
// telephone speech measured ~2650-3100 RMS, 800 ms hangover). The BargeIn fields are the
// stricter onset while our own audio plays (docs/decisions/2026-10-21-phone-barge-in.md).
type VADConfig struct {
	ThresholdRMS  float64
	FrameMS       int
	HangoverMS    int
	StartFrames   int     // consecutive loud frames required to enter speech
	PrerollFrames int     // pre-speech frames kept so the utterance keeps its onset
	MaxUtteranceS float64 // force an endpoint after this much continuous speech
	// While the agent plays, only BargeInFrames consecutive frames above BargeInThresholdRMS
	// enter speech, so our own echo and TTS bleed (quieter than the far end's voice) and a
	// click don't. BargeInFrames <= 0: playback suppresses the VAD (half-duplex).
	BargeInThresholdRMS float64
	BargeInFrames       int
}

// DefaultVADConfig is the spike's tuning.
func DefaultVADConfig() VADConfig {
	return VADConfig{ThresholdRMS: 250, FrameMS: 20, HangoverMS: 800, StartFrames: 3, PrerollFrames: 15, MaxUtteranceS: 15,
		BargeInThresholdRMS: 1000, BargeInFrames: 10}
}

func (c VADConfig) hangoverFrames() int { return max(1, c.HangoverMS/c.FrameMS) }

func (c VADConfig) maxUtteranceFrames() int { return int(c.MaxUtteranceS * 1000 / float64(c.FrameMS)) }

// VAD is RMS voice-activity detection with hangover endpointing: a pure state machine fed
// fixed-size frames. Not safe for concurrent use (one per call).
type VAD struct {
	cfg          VADConfig
	frames       [][]int16
	speechFrames int
	silence      int
	inSpeech     bool
}

// NewVAD returns a detector.
func NewVAD(cfg VADConfig) *VAD { return &VAD{cfg: cfg} }

// InSpeech reports whether an utterance is in progress.
func (v *VAD) InSpeech() bool { return v.inSpeech }

// Reset discards all state.
func (v *VAD) Reset() {
	v.frames = nil
	v.speechFrames = 0
	v.silence = 0
	v.inSpeech = false
}

// Feed pushes one frame and returns a finished utterance, or nil. playing (the agent's audio
// is playing) holds the onset to the barge-in thresholds, and an utterance already in
// progress goes on; InSpeech turning true while playing is a barge-in. Without barge-in
// thresholds, playing discards accumulation instead of endpointing on our own echo.
func (v *VAD) Feed(frame []int16, playing bool) []int16 {
	threshold, start := v.cfg.ThresholdRMS, v.cfg.StartFrames
	if playing {
		if v.cfg.BargeInFrames <= 0 {
			v.Reset()
			return nil
		}
		threshold, start = v.cfg.BargeInThresholdRMS, v.cfg.BargeInFrames
	}
	rms := RMS(frame)
	loud := rms > threshold
	if !v.inSpeech {
		v.frames = append(v.frames, frame)
		if n := v.cfg.PrerollFrames; len(v.frames) > n {
			if n <= 0 {
				v.frames = v.frames[:0]
			} else {
				v.frames = append([][]int16(nil), v.frames[len(v.frames)-n:]...)
			}
		}
		if loud {
			v.speechFrames++
			if v.speechFrames >= start {
				v.inSpeech = true
				v.silence = 0
			}
		} else {
			v.speechFrames = 0
		}
		return nil
	}
	v.frames = append(v.frames, frame)
	if rms > v.cfg.ThresholdRMS { // in speech, the normal threshold endpoints
		v.silence = 0
	} else {
		v.silence++
	}
	tooLong := len(v.frames) > v.cfg.maxUtteranceFrames()
	if v.silence >= v.cfg.hangoverFrames() || tooLong {
		var n int
		for _, f := range v.frames {
			n += len(f)
		}
		utt := make([]int16, 0, n)
		for _, f := range v.frames {
			utt = append(utt, f...)
		}
		v.Reset()
		return utt
	}
	return nil
}
