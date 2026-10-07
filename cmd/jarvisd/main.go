// Command jarvisd is the single-binary Jarvis server.
//
// See docs/PLAN.md for the architecture and docs/STATUS.md for current progress.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	llmmod "github.com/alexberardi/jarvis-server/internal/modules/llm"
	logsmod "github.com/alexberardi/jarvis-server/internal/modules/logs"
	notifmod "github.com/alexberardi/jarvis-server/internal/modules/notifications"
	ocrmod "github.com/alexberardi/jarvis-server/internal/modules/ocr"
	sttmod "github.com/alexberardi/jarvis-server/internal/modules/stt"
	ttsmod "github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/logging"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/mqtt"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

var version = "dev"

// modules lists every module jarvisd serves. Modules are added here as they are ported.
func modules() []module.Module {
	mods := []module.Module{
		&configmod.Module{
			AdminToken: os.Getenv("JARVIS_CONFIG_ADMIN_TOKEN"),
			Advertise:  os.Getenv("JARVIS_MDNS") != "0",
		},
		&logsmod.Module{},
		&ocrmod.Module{},
		&llmmod.Module{},
		&ttsmod.Module{},
		&sttmod.Module{},
		&notifmod.Module{
			AdminKey: os.Getenv("ADMIN_API_KEY"),
			RelayURL: os.Getenv("RELAY_URL"),
			// Normally empty: the relay token is registered per household.
			RelayHouseholdJWT: os.Getenv("RELAY_HOUSEHOLD_JWT"),
		},
		&ccmod.Module{
			AdminKey: os.Getenv("ADMIN_API_KEY"),
			MQTT: ccmod.MQTTOptions{
				TCPAddr:        envOr("JARVIS_MQTT_ADDR", mqtt.DefaultTCPAddr),
				WSAddr:         envOr("JARVIS_MQTT_WS_ADDR", mqtt.DefaultWSAddr),
				AllowAnonymous: os.Getenv("JARVIS_MQTT_ALLOW_ANONYMOUS") == "1",
			},
		},
		&authmod.Module{
			AdminToken: os.Getenv("JARVIS_AUTH_ADMIN_TOKEN"),
			// Legacy HS256 secret: HS256 is minted (auth.algorithm=HS256) and verified only when set.
			HMACSecret: os.Getenv("AUTH_SECRET_KEY"),
		},
	}
	// The registry lists exactly the listeners jarvisd serves; account deletion skips the
	// legacy HTTP purge for the same services.
	var served, names []string
	for _, m := range mods {
		served = append(served, m.Listener())
		names = append(names, configmod.ServiceNames[m.Listener()])
	}
	var auth *authmod.Module
	var llm *llmmod.Module
	var sttm *sttmod.Module
	var ttsm *ttsmod.Module
	var notif *notifmod.Module
	for _, m := range mods {
		switch x := m.(type) {
		case *authmod.Module:
			auth = x
		case *llmmod.Module:
			llm = x
		case *sttmod.Module:
			sttm = x
		case *ttsmod.Module:
			ttsm = x
		case *notifmod.Module:
			notif = x
		}
	}
	superuser := settings.SuperuserGuard(auth.VerifyUser)
	for _, m := range mods {
		switch c := m.(type) {
		case *configmod.Module:
			c.Served = served
			c.SettingsGuard = superuser
			c.MQTTPort = portOf(envOr("JARVIS_MQTT_ADDR", mqtt.DefaultTCPAddr))
		case *authmod.Module:
			c.InProcess = names
		case *notifmod.Module:
			c.Auth = auth
			c.Users = auth
			auth.OnUserDeleted(c.PurgeUser)
		case *ocrmod.Module:
			c.Auth = auth
			c.SettingsRead = settings.CombinedGuard(auth.VerifyUser, auth.ValidateApp)
			c.SettingsWrite = superuser
			c.Version = version
			// During the strangler phase LLM vision goes to the legacy llm-proxy; jarvisd's own
			// app credentials (legacy names) sign outbound calls and job-completion callbacks.
			c.AppID, c.AppKey = os.Getenv("JARVIS_APP_ID"), os.Getenv("JARVIS_APP_KEY")
			// LLM vision and validation go to jarvisd's own llm module, in memory.
			c.LLMURL, c.LLMAppID, c.LLMAppKey = llmmod.InProcessBaseURL, "jarvisd", "in-process"
			c.LLMClient = llm.InProcessClient()
			c.AppleVisionURL, c.AppleVisionKey = os.Getenv("JARVIS_OSX_API_URL"), os.Getenv("JARVIS_OSX_API_KEY")
		case *llmmod.Module:
			c.Auth = auth
			c.SettingsRead = settings.CombinedGuard(auth.VerifyUser, auth.ValidateApp)
			c.SettingsWrite = superuser
			c.ManagerGuard = superuser
			c.Version = version
			c.AppID, c.AppKey = os.Getenv("JARVIS_APP_ID"), os.Getenv("JARVIS_APP_KEY")
		case *ttsmod.Module:
			c.Auth = auth
			c.SettingsRead = settings.CombinedGuard(auth.VerifyUser, auth.ValidateApp)
			c.SettingsWrite = superuser
			c.Version = version
			c.Models = ttsmod.ModelPathFunc(llm.ModelPath)
		case *sttmod.Module:
			c.Auth = auth
			c.SettingsRead = settings.CombinedGuard(auth.VerifyUser, auth.ValidateApp)
			c.SettingsWrite = superuser
			c.Version = version
			c.Engine = sttmod.ResolveFunc(func(ctx context.Context, label string) (string, error) {
				ep, err := llm.Resolver.Resolve(ctx, label)
				return ep.BaseURL, err
			})
			c.Models = sttmod.ModelPathFunc(llm.ModelPath)
			auth.OnUserDeleted(c.PurgeUser)
		case *ccmod.Module:
			c.Auth = auth
			c.Users = auth
			c.Nodes = auth
			c.SettingsRead = settings.CombinedGuard(auth.VerifyUser, auth.ValidateApp)
			c.SettingsWrite = superuser
			c.Version = version
			// The voice pipeline calls the other modules in process (5b).
			c.LLM = llm.Service()
			c.STT = sttm
			c.TTS = ccmod.TTSFrom(ttsm)
			c.Notify = notif
			c.Names = auth
			auth.OnUserDeleted(c.PurgeUser)
		case *logsmod.Module:
			c.Auth = auth
			c.SettingsRead = settings.CombinedGuard(auth.VerifyUser, auth.ValidateApp)
			c.SettingsWrite = superuser
		}
	}
	return mods
}

// envOr returns the variable's value when it is set, even to "" (which disables a listener),
// and def when it is unset.
func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}

// portOf is the port of a listen address like ":1884", or 0.
func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

const usage = `usage: jarvisd <command>

commands:
  serve            run the server
  migrate status   show each module's migration state
  version          print the version
`

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "jarvisd:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return errors.New("no command")
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, version)
		return nil
	case "serve":
		return serve(ctx)
	case "migrate":
		if len(args) < 2 || args[1] != "status" {
			return errors.New("usage: jarvisd migrate status")
		}
		return migrateStatus(ctx, stdout)
	default:
		fmt.Fprint(stdout, usage)
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func newLogger() *slog.Logger {
	return logging.New(os.Stderr, logging.ParseLevel(os.Getenv("JARVIS_LOG_LEVEL")), nil)
}

func openDeps(ctx context.Context) (module.Deps, error) {
	cfg, err := config.Load()
	if err != nil {
		return module.Deps{}, err
	}
	d, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return module.Deps{}, err
	}
	log := newLogger()
	blobs, err := blob.NewFS(filepath.Join(cfg.Home, "blobs"), log)
	if err != nil {
		d.Close()
		return module.Deps{}, err
	}
	q := queue.New(d, log)
	return module.Deps{
		Config: cfg, DB: d, Log: log,
		Queue: q, Scheduler: scheduler.New(d, q, log), Blobs: blobs,
	}, nil
}

func serve(ctx context.Context) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	deps, err := openDeps(ctx)
	if err != nil {
		return err
	}
	defer deps.DB.Close()
	mods := modules()
	// jarvisd's own records go to stderr and, once migrated, into the logs module's store.
	for _, m := range mods {
		if l, ok := m.(*logsmod.Module); ok {
			shipper := logging.NewShipper(l.Sink(), 4096, 200, 2*time.Second)
			defer shipper.Close()
			deps.Log = logging.New(os.Stderr, logging.ParseLevel(os.Getenv("JARVIS_LOG_LEVEL")), shipper)
			deps.Queue = queue.New(deps.DB, deps.Log)
			deps.Scheduler = scheduler.New(deps.DB, deps.Queue, deps.Log)
		}
	}
	deps.Log.Info("starting jarvisd", "version", version, "home", deps.Config.Home)
	return (&module.Runner{Deps: deps, Modules: mods}).Run(ctx)
}

func migrateStatus(ctx context.Context, stdout io.Writer) error {
	deps, err := openDeps(ctx)
	if err != nil {
		return err
	}
	defer deps.DB.Close()
	fmt.Fprintf(stdout, "%-20s %8s %8s %8s\n", "MODULE", "CURRENT", "LATEST", "PENDING")
	for _, m := range modules() {
		fsys := m.Migrations()
		if fsys == nil {
			continue
		}
		st, err := db.Status(ctx, deps.DB, m.Name(), fsys)
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%-20s %8d %8d %8d\n", st.Module, st.Current, st.Latest, st.Pending)
	}
	return nil
}
