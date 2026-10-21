// Command jarvisd is the single-binary Jarvis server.
//
// See docs/PLAN.md for the architecture and docs/STATUS.md for current progress.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/alexberardi/jarvis-server/internal/buildinfo"
	"github.com/alexberardi/jarvis-server/internal/doctor"
	adminmod "github.com/alexberardi/jarvis-server/internal/modules/admin"
	authmod "github.com/alexberardi/jarvis-server/internal/modules/auth"
	ccmod "github.com/alexberardi/jarvis-server/internal/modules/cc"
	configmod "github.com/alexberardi/jarvis-server/internal/modules/config"
	llmmod "github.com/alexberardi/jarvis-server/internal/modules/llm"
	logsmod "github.com/alexberardi/jarvis-server/internal/modules/logs"
	notifmod "github.com/alexberardi/jarvis-server/internal/modules/notifications"
	ocrmod "github.com/alexberardi/jarvis-server/internal/modules/ocr"
	recipesmod "github.com/alexberardi/jarvis-server/internal/modules/recipes"
	sttmod "github.com/alexberardi/jarvis-server/internal/modules/stt"
	ttsmod "github.com/alexberardi/jarvis-server/internal/modules/tts"
	"github.com/alexberardi/jarvis-server/internal/platform/blob"
	"github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/db"
	"github.com/alexberardi/jarvis-server/internal/platform/logging"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/service"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
	"github.com/alexberardi/jarvis-server/internal/update"
)

var version = "dev"

// crashForTest, set only by test builds (-ldflags "-X main.crashForTest=1"), makes serve fail
// right after counting its start: the CI upgrade job's broken release, which must roll back.
var crashForTest string

// modules lists every module jarvisd serves, wired from env (cfg.Env). Modules are added here as
// they are ported. The slice's order is the listeners', the migrations' and the start order.
func modules(env config.Env) []module.Module {
	cfgm := &configmod.Module{AdminToken: env.ConfigAdminToken, Advertise: env.MDNS}
	logs := &logsmod.Module{}
	ocrm := &ocrmod.Module{}
	llm := &llmmod.Module{}
	ttsm := &ttsmod.Module{}
	sttm := &sttmod.Module{}
	notif := &notifmod.Module{
		AdminKey: env.AdminAPIKey,
		// The relay is the notifications relay.url setting (env fallback RELAY_URL; ID8).
		// Normally empty: the relay token is registered per household.
		RelayHouseholdJWT: env.RelayHouseholdJWT,
	}
	// The household recipe box on 7030 (docs/recipes; jarvis-recipes-mobile's server).
	recipes := &recipesmod.Module{}
	cc := &ccmod.Module{
		AdminKey:        env.AdminAPIKey,
		DevSpeakerHints: env.DevSpeakerHints,
		MQTT: ccmod.MQTTOptions{
			TCPAddr:        env.MQTTAddr,
			WSAddr:         env.MQTTWSAddr,
			AllowAnonymous: env.MQTTAllowAnonymous,
			AllowedOrigins: mqttAllowedOrigins(env.MQTTWSAllowedOrigins),
		},
		Phone: phoneConfig(env), // cmd/jarvisd/phone.go
	}
	auth := &authmod.Module{
		AdminToken: env.AuthAdminToken,
		// Legacy HS256 secret: HS256 is minted (auth.algorithm=HS256) and verified only when set.
		HMACSecret: env.AuthSecretKey,
	}
	// The admin SPA on 7710. JARVIS_ADMIN_UI_DIR serves a built UI from disk instead of
	// the embedded one (testing a rebuilt SPA without rebuilding jarvisd).
	admin := &adminmod.Module{UIDir: env.AdminUIDir, Version: version}
	mods := []module.Module{cfgm, logs, ocrm, llm, ttsm, sttm, notif, recipes, cc, auth, admin}

	// The registry lists exactly the listeners jarvisd serves; account deletion skips the
	// legacy HTTP purge for the same services.
	var served, names []string
	for _, m := range mods {
		served = append(served, m.Listener())
		names = append(names, configmod.ServiceNames[m.Listener()])
	}
	superuser := settings.SuperuserGuard(auth.VerifyUser)
	settingsRead := func() settings.Guard { return settings.CombinedGuard(auth.VerifyUser, auth.ValidateApp) }

	cfgm.Served = served
	cfgm.SettingsGuard = superuser
	cfgm.MQTTPort = doctor.PortOf(env.MQTTAddr)
	cfgm.External = func(ctx context.Context) map[string]string {
		// The system-level Pantry; households can override it for their own installs.
		return map[string]string{"jarvis-pantry": cc.PantryBaseURL(ctx, "")}
	}

	logs.Auth = auth
	logs.SettingsRead = settingsRead()
	logs.SettingsWrite = superuser

	ocrm.Auth = auth
	ocrm.SettingsRead = settingsRead()
	ocrm.SettingsWrite = superuser
	ocrm.Version = version
	// jarvisd's app credentials (legacy env names) sign job-completion callbacks.
	ocrm.AppID, ocrm.AppKey = env.AppID, env.AppKey
	// Without them (every fresh install), jarvisd signs with its own app client.
	ocrm.AppCreds = auth.SelfAppCreds
	ocrm.LLM = llm.Service() // LLM vision and validation
	ocrm.AppleVisionURL, ocrm.AppleVisionKey = env.OSXAPIURL, env.OSXAPIKey

	llm.Auth = auth
	llm.SettingsRead = settingsRead()
	llm.SettingsWrite = superuser
	llm.ManagerGuard = superuser
	llm.Version = version
	llm.AppID, llm.AppKey = env.AppID, env.AppKey
	llm.AppCreds = auth.SelfAppCreds

	ttsm.Auth = auth
	ttsm.SettingsRead = settingsRead()
	ttsm.SettingsWrite = superuser
	ttsm.Version = version
	ttsm.Models = ttsmod.ModelPathFunc(llm.ModelPath)

	sttm.Auth = auth
	sttm.SettingsRead = settingsRead()
	sttm.SettingsWrite = superuser
	sttm.Version = version
	sttm.Engine = sttmod.ResolveFunc(func(ctx context.Context, label string) (string, error) {
		ep, err := llm.Resolver.Resolve(ctx, label)
		return ep.BaseURL, err
	})
	sttm.Models = sttmod.ModelPathFunc(llm.ModelPath)
	auth.OnUserDeleted(sttm.PurgeUser)
	auth.OnMemberRemoved(sttm.PurgeUserHousehold)
	auth.OnHouseholdDeleted(sttm.PurgeHousehold)

	notif.Auth = auth
	notif.Users = auth
	notif.SettingsRead = settingsRead()
	notif.SettingsWrite = superuser
	auth.OnUserDeleted(notif.PurgeUser)
	auth.OnMemberRemoved(notif.PurgeUserHousehold)
	auth.OnHouseholdDeleted(notif.PurgeHousehold)

	recipes.Users = auth
	recipes.Households = auth
	recipes.Clock = cc // "today" for the planner is the household's date
	recipes.LLM = llm.Service()
	recipes.OCR = ocrm     // photo import runs every available engine in process (§7.3)
	recipes.Notify = notif // a chat photo import's outcome: inbox item + push
	recipes.SettingsRead = settingsRead()
	recipes.SettingsWrite = superuser
	// RD4: the household keeps shared rows; private rows, jobs and imports go.
	auth.OnUserDeleted(recipes.PurgeUser)
	auth.OnMemberRemoved(recipes.PurgeUserHousehold)
	auth.OnHouseholdDeleted(recipes.PurgeHousehold)

	cc.Auth = auth
	cc.Users = auth
	cc.Nodes = auth
	cc.SettingsRead = settingsRead()
	cc.SettingsWrite = superuser
	cc.Version = version
	// The voice pipeline calls the other modules in process (5b).
	cc.LLM = ccmod.WaitingLLM(llm.Service(), ccmod.ReadyWait)
	cc.STT = sttm
	cc.TTS = ccmod.TTSFrom(ttsm)
	cc.Notify = notif
	cc.Names = auth
	// Chat photo → recipe (save_recipe_from_image): the recipes photo import, in process.
	cc.Recipes = recipes
	// Tool-result images (desktop_read_screen) as text when live has no vision.
	cc.OCR = ocrm
	// With llm.prompt_provider unset, the live model's catalog entry names it, so a
	// fresh install answers its first voice turn without anyone choosing one.
	cc.DefaultPromptProvider = llm.LivePromptProvider
	// The live model's tool calling and thinking switch, for llm.tool_calling/thinking_switch auto.
	cc.LiveModelTraits = llm.LiveModelTraits
	// Slot capabilities: vision for chat images, context size for compaction.
	cc.Endpoints = func(ctx context.Context, label string) (llmmod.Endpoint, error) {
		if llm.Resolver == nil {
			return llmmod.Endpoint{}, errors.New("no llm resolver")
		}
		return llm.Resolver.Resolve(ctx, label)
	}
	auth.OnUserDeleted(cc.PurgeUser)
	auth.OnMemberRemoved(cc.PurgeUserHousehold)
	auth.OnHouseholdDeleted(cc.PurgeHousehold)

	auth.InProcess = names

	admin.Verify = auth.VerifyUser
	// The BFF calls the modules in process (A3).
	for _, s := range mods {
		if src, ok := s.(adminmod.SettingsSource); ok {
			admin.SettingsSources = append(admin.SettingsSources, src)
		}
	}
	admin.Traces, admin.Prompts = cc, cc
	admin.Accounts, admin.Households = auth, auth
	admin.Models = llm
	admin.TTS = ttsm // the wizard's voice sample (AD3b)
	admin.Exposure = exposure(served, env)
	// A4: logs (AD9), connections (AD7: registry + app clients).
	admin.Logs, admin.Registry, admin.Apps = logs, cfgm, auth
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

// mqttAllowedOrigins splits JARVIS_MQTT_WS_ALLOWED_ORIGINS (config.Env.MQTTWSAllowedOrigins):
// comma-separated hosts (or URLs) whose pages may open the broker's WebSocket besides this
// host, localhost and the LAN.
func mqttAllowedOrigins(v string) []string {
	var out []string
	for _, h := range strings.Split(v, ",") {
		if h = strings.TrimSpace(h); h != "" {
			out = append(out, h)
		}
	}
	return out
}

const usage = `usage: jarvisd <command> [--home DIR]

commands:
  serve [--no-browser] [--allow-downgrade] [--port-offset N]
                   run the server; on first start it prints the admin setup link and, at a
                   desktop, opens it (--no-browser or JARVIS_NO_BROWSER=1 to not).
                   --allow-downgrade runs on a database a newer jarvisd migrated (experts)
                   --port-offset N adds N to every default port (JARVIS_PORT_OFFSET), to run
                   a second instance beside the installed one: 10000 puts CC on 17703
  upgrade [--version vX.Y.Z [--allow-older]] [--check] [--rollback] [--bin PATH] [--user]
                   install a signed release: verify, snapshot the database, swap the binary
                   (previous kept as jarvisd.prev), restart the service, roll back if the new
                   version doesn't come up healthy. JARVISD_RELEASE_BASE=URL|DIR reads a flat
                   release directory (SHA256SUMS, its .minisig, the archives) instead of GitHub
  service install [--user] [--bin PATH] [--run-as USER] [--no-start]
  service uninstall [--user] [--purge [--yes]] [--keep-firewall]
                   remove the service and the firewall rules doctor --fix added; --purge
                   also deletes the data directory, env file and service account
  service start|stop|restart [--user]
  service status [--user] [--json] [--wait DURATION]
                   register and control jarvisd with systemd, launchd or the Windows SCM
  setup-link       print the first-run setup link and token (or the admin URL once set up)
                   [--url] the URL alone (for installers); [--qr] also as a QR code
  install-from-app [--run-as USER] [--bin PATH] [--no-firewall] [--url-file FILE] [--qr] [--open] [--allow-no-service]
                   the GUI installers' privileged step: put this binary where the service runs it,
                   then service install, doctor --fix (unless --no-firewall) and print the URL to
                   open (needs root / an Administrator prompt); --open (Linux) opens the setup
                   link in the browser of whoever is at the screen
  migrate status   show each module's migration state
  import-recipes [--apply] [--household OLD=NEW]... [--park-unmatched] [--legacy-host HOST]... BUNDLE
                   import recipes, meal plans, staples and SKU mappings from a legacy export
                   (scripts/legacy/recipes-export.sh), owned by the accounts whose emails match;
                   a dry run unless --apply; re-run after more people sign up
  import-legacy (--compose DIR | --from postgres://USER:PASS@HOST:PORT) [--apply] [--accept-head DB=HEAD]...
                   carry a legacy install's users, households, nodes, rooms, devices, settings,
                   memories, routines, contacts and inbox over, keeping ids; reads the legacy
                   Postgres read-only (credentials from the compose dir's env files); runs once,
                   with jarvisd stopped, before the setup wizard; a dry run unless --apply
  doctor [--json] [--fix]
                   check that nodes and phones can reach jarvisd (listeners, ports held by
                   another program, host firewall, data permissions, legacy stack); --fix
                   applies the firewall fix for private LAN subnets (needs sudo/Administrator)
  version          print the version

--home DIR picks the data directory; otherwise JARVIS_HOME, else the installed service's,
else ~/.jarvisd. Variables in <home>/jarvisd.env (and /etc/jarvisd/jarvisd.env on Linux/macOS)
apply when not already set in the environment.
`

func main() {
	args := os.Args[1:]
	if service.IsWindowsService() {
		os.Exit(runWindowsService(args))
	}
	err := run(context.Background(), args, os.Stdout)
	if err != nil && !service.Requested(err) {
		fmt.Fprintln(os.Stderr, "jarvisd:", err)
	}
	os.Exit(service.ExitCode(err))
}

// runWindowsService is main under the Windows SCM: stderr goes to <home>\logs\jarvisd.log
// (it goes nowhere otherwise), and Stop/Shutdown cancel serve's context.
func runWindowsService(args []string) int {
	flagHome, rest := takeHome(args)
	if helperArgs(rest) {
		return runWindowsHelper(flagHome, rest)
	}
	if home, err := config.ResolveHome(flagHome, "", os.Getenv); err == nil {
		_ = service.RedirectStderr(service.LogPath(home))
	}
	err := service.RunWindowsService(func(ctx context.Context) error { return run(ctx, args, os.Stderr) })
	if err != nil && !service.Requested(err) {
		fmt.Fprintln(os.Stderr, "jarvisd:", err)
	}
	return service.ExitCode(err)
}

func run(ctx context.Context, args []string, stdout io.Writer) error {
	flagHome, args := takeHome(args)
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return errors.New("no command")
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, buildinfo.Version(version))
		return nil
	case "help", "-h", "--help":
		// The install scripts read the command list to see what this binary supports.
		fmt.Fprint(stdout, usage)
		return nil
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ContinueOnError)
		fs.SetOutput(stdout)
		noBrowser := fs.Bool("no-browser", false, "don't open the setup link in a browser")
		allowDowngrade := fs.Bool("allow-downgrade", false, "start even though a newer jarvisd migrated the database")
		portOffset := fs.String("port-offset", "", "add this to every default port (JARVIS_PORT_OFFSET)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *portOffset != "" {
			// Through the environment, so the listeners, the broker and the env file agree.
			if err := os.Setenv("JARVIS_PORT_OFFSET", *portOffset); err != nil {
				return err
			}
		}
		// serve refuses to start on an env file it can't read; the CLI commands only warn.
		if err := bootstrap(flagHome, true, os.Stderr); err != nil {
			return err
		}
		v := os.Getenv("JARVIS_NO_BROWSER")
		return serve(ctx, !*noBrowser && (v == "" || v == "0"), *allowDowngrade)
	case "upgrade":
		return runUpgrade(ctx, flagHome, args[1:], stdout)
	case "service":
		return runService(ctx, flagHome, args[1:], stdout)
	case "doctor":
		envErr, err := bootstrapEnv(flagHome)
		if err != nil {
			return err
		}
		return runDoctor(ctx, envErr, args[1:], stdout, os.Stderr)
	case "install-from-app":
		return runInstallFromApp(ctx, flagHome, args[1:], stdout, os.Stderr)
	case "setup-link":
		opt, err := parseSetupLinkArgs(args[1:], os.Stderr)
		if err != nil {
			return err
		}
		opt.qrStyle = qrStyleFor(stdout)
		if err := bootstrap(flagHome, false, os.Stderr); err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		return printSetupLink(cfg, stdout, opt)
	case "import-recipes":
		if err := bootstrap(flagHome, false, os.Stderr); err != nil {
			return err
		}
		return runImportRecipes(ctx, args[1:], stdout)
	case "import-legacy":
		if err := bootstrap(flagHome, false, os.Stderr); err != nil {
			return err
		}
		return runImportLegacy(ctx, args[1:], stdout)
	case "migrate":
		if len(args) < 2 || args[1] != "status" {
			return errors.New("usage: jarvisd migrate status")
		}
		if err := bootstrap(flagHome, false, os.Stderr); err != nil {
			return err
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
	if err := secureHome(cfg.Home); err != nil {
		return module.Deps{}, err
	}
	d, err := db.Open(ctx, cfg.DBPath())
	if err != nil {
		return module.Deps{}, err
	}
	log := newLogger()
	// JARVIS_BLOB_STORE picks the backend by URL (default: files under <home>/blobs).
	blobs, err := blob.Open(envOr("JARVIS_BLOB_STORE", filepath.Join(cfg.Home, "blobs")), log)
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

func serve(ctx context.Context, browser, allowDowngrade bool) error {
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Ending serve with a restart request makes the supervisor start jarvisd again: the admin
	// restart button (AD8) and self-update (AD5). A stop request (the admin Stop button, AD8b)
	// exits with a code the supervisor leaves alone.
	restarter := service.NewRestarter(service.Detect(), cancel)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	exe, err := selfExe()
	if err != nil {
		return err
	}
	upaths := update.Paths{Home: cfg.Home, Exe: exe}
	helper := service.DetectHelper()
	// A pending upgrade step (swap, rollback) and the start count run before the database
	// opens (ID10).
	gate, restart, err := upgradeStart(ctx, newLogger(), upaths, restarter.Kind().Supervised(), helper)
	if err != nil {
		return err
	}
	if restart {
		return service.ErrRestart
	}
	if crashForTest != "" {
		return errors.New("crashing on purpose (test build)")
	}
	deps, err := openDeps(ctx)
	if err != nil {
		return err
	}
	dbOpen := true
	// The shipper writes into the DB: close it first, on every path that closes the DB
	// (rollbackAfterGate closes it before the deferred calls run).
	var shipper *logging.Shipper
	closeDB := func() {
		if shipper != nil {
			shipper.Close()
		}
		if dbOpen {
			deps.DB.Close()
			dbOpen = false
		}
	}
	defer closeDB()
	mods := modules(cfg.Env)
	// jarvisd's own records go to stderr and, once migrated, into the logs module's store.
	for _, m := range mods {
		if l, ok := m.(*logsmod.Module); ok {
			shipper = logging.NewShipper(l.Sink(), 4096, 200, 2*time.Second)
			deps.Log = logging.New(os.Stderr, logging.ParseLevel(os.Getenv("JARVIS_LOG_LEVEL")), shipper)
			deps.Queue = queue.New(deps.DB, deps.Log)
			deps.Scheduler = scheduler.New(deps.DB, deps.Queue, deps.Log)
		}
	}
	if os.Getenv("JARVIS_DEV_SPEAKER_HINTS") == "1" {
		deps.Log.Warn("JARVIS_DEV_SPEAKER_HINTS=1: nodes can claim who is speaking; for dev sandboxes only")
	}
	for _, m := range mods {
		switch x := m.(type) {
		case *authmod.Module:
			log, cfg := deps.Log, deps.Config
			x.OnSetupToken = func(token, path string) {
				announceSetup(os.Stderr, stderrIsTerminal(os.Getenv, os.Stderr), log, cfg, token, path, browser)
			}
		case *adminmod.Module:
			// AD8 restart button and AD5 one-click update.
			x.Restarter = restarter
			userUnit := restarter.Kind() == service.Systemd && service.UserUnit()
			x.Upgrade = adminmod.UpgradeConfig{
				Exe:      exe,
				Helper:   helper,
				UserUnit: userUnit,
				Source:   updateSource(),
			}
			// AD8b: the Stop button only when the installed definition keeps jarvisd stopped.
			kind := restarter.Kind()
			x.StopBlocker = func() (string, string) { return service.StopBlocker(kind, userUnit) }
			if helper.OnDemand() {
				x.Upgrade.Trigger = func(ctx context.Context) error { return triggerHelper(ctx, upaths, helper) }
			}
		}
	}
	log := deps.Log
	log.Info("starting jarvisd", "version", version, "home", deps.Config.Home, "supervisor", restarter.Kind())
	go func() {
		<-ctx.Done()
		logging.SetShuttingDown(true) // work the stop cuts short logs at debug, not error
		_ = service.Stopping()
	}()
	// The post-upgrade health gate: every listener bound and /health answering within the
	// timeout, else roll back.
	var gateFailed atomic.Value // string: why
	gateDone := make(chan struct{})
	var gateOnce sync.Once
	endGate := func() bool {
		first := false
		gateOnce.Do(func() { first = true; close(gateDone) })
		return first
	}
	if gate != nil {
		timer := time.AfterFunc(gateTimeout(), func() {
			reason := fmt.Sprintf("%s did not become healthy within %s", gate.To, gateTimeout())
			if !endGate() {
				return
			}
			gateFailed.Store(reason)
			log.Error("upgrade: health gate failed; rolling back", "reason", reason)
			cancel()
		})
		defer timer.Stop()
	}
	var runner *module.Runner
	runner = &module.Runner{Deps: deps, Modules: mods, AllowDowngrade: allowDowngrade, Mounts: config.Paths, OnReady: func() {
		if err := service.Ready(ctx); err != nil {
			log.Warn("could not notify the service manager", "err", err)
		}
		log.Info("jarvisd ready")
		if gate != nil {
			go passGate(ctx, log, runner, upaths, gate, gateDone, endGate)
		}
	}}
	err = restarter.Err(runner.Run(ctx))
	var de *db.DowngradeError
	if errors.As(err, &de) {
		err = downgradeHelp(de, upaths)
	}
	if v := gateFailed.Load(); v != nil {
		return rollbackAfterGate(ctx, log, upaths, v.(string), restarter.Kind().Supervised(), helper, closeDB)
	}
	if gate != nil && err != nil && !service.Requested(err) {
		// The new version failed to start: the marker holds the attempt, and the next start
		// rolls back once MaxFailedStarts is reached.
		log.Error("upgrade: the new version failed to start", "err", err, "attempt", gate.Attempts)
	}
	if errors.Is(err, service.ErrRestart) {
		log.Info("exiting for the service manager to restart jarvisd", "supervisor", restarter.Kind())
	}
	if errors.Is(err, service.ErrStop) {
		log.Info("stopped from the admin; jarvisd stays stopped until started again",
			"supervisor", restarter.Kind(), "exit_code", service.ExitCode(err),
			"start_command", service.StartCommand(restarter.Kind(), restarter.Kind() == service.Systemd && service.UserUnit()))
	}
	return err
}

// passGate polls the config listener's /health (admin's when config is disabled) once every
// listener is bound, and records the upgrade's success unless the gate timer fired first.
func passGate(ctx context.Context, log *slog.Logger, runner *module.Runner, p update.Paths, gate *update.Marker,
	done <-chan struct{}, end func() bool) {
	addr := runner.Addr(config.ListenerConfig)
	if addr == "" {
		addr = runner.Addr(config.ListenerAdmin)
	}
	for addr != "" && !healthy(ctx, addr) {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case <-time.After(time.Second):
		}
	}
	if !end() {
		return // the timer fired first
	}
	if err := update.Confirm(p); err != nil {
		log.Error("upgrade: recording success failed", "err", err)
		return
	}
	log.Info("upgrade: passed the health gate", "from", gate.From, "to", gate.To)
}

// rollbackAfterGate rolls back once serve has stopped and the database is closed.
func rollbackAfterGate(ctx context.Context, log *slog.Logger, p update.Paths, reason string, supervised bool,
	helper service.Helper, closeDB func()) error {
	closeDB()
	if err := update.RequestRollback(p, reason); err != nil {
		return err
	}
	if !update.CanWrite(p.Exe) {
		if helper == service.HelperPrestart && supervised {
			return service.ErrRestart // the privileged pre-start rolls back
		}
		if helper.OnDemand() && supervised {
			// serve's context is over; wait on our own (the helper restarts jarvisd next).
			awaitHelper(context.WithoutCancel(ctx), log, p, helper, update.StateRollbackRequested)
			return service.ErrRestart
		}
		return fmt.Errorf("upgrade: %s; rolling back needs administrator rights: run %s", reason, elevated("jarvisd upgrade --rollback"))
	}
	res, err := update.Rollback(context.WithoutCancel(ctx), p, reason)
	if err != nil {
		return err
	}
	log.Info("upgrade: rolled back", "from", res.To, "to", res.From, "db_restored", res.DBRestored, "data_since", res.DataSince)
	if supervised {
		return service.ErrRestart
	}
	return fmt.Errorf("upgrade: %s; rolled back to %s; start jarvisd again", reason, res.From)
}

// downgradeHelp explains the downgrade guard's refusal.
func downgradeHelp(de *db.DowngradeError, p update.Paths) error {
	return fmt.Errorf("%w.\nThis jarvisd (%s) is older than the one that last ran on this data. Either install that "+
		"newer version again (jarvisd upgrade --version vX), or restore a database snapshot taken before the "+
		"upgrade from %s (stop jarvisd, copy it over %s). Experts: serve --allow-downgrade runs anyway",
		de, version, p.BackupsDir(), filepath.Join(p.Home, "jarvis.db"))
}

func migrateStatus(ctx context.Context, stdout io.Writer) error {
	deps, err := openDeps(ctx)
	if err != nil {
		return err
	}
	defer deps.DB.Close()
	fmt.Fprintf(stdout, "%-20s %8s %8s %8s\n", "MODULE", "CURRENT", "LATEST", "PENDING")
	for _, m := range modules(deps.Config.Env) {
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
