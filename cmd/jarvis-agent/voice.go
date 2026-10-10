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
	"runtime"
	"time"

	"github.com/alexberardi/jarvis-server/internal/agent/node"
	"github.com/alexberardi/jarvis-server/internal/agent/voice"
	"github.com/alexberardi/jarvis-server/internal/audio/device"
)

// voiceFlags are shared by `run --voice` and `listen`.
type voiceFlags struct {
	input, output string
	models        string
	threshold     float64
	noChimes      bool
}

func addVoiceFlags(fs *flag.FlagSet) *voiceFlags {
	v := &voiceFlags{}
	fs.StringVar(&v.input, "voice-input", "default", `microphone: "default", an ALSA PCM name (Linux), or wav:FILE`)
	fs.StringVar(&v.output, "voice-output", "default", `speaker: "default", an ALSA PCM name (Linux), or wav:FILE (replies appended)`)
	fs.StringVar(&v.models, "wake-models", "", "openWakeWord models directory (default: <user cache>/jarvis-agent/openwakeword, downloaded on first use)")
	fs.Float64Var(&v.threshold, "wake-threshold", 0.4, "wake word fires above this score (the Python node's wake_word_threshold)")
	fs.BoolVar(&v.noChimes, "no-chimes", false, "don't play the wake and error chimes")
	return v
}

// voiceStack is a loaded wake word detector plus the pipeline settings.
type voiceStack struct {
	flags *voiceFlags
	cfg   voice.Config
	det   voice.Detector
	close func()
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
	return &voiceStack{flags: f, cfg: cfg, det: det, close: closeDet}, nil
}

func (v *voiceStack) options(cc *node.CC, tools node.Toolset, log *slog.Logger, onTurn func(voice.Turn)) voice.Options {
	return voice.Options{Input: v.flags.input, Output: v.flags.output, Config: v.cfg, Detector: v.det,
		Server: voice.NewHTTPServer(cc.BaseURL, cc.Creds), Tools: tools, Log: log, OnTurn: onTurn}
}

// --- listen ---

// listen runs only the voice pipeline (no MQTT): for trying a microphone, and for scripted
// end-to-end checks with --voice-input wav:FILE --once. Voice turns get the same client tools
// as `run` unless --no-tools.
func listen(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("listen", flag.ExitOnError)
	cp := fs.String("creds", "", "credentials file")
	once := fs.Bool("once", false, "exit after the first voice turn (exit status 1 unless a reply was spoken)")
	timeout := fs.Duration("timeout", 0, "give up after this long (0: run until interrupted)")
	noTools := fs.Bool("no-tools", false, "offer voice turns no client tools")
	tf := addToolFlags(fs)
	vf := addVoiceFlags(fs)
	_ = fs.Parse(args)
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
	var tools node.Toolset
	if !*noTools {
		_, ts, closeTools, err := tf.build(ctx, log, false)
		if err != nil {
			return err
		}
		defer closeTools()
		tools = ts
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
	fmt.Printf("turn %s: wake %.3f, recorded %.2f s (silence threshold %.0f), heard %q",
		t.ConversationID, t.WakeScore, t.RecordedSecs, t.Threshold, t.Transcript)
	for _, r := range t.Tools {
		args, _ := json.Marshal(r.Args)
		fmt.Printf(", ran %s%s", r.Name, args)
		if !r.Success {
			fmt.Printf(" (failed: %s)", r.Error)
		}
	}
	fmt.Printf(" -> %s", t.Outcome)
	if t.Message != "" {
		fmt.Printf(", said %q (%.2f s of audio)", t.Message, t.ReplySecs)
	}
	if t.Err != nil {
		fmt.Printf(", error: %v", t.Err)
	}
	fmt.Printf(", %s\n", t.Took.Round(time.Millisecond))
}

// voiceStatus is the `status` line: whether this build/OS can do voice and whether the
// models are in place.
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
			models = "wake models not downloaded yet (fetched on first `run --voice`)"
		} else {
			models = err.Error()
		}
	}
	return fmt.Sprintf("available (%s audio; %s in %s); off unless run with --voice", device.Backend(), models, dir)
}
