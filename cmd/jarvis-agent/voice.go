package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/computer"
	"github.com/alexberardi/jarvis-server/internal/agent/dictate"
	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/nodesettings"
	"github.com/alexberardi/jarvis-server/internal/agent/settings"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// voiceFlags are shared by `run` and `listen`.
type voiceFlags struct {
	input, output string
	models        string
	threshold     float64
	noChimes      bool
	followUp      float64
	followSilence float64
	bargeIn       bool
	// Endpointing (#295): the app's silence_threshold, silence_duration,
	// follow_up_min_record_after_onset_secs and follow_up_min_speech_secs.
	silenceThreshold    float64
	listenSilence       float64
	followMinAfterOnset float64
	followMinSpeech     float64
	debounce            time.Duration
	onsetWait           time.Duration
	preRoll             time.Duration
}

// minWakeDebounce is the shortest --wake-debounce: below it one utterance of the wake word
// can fire twice.
const minWakeDebounce = 500 * time.Millisecond

// wakeDebounce clamps --wake-debounce to minWakeDebounce.
func wakeDebounce(d time.Duration) time.Duration { return max(d, minWakeDebounce) }

func addVoiceFlags(fs *flag.FlagSet) *voiceFlags {
	v := &voiceFlags{}
	fs.StringVar(&v.input, "voice-input", "default", `microphone: "default", an ALSA PCM name (Linux), or wav:FILE`)
	fs.StringVar(&v.output, "voice-output", "default", `speaker: "default", an ALSA PCM name (Linux), or wav:FILE (replies appended)`)
	fs.StringVar(&v.models, "wake-models", "", "openWakeWord models directory (default: <user cache>/jarvis-agent/openwakeword, downloaded on first use)")
	fs.Float64Var(&v.threshold, "wake-threshold", 0.4, "wake word fires above this score (the Python node's wake_word_threshold)")
	fs.BoolVar(&v.noChimes, "no-chimes", false, "don't play the wake and error chimes")
	fs.Float64Var(&v.followUp, "follow-up", voice.DefaultFollowUp().ListenSecs,
		"after a reply, listen this many seconds for a follow-up without the wake word (0: off; at most 3; the Python node's follow_up_listen_seconds)")
	fs.Float64Var(&v.followSilence, "follow-up-silence", voice.DefaultFollowUp().SilenceSecs,
		"seconds of silence that end a follow-up answer (the Python node's follow_up_silence_duration is 0.5)")
	ep, fu := voice.DefaultEndpointing(), voice.DefaultFollowUp()
	fs.Float64Var(&v.silenceThreshold, "silence-threshold", ep.SilenceThreshold,
		"RMS below which a command counts as silence, when the room's level before the wake word isn't known (the Python node's silence_threshold)")
	fs.Float64Var(&v.listenSilence, "listen-silence", ep.SilenceSecs, "seconds of silence that end a command (the Python node's silence_duration)")
	fs.Float64Var(&v.followMinAfterOnset, "follow-up-min-after-onset", fu.MinAfterOnsetSecs,
		"a follow-up answer records at least this many seconds after speech starts (the Python node's follow_up_min_record_after_onset_secs)")
	fs.Float64Var(&v.followMinSpeech, "follow-up-min-speech", fu.MinSpeechSecs,
		"a follow-up answer shorter than this many seconds of speech is noise (the Python node's follow_up_min_speech_secs)")
	fs.BoolVar(&v.bargeIn, "barge-in", voice.DefaultBargeIn().Enabled,
		"stop a reply when \"hey Jarvis\" is said over it (-barge-in=false: off; the Python node's barge_in_enabled)")
	fs.DurationVar(&v.debounce, "wake-debounce", voice.DefaultConfig().Debounce,
		"no second wake this soon after one (at least 500ms)")
	fs.DurationVar(&v.onsetWait, "listen-onset-wait", 0,
		"after the wake word, wait up to this long for speech before silence can end the recording (0: silence counts from the start, as the Python node)")
	fs.DurationVar(&v.preRoll, "listen-preroll", defaultPreRoll,
		"keep up to this much of what the microphone heard while the wake chime played, in front of the command (0: drop it, as the Python node)")
	return v
}

// clampFollowUp caps the follow-up window at voice.MaxFollowUpListenSecs. An agent.json or
// --follow-up from before the cap (8 s was the default) still starts the agent (#296).
func (v *voiceFlags) clampFollowUp(log *slog.Logger) {
	if c := voice.ClampFollowUpListen(v.followUp); c != v.followUp {
		log.Info("follow-up window clamped", "from_s", v.followUp, "to_s", c)
		v.followUp = c
	}
}

// defaultPreRoll covers the wake chime (210 ms) plus the speaker's open and drain latency on a
// Pi: a command said right after "hey Jarvis" starts under the chime (#229). The queue holds
// at most 1.28 s anyway.
const defaultPreRoll = time.Second

// voiceStack is a loaded wake word detector plus the pipeline settings.
type voiceStack struct {
	flags *voiceFlags
	cfg   voice.Config
	det   voice.Detector
	close func()
	// indicator is the on-screen island (run, unless --no-overlay); nil shows nothing.
	indicator voice.Indicator
	// live, when set, retunes and measures the running pipeline (the Pi's node settings).
	live *voice.Live
}

// loadVoice resolves the wake models (fetching them into the cache on first use unless
// --wake-models names a directory), then loads onnxruntime and openWakeWord.
func loadVoice(ctx context.Context, f *voiceFlags, log *slog.Logger) (*voiceStack, error) {
	if !voice.Supported {
		return nil, fmt.Errorf("voice is not supported on %s yet", runtime.GOOS)
	}
	cache, err := voice.CacheDir()
	if err != nil {
		return nil, err
	}
	models := f.models
	if models == "" {
		models = voice.ModelsDir(cache)
		fetched, err := voice.EnsureWakeModels(ctx, models, "", nil)
		if err != nil {
			return nil, err
		}
		if len(fetched) > 0 {
			log.Info("voice: downloaded wake word models", "dir", models, "files", fetched)
		}
	} else if err := voice.CheckWakeModels(models); err != nil {
		return nil, err
	}
	det, closeDet, err := voice.LoadDetector(cache, models)
	if err != nil {
		return nil, fmt.Errorf("wake word: %w", err)
	}
	return &voiceStack{flags: f, cfg: voiceConfig(f), det: det, close: closeDet}, nil
}

// voiceConfig is the pipeline's configuration from the flags (agent.json applied to them).
func voiceConfig(f *voiceFlags) voice.Config {
	cfg := voice.DefaultConfig()
	cfg.WakeThreshold = f.threshold
	cfg.Chimes = !f.noChimes
	cfg.Debounce = wakeDebounce(f.debounce)
	cfg.Endpointing.OnsetWaitSecs = max(0, f.onsetWait.Seconds())
	cfg.Endpointing.PreRollSecs = max(0, f.preRoll.Seconds())
	cfg.FollowUp.ListenSecs = f.followUp
	cfg.BargeIn.Enabled = f.bargeIn
	if f.followSilence > 0 {
		cfg.FollowUp.SilenceSecs = f.followSilence
	}
	if f.silenceThreshold > 0 {
		cfg.Endpointing.SilenceThreshold = f.silenceThreshold
	}
	if f.listenSilence > 0 {
		cfg.Endpointing.SilenceSecs = f.listenSilence
	}
	if f.followMinAfterOnset > 0 {
		cfg.FollowUp.MinAfterOnsetSecs = f.followMinAfterOnset
	}
	if f.followMinSpeech >= 0 {
		cfg.FollowUp.MinSpeechSecs = f.followMinSpeech
	}
	return cfg
}

func (v *voiceStack) options(cc *node.CC, tools node.Toolset, log *slog.Logger, onTurn func(voice.Turn)) voice.Options {
	return voice.Options{Input: v.flags.input, Output: v.flags.output, Config: v.cfg, Detector: v.det,
		Server: voice.NewHTTPServer(cc.BaseURL, cc.Creds), Tools: tools, Log: log, OnTurn: onTurn,
		Indicator: v.indicator, Vocabulary: vocabularyOf(tools), Live: v.live}
}

// vocabularyOf is the app names for speech-to-text from the toolset's dictation tools (their
// windows, launches and catalogue), or nil.
func vocabularyOf(tools node.Toolset) func(ctx context.Context) string {
	m, _ := tools.(node.Multi)
	for _, ts := range m {
		if dt, ok := ts.(*dictate.Tools); ok {
			return dt.Vocabulary
		}
	}
	return nil
}

// --- listen ---

// listen runs only the voice pipeline (no MQTT): for trying a microphone, and for scripted
// end-to-end checks with --voice-input wav:FILE --once. Voice turns get the same client tools
// as `run` unless --no-tools.
func listen(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	cp := fs.String("creds", "", "credentials file")
	once := fs.Bool("once", false, "exit after the first voice turn, follow-ups included (exit status 1 unless a reply was spoken)")
	timeout := fs.Duration("timeout", 0, "give up after this long (0: run until interrupted)")
	noTools := fs.Bool("no-tools", false, "offer voice turns no client tools")
	fake := fs.String("computer", "", `a pretend computer task for the turns, KIND:TEXT ("controlling:Ordering the blue mug"); its status is printed at the end`)
	tf := addToolFlags(fs)
	vf := addVoiceFlags(fs)
	hf := addHatFlags(fs)
	prof := profileFlag(fs)
	_ = fs.Parse(args)
	if err := noStrayArgs(fs); err != nil {
		return err
	}
	p, err := thisProfile(*prof)
	if err != nil {
		return err
	}
	tf.pi = !p.desktop
	activity, err := fakeActivity(*fake)
	if err != nil {
		return err
	}
	path, err := credsPath(*cp)
	if err != nil {
		return err
	}
	c, err := node.LoadCreds(path)
	if err != nil {
		return err
	}
	log := logger()
	hc := &http.Client{Timeout: 10 * time.Second}
	svcs, err := node.Discover(ctx, hc, c.ConfigURL)
	if err != nil {
		return err
	}
	ccURL, err := svcs.Need(node.SvcCommandCenter)
	if err != nil {
		return err
	}
	vf.clampFollowUp(log)
	vs, err := loadVoice(ctx, vf, log)
	if err != nil {
		return fmt.Errorf("voice: %w", err)
	}
	defer vs.close()
	vs.cfg.Greeting = tf.pi
	if hd := openHat(hf, nil, log); hd != nil {
		defer hd.Close()
		vs.addIndicator(&ledCue{leds: hd.leds, log: log})
	}
	var tools node.Toolset
	if !*noTools {
		st, err := nodesettings.OpenStore(filepath.Dir(path), c.NodeID)
		if err != nil {
			return fmt.Errorf("node settings: %w", err)
		}
		_, ts, closeTools, err := tf.build(ctx, log, false, st)
		if err != nil {
			return err
		}
		defer closeTools()
		tf.origins.Add(c.ConfigURL)
		addServerOrigins(tf.origins, svcs)
		tools = nodesettings.Gate{Tools: ts, Store: st}
	}
	if *timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	var last *voice.Turn
	onTurn := func(t voice.Turn) {
		printTurn(t)
		if *once {
			last = &t
			stop()
		}
	}
	o := vs.options(node.NewCC(ccURL, c), tools, log, onTurn)
	if activity != nil {
		o.Computer = activity
	}
	if err := voice.Serve(ctx, o); err != nil {
		return err
	}
	if activity != nil {
		fmt.Println("computer:", activity)
	}
	if *once {
		switch {
		case last == nil:
			return errors.New("no wake word heard")
		case last.Outcome != voice.OutcomeSpoken:
			return fmt.Errorf("turn ended %s", last.Outcome)
		}
	}
	return nil
}

// fakeActivity is listen --computer's pretend task: KIND:TEXT, KIND one of controlling or
// recording. "" is none.
func fakeActivity(s string) (*computer.Activity, error) {
	if s == "" {
		return nil, nil
	}
	k, text, _ := strings.Cut(s, ":")
	switch kind := computer.Kind(k); kind {
	case computer.Controlling, computer.Recording, computer.Paused:
		a := &computer.Activity{}
		a.Set(kind, text)
		return a, nil
	}
	return nil, fmt.Errorf("--computer %q: want controlling:TEXT, recording:TEXT or paused:TEXT", s)
}

func printTurn(t voice.Turn) {
	fmt.Printf("turn %s: wake %.3f, recorded %.2f s (pre-roll %.2f s, silence threshold %.0f), ",
		t.ConversationID, t.WakeScore, t.RecordedSecs, t.PreRollSecs, t.Threshold)
	printExchange(t.Exchange)
	for i, f := range t.FollowUps {
		fmt.Printf("  follow-up %d: recorded %.2f s, ", i+1, f.RecordedSecs)
		printExchange(f)
	}
	if t.FollowUpEnd != "" {
		fmt.Printf("  follow-up window: %s\n", t.FollowUpEnd)
	}
	fmt.Printf("  took %s\n", t.Took.Round(time.Millisecond))
}

func printExchange(e voice.Exchange) {
	fmt.Printf("heard %q", e.Transcript)
	for _, v := range e.Validations {
		fmt.Printf(", asked %q and heard %q", v.Question, v.Answer)
	}
	for _, r := range e.Tools {
		args, _ := json.Marshal(r.Args)
		fmt.Printf(", ran %s%s", r.Name, args)
		if !r.Success {
			fmt.Printf(" (failed: %s)", r.Error)
		}
	}
	fmt.Printf(" -> %s", e.Outcome)
	if e.Message != "" {
		fmt.Printf(", said %q (%.2f s of audio)", e.Message, e.ReplySecs)
	}
	if e.Err != nil {
		fmt.Printf(", error: %v", e.Err)
	}
	fmt.Println()
}

// voiceStatus is the `status` line: whether this build/OS can do voice, whether listening is
// on (agent.json's voice, set by the status icon's toggle), and whether the models are in place.
func voiceStatus() string {
	if !voice.Supported {
		return fmt.Sprintf("not supported on %s yet", runtime.GOOS)
	}
	cache, err := voice.CacheDir()
	if err != nil {
		return err.Error()
	}
	dir := voice.ModelsDir(cache)
	models := "wake models ready"
	if err := voice.CheckWakeModels(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			models = "wake models not downloaded yet (fetched when listening first starts)"
		} else {
			models = err.Error()
		}
	}
	return fmt.Sprintf("%s (%s audio; %s in %s)", listeningState(voiceSetting()), device.Backend(), models, dir)
}

// voiceSetting is whether run listens: agent.json's voice, else the default.
func voiceSetting() bool {
	on := voiceByDefault
	if p, err := settings.DefaultPath(); err == nil {
		if s, _, err := settings.Load(p); err == nil && s.Voice != nil {
			on = *s.Voice
		}
	}
	return on
}

func listeningState(on bool) string {
	if on {
		return `listening for "Hey Jarvis"`
	}
	return `not listening: turn on "Listen for \"Hey Jarvis\"" in the Jarvis Automation menu`
}
