package live

// VADConfig tunes the RMS endpointer (audio/vad.py; defaults from the P0 spike: threshold 250,
// telephone speech measured ~2650-3100 RMS, 800 ms hangover).
type VADConfig struct {
	ThresholdRMS  float64
	FrameMS       int
	HangoverMS    int
	StartFrames   int     // consecutive loud frames required to enter speech
	PrerollFrames int     // pre-speech frames kept so the utterance keeps its onset
	MaxUtteranceS float64 // force an endpoint after this much continuous speech
}

// DefaultVADConfig is the spike's tuning.
func DefaultVADConfig() VADConfig {
	return VADConfig{ThresholdRMS: 250, FrameMS: 20, HangoverMS: 800, StartFrames: 3, PrerollFrames: 15, MaxUtteranceS: 15}
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

// Feed pushes one frame and returns a finished utterance, or nil. suppress (the agent is
// speaking, half-duplex) discards accumulation instead of endpointing on our own echo.
func (v *VAD) Feed(frame []int16, suppress bool) []int16 {
	if suppress {
		v.Reset()
		return nil
	}
	loud := RMS(frame) > v.cfg.ThresholdRMS
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
			if v.speechFrames >= v.cfg.StartFrames {
				v.inSpeech = true
				v.silence = 0
			}
		} else {
			v.speechFrames = 0
		}
		return nil
	}
	v.frames = append(v.frames, frame)
	if loud {
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
