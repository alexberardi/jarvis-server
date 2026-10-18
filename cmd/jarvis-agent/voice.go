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
	"time"

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
	debounce      time.Duration
	onsetWait     time.Duration
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
		"after a reply, listen this many seconds for a follow-up without the wake word (0: off; the Python node's follow_up_listen_seconds)")
	fs.Float64Var(&v.followSilence, "follow-up-silence", voice.DefaultFollowUp().SilenceSecs,
		"seconds of silence that end a follow-up answer (the Python node's follow_up_silence_duration is 0.5)")
	fs.BoolVar(&v.bargeIn, "barge-in", voice.DefaultBargeIn().Enabled,
		"stop a reply when \"hey Jarvis\" is said over it (-barge-in=false: off; the Python node's barge_in_enabled)")
	fs.DurationVar(&v.debounce, "wake-debounce", voice.DefaultConfig().Debounce,
		"no second wake this soon after one (at least 500ms)")
	fs.DurationVar(&v.onsetWait, "listen-onset-wait", 0,
		"after the wake word, wait up to this long for speech before silence can end the recording (0: silence counts from the start, as the Python node)")
	return v
}

// voiceStack is a loaded wake word detector plus the pipeline settings.
type voiceStack struct {
	flags *voiceFlags
	cfg   voice.Config
	det   voice.Detector
	close func()
	// indicator is the on-screen island (run, unless --no-overlay); nil shows nothing.
	indicator voice.Indicator
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
	cfg := voice.DefaultConfig()
	cfg.WakeThreshold = f.threshold
	cfg.Chimes = !f.noChimes
	cfg.Debounce = wakeDebounce(f.debounce)
	cfg.Endpointing.OnsetWaitSecs = max(0, f.onsetWait.Seconds())
	cfg.FollowUp.ListenSecs = f.followUp
	cfg.BargeIn.Enabled = f.bargeIn
	if f.followSilence > 0 {
		cfg.FollowUp.SilenceSecs = f.followSilence
	}
	return &voiceStack{flags: f, cfg: cfg, det: det, close: closeDet}, nil
}

func (v *voiceStack) options(cc *node.CC, tools node.Toolset, log *slog.Logger, onTurn func(voice.Turn)) voice.Options {
	return voice.Options{Input: v.flags.input, Output: v.flags.output, Config: v.cfg, Detector: v.det,
		Server: voice.NewHTTPServer(cc.BaseURL, cc.Creds), Tools: tools, Log: log, OnTurn: onTurn,
		Indicator: v.indicator, Vocabulary: vocabularyOf(tools)}
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
	vs, err := loadVoice(ctx, vf, log)
	if err != nil {
		return fmt.Errorf("voice: %w", err)
	}
	defer vs.close()
	if hd := openHat(hf, log); hd != nil {
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
	if err := voice.Serve(ctx, vs.options(node.NewCC(ccURL, c), tools, log, onTurn)); err != nil {
		return err
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

func printTurn(t voice.Turn) {
	fmt.Printf("turn %s: wake %.3f, recorded %.2f s (silence threshold %.0f), ",
		t.ConversationID, t.WakeScore, t.RecordedSecs, t.Threshold)
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
