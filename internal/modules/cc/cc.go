package cc

import (
	"context"
	"database/sql"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/alexberardi/jarvis-server/internal/modules/cc/errands"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/phone"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/policy"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/prompts"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/servertools"
	"github.com/alexberardi/jarvis-server/internal/modules/cc/watches"
	"github.com/alexberardi/jarvis-server/internal/modules/llm"
	"github.com/alexberardi/jarvis-server/internal/platform/authn"
	pconfig "github.com/alexberardi/jarvis-server/internal/platform/config"
	"github.com/alexberardi/jarvis-server/internal/platform/httpx"
	"github.com/alexberardi/jarvis-server/internal/platform/module"
	"github.com/alexberardi/jarvis-server/internal/platform/mqtt"
	"github.com/alexberardi/jarvis-server/internal/platform/queue"
	"github.com/alexberardi/jarvis-server/internal/platform/scheduler"
	"github.com/alexberardi/jarvis-server/internal/platform/settings"
)

// The cc module is command-center (legacy port 7703), ported in sub-phases (PLAN §6 Phase 5).
// Phase 5a, here: nodes and the MQTT data plane (docs/cc/05-nodes.md):
//
//   - the node registry, provisioning tokens and /nodes/register, the admin node routes,
//     heartbeat (with update dispatch) and liveness;
//   - factory reset: the tracked task flow with its token persisted on the task (D10), plus the
//     DELETE + verify-reset transition path for older nodes and mobile;
//   - the embedded MQTT broker (per-node credentials and ACLs, D4) and the Bus: command
//     publishes with the verify map (D48), request/response, and in-process result slots
//     that replace the /tmp rendezvous files;
//   - node settings requests and snapshots, the K2 relay, config push pending/ack (D6);
//   - node updates, ambient-noise calibration, request traces.
//
// Later sub-phases (voice and tool loop, memory, routines, smart home, mobile) plug into the
// Bus (Module.Bus) and the settings service (Module.Settings), and append their own setting
// Definitions.

// ServiceName is the legacy service name: the registry name and the node service grant
// (validate-node's service_id) a node needs to use CC.
const ServiceName = "jarvis-command-center"

// Setting keys owned by 5a (D11: only keys something reads are declared).
const settingUpdatesAllowCheck = "updates.allow_check"

// Definitions are the module's settings declared so far. The household settings allowlist's
// keys are marked Household, so the admin shows and edits each household's own value.
func Definitions() []settings.Definition {
	return markHouseholdControllable(routineDefinitions(slices.Concat(nodeDefinitions(),
		voiceDefinitions(prompts.DefaultPersona), followUpGateDefinitions(), packageDefinitions(), smartHomeDefinitions(),
		memoryDefinitions(), signalDefinitions(), phone.Definitions(), errands.Definitions(),
		householdSettingDefinitions(), compactionDefinitions(), watchDefinitions(),
		recordingDefinitions(), computerDefinitions())))
}

func nodeDefinitions() []settings.Definition {
	return []settings.Definition{
		{Key: settingUpdatesAllowCheck, Category: "updates", Type: settings.Bool, Default: false,
			Description: "Allow outbound update-version lookups to api.github.com (node release checks). " +
				"Default OFF so local-only households never egress to GitHub; an explicit version can " +
				"still be installed without it."},
	}
}

// UserVerifier verifies a user access token (the auth module's VerifyUser).
type UserVerifier interface {
	VerifyUser(ctx context.Context, token string) (authn.User, error)
}

// NodeRegistry is the auth module's node registration, in process (legacy CC called
// /internal/nodes/register and DELETE /internal/nodes/{id} with its app credentials).
// RegisterNode errors carrying StatusCode() are client errors whose message is the detail.
type NodeRegistry interface {
	RegisterNode(ctx context.Context, nodeID, householdID, name string, services []string) (string, error)
	DeactivateNode(ctx context.Context, nodeID string) error
	// NodeNames is each known node's registered name, by node id (ids auth doesn't know are
	// absent). It is read only, for the node list.
	NodeNames(ctx context.Context, nodeIDs []string) (map[string]string, error)
	// RenameNode sets an active node's registered name (its heartbeat reports a new one).
	RenameNode(ctx context.Context, nodeID, name string) error
}

// MQTTOptions configures the embedded broker. Empty addresses disable a listener; CC still
// publishes in process.
type MQTTOptions struct {
	TCPAddr        string
	WSAddr         string
	AllowAnonymous bool     // dev only: admit clients without credentials, read-only (mqtt.Anonymous)
	AllowedOrigins []string // extra hosts whose pages may open the WebSocket listener
	Disabled       bool     // no broker at all (MQTT routes answer 503 / log, as legacy without MQTT)
}

// Module is the command-center module.
type Module struct {
	Auth  authn.Authority
	Users UserVerifier
	Nodes NodeRegistry
	// AdminKey is the legacy ADMIN_API_KEY (X-API-Key on admin routes). Empty rejects all.
	AdminKey string
	// DevSpeakerHints (JARVIS_DEV_SPEAKER_HINTS=1, dev sandboxes only) trusts a node turn's
	// speaker_user_id as a voice match, so text-only pipeline tests can play a known speaker.
	// Off, the field is ignored as D2 says: only jarvisd's own identification counts.
	DevSpeakerHints bool
	// ServiceID overrides the service grant nodes need (default ServiceName).
	ServiceID string
	MQTT      MQTTOptions
	// SettingsRead / SettingsWrite guard /settings (combined app-or-superuser / superuser).
	SettingsRead, SettingsWrite settings.Guard
	// GitHubAPI is the releases API base (default https://api.github.com).
	GitHubAPI  string
	HTTPClient *http.Client
	Version    string

	// Publisher replaces the embedded broker (tests).
	Publisher Publisher

	// Phase 5b, the voice pipeline: the other modules, in process. Nil disables what needs them.
	LLM    LLM
	STT    STT
	TTS    TTS
	Notify Notifier
	Names  NameResolver
	// Memory and Attention are 5c hooks (nil until those modules exist).
	Memory    MemoryProfile
	Attention AttentionGate // nil: Register wires the 5c broker (attention.go)
	// HouseholdClock overrides the household timezone (default: the household.timezone setting,
	// else the zone its most recently seen node reported; timezone.go). Used by attention (D18),
	// errands, signal reactions and recipes; a turn's own zone ignores it.
	HouseholdClock HouseholdTimezone
	// WebSearch replaces DuckDuckGo for quick_search / deep_research (tests).
	WebSearch servertools.WebSearcher
	// WatchFetcher replaces the SSRF-guarded fetcher a web watch reads its page with (tests).
	WatchFetcher WatchFetcher
	// DefaultPromptProvider names the prompt provider when llm.prompt_provider is unset (e.g.
	// the live model's catalog entry). Nil: an unset setting is an error (D11).
	DefaultPromptProvider func(ctx context.Context) string
	// LiveModelTraits reports the live model's tool calling and thinking switch, for the auto
	// values of llm.tool_calling and llm.thinking_switch (llmmode.go). Nil: text tools, no
	// thinking suffix, the force-tools guard armed.
	LiveModelTraits func(ctx context.Context) llm.ModelTraits
	// Endpoints resolves an llm slot ("live", "background") for its capabilities: vision for
	// chat images, the context size for compaction (chat_images.go, compaction.go). Nil: no
	// images, no compaction.
	Endpoints func(ctx context.Context, label string) (llm.Endpoint, error)
	// Recipes is the recipes module's photo import for save_recipe_from_image (chat photo →
	// recipe, chat_image_actions.go). Nil: the tool is not offered.
	Recipes RecipeImporter
	// OCR reads images attached to tool results when the live model has no vision
	// (tool_images.go). Nil: the model is told it can't see them.
	OCR ImageOCR
	// Phone configures phone calls (5c, docs/cc/11; phone_wire.go).
	Phone PhoneConfig

	// 5c errand hooks (docs/cc/09): default to the module's own phone service (doc 11) and
	// schedules store (doc 08); tests replace them.
	ErrandPhone     errands.PhoneCalls
	ErrandSchedules errands.Schedules

	deps     module.Deps
	settings *settings.Service
	broker   *mqtt.Broker
	bus      *Bus
	now      func() time.Time
	k2       *k2Store
	resets   *resetStore
	ambient  *ambientStore
	releases *releaseCache
	// serverReleases is jarvis-server's release list, which Go nodes follow (updates.go).
	serverReleases *serverReleaseCache
	// lastInputs is each node's latest warmup prompt inputs (household_prompt.go).
	lastInputs lastInputs

	convs *convCache
	// warms shares identical warm-ups and remembers recent ones for re-warming (warm_flight.go).
	warms   warmFlight
	signals *convSignals
	// wakeSem and voiceSem bound the wake verifications and follow-up voice checks running
	// beside node transcriptions (whisper and the speaker model are shared): one more is
	// dropped, fail open, and counted.
	wakeSem, voiceSem     chan struct{}
	wakeDrops, voiceDrops atomic.Int64
	vocab                 sttVocab // desktop nodes' app names for whisper (stt_vocabulary.go)
	enroll                *enrollments
	pairings              *pairingStore // device-code pairing (pairing.go)
	tools                 *servertools.Registry
	dateKeys              []string      // DT_KEYS override (tests); nil = the shared vocabulary
	lanAddr               func() string // this host's LAN address override (tests); nil = netaddr.LANAddr
	// localIPs overrides this server's own addresses for "this computer" (tests; thisdevice.go).
	localIPs func() []net.IP
	// recordingDeleter asks an agent to delete its copy of a recording (recordings.go; tests
	// replace it).
	recordingDeleter func(ctx context.Context, nodeID, recordingID string) error
	// recordingAgent runs computer_record_start / _stop on a node (recording_tools.go; nil =
	// over the bus, tests replace it).
	recordingAgent func(ctx context.Context, nodeID, tool string, args map[string]any, uid int64) map[string]any
	// recordingTasks is what the user said they would show, by recording id, from
	// start_recording until the recording is stored (its narration).
	recordingTasks sync.Map
	// recordingNamed is, by computer, the recording a voice start_recording named
	// (namedRecording):
	// stop_recording asks for a name otherwise.
	recordingNamed sync.Map
	// recordingOn is, by computer, when a voice start_recording started recording there: a
	// node mid-recording answers the wake it shares with other nodes (arbitration.go).
	recordingOn sync.Map
	// keyframeMu makes a keyframe's limit check and store one step (recording_keyframes.go).
	keyframeMu sync.Mutex
	// wakes arbitrates one wake heard by several nodes (arbitration.go); wakeAfter is its
	// timer (tests replace it).
	wakes     *wakeArbiter
	wakeAfter func(time.Duration) <-chan time.Time
	// learnRecording gets every recording stored from an agent: it queues learning a
	// workflow from it (workflows_learn.go; tests replace it).
	learnRecording func(ctx context.Context, rec Recording) error

	cmdData *schemaCache // command-data schema cache (doc 12, packages.go)
	// pantryHTTP is the Forge share-code check's client (testinstall.go; legacy 10 s timeout).
	pantryHTTP *http.Client
	smart      *smartHome       // 5c smart home (smarthome.go)
	rt         *routineState    // 5c routines and errand schedules (routines.go)
	sig        *signalState     // 5c signals, proposals and attention (signals.go)
	phone      *phone.Service   // 5c phone calls (phone_wire.go)
	errands    *errands.Service // 5c errands and workflows (errands.go)

	// tasks tracks fire-and-forget goroutines so shutdown waits for them (tasks.go, Wait).
	tasks taskGroup

	// Autonomy (docs/autonomy/PLAN.md): the activity log and the rule policy (P1 fills Rules).
	autonomy *policy.Store
	policy   *policy.Policy
	watches  *watches.Store // watches (docs/autonomy/PLAN.md §2, watches.go)

	// 5d interactive callbacks (callbacks.go): the static server-callback map and result waiters.
	cbOnce sync.Once
	cbMap  map[string]serverCallbackFunc
	cbWait callbackWaiters

	// convJobHook captures conversation jobs instead of queueing them (tests).
	convJobHook func(jobType string, j convJob)
}

func (m *Module) Name() string      { return "cc" }
func (m *Module) Listener() string  { return pconfig.ListenerCC }
func (m *Module) Migrations() fs.FS { return Migrations() }

// Bus is the node data plane for other CC sub-phases (valid after Register).
func (m *Module) Bus() *Bus { return m.bus }

// Broker is the embedded broker, or nil (valid after Register).
func (m *Module) Broker() *mqtt.Broker { return m.broker }

// Settings is the module's settings service (valid after Register).
func (m *Module) Settings() *settings.Service { return m.settings }

func (m *Module) serviceID() string {
	if m.ServiceID != "" {
		return m.ServiceID
	}
	return ServiceName
}

const (
	cleanupJob   = "cc.cleanup"
	taskSweepJob = "cc.task_sweep"
)

func (m *Module) Register(mux *http.ServeMux, deps module.Deps) {
	m.deps = deps
	// Recent warm-ups survive a restart, so the live engine is warm before the first wake.
	if deps.Config.Home != "" {
		m.warms.path = filepath.Join(deps.Config.Home, "cc", "recent-warmups.json")
		m.warms.load()
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.GitHubAPI == "" {
		m.GitHubAPI = "https://api.github.com"
	}
	if m.HTTPClient == nil {
		m.HTTPClient = &http.Client{Timeout: 5 * time.Second}
	}
	m.k2 = newK2Store()
	m.resets = newResetStore()
	m.ambient = newAmbientStore()
	m.wakes = newWakeArbiter(func() time.Time { return m.now() }, m.wakeAfter)
	m.releases = &releaseCache{}
	m.serverReleases = &serverReleaseCache{}
	m.convs = newConvCache(m.now)
	m.signals = newConvSignals(m.now)
	m.wakeSem = make(chan struct{}, wakeVerifyLimit)
	m.voiceSem = make(chan struct{}, voiceCheckLimit)
	m.enroll = newEnrollments()
	m.pairings = newPairingStore()
	if m.tools == nil {
		m.tools = servertools.NewRegistry()
		m.registerServerTools()
	}

	svc, err := settings.New(deps.DB, "cc", Definitions(), deps.Log)
	if err != nil {
		panic(err) // static definitions: a programming error
	}
	m.settings = svc
	if err := svc.Migrate(context.Background()); err != nil {
		deps.Log.Error("cc: settings table migration failed", "err", err)
	}

	pub := m.Publisher
	if pub == nil && !m.MQTT.Disabled {
		b, err := mqtt.New(brokerAuth{auth: m.Auth, serviceID: m.serviceID()}, mqtt.Options{
			TCPAddr: m.MQTT.TCPAddr, WSAddr: m.MQTT.WSAddr, AllowAnonymous: m.MQTT.AllowAnonymous,
			AllowedOrigins: m.MQTT.AllowedOrigins, Logger: deps.Log,
		})
		if err != nil {
			deps.Log.Error("cc: mqtt broker unavailable", "err", err)
		} else {
			m.broker = b
			pub = b
		}
	}
	m.bus = newBus(pub, deps.Log, m.now)
	m.bus.seen = m.recordSeen

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status": "healthy", "service": ServiceName, "timestamp": pyNaive(m.now()),
		})
	})
	mux.HandleFunc("GET /api/v0/ping", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"message": "pong"})
	})
	if m.SettingsRead != nil && m.SettingsWrite != nil {
		svc.Mount(mux, m.SettingsRead, m.SettingsWrite)
	}

	const v0 = "/api/v0"
	// Registry, admin and lifecycle (admin.py, provisioning.py).
	mux.HandleFunc("GET "+v0+"/admin/nodes", m.user(m.handleListNodes))
	mux.HandleFunc("POST "+v0+"/admin/nodes/heartbeat", m.node(m.handleHeartbeat))
	mux.HandleFunc("GET "+v0+"/admin/nodes/{node_id}", m.user(m.handleGetNode))
	mux.HandleFunc("POST "+v0+"/admin/nodes", m.admin(m.handleCreateNode))
	mux.HandleFunc("PATCH "+v0+"/admin/nodes/{node_id}", m.admin(m.handlePatchNode))
	mux.HandleFunc("DELETE "+v0+"/admin/nodes/{node_id}", m.user(m.handleDeleteNode))
	mux.HandleFunc("POST "+v0+"/admin/nodes/{node_id}/factory-reset", m.user(m.handleFactoryReset))
	// Device sign-out and "this computer" (signout.go, thisdevice.go).
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/sign-out", m.user(m.handleSignOutNode))
	mux.HandleFunc("POST "+v0+"/node/sign-out", m.node(m.handleSelfSignOut))
	mux.HandleFunc("GET "+v0+"/nodes/this-device", m.user(m.handleThisDevice))
	mux.HandleFunc("POST "+v0+"/nodes/factory-reset/{task_id}/status", m.handleFactoryResetStatus)
	mux.HandleFunc("POST "+v0+"/nodes/verify-reset", m.handleVerifyReset)
	mux.HandleFunc("POST "+v0+"/provisioning/token", m.handleProvisioningToken)
	mux.HandleFunc("POST "+v0+"/nodes/register", m.handleRegister)
	// Device-code pairing (pairing.go): start and poll are the computer's, the rest a member's.
	mux.HandleFunc("POST "+v0+"/nodes/pairings", m.handleStartPairing)
	mux.HandleFunc("POST "+v0+"/nodes/pairings/{pairing_id}/poll", m.handlePollPairing)
	mux.HandleFunc("GET "+v0+"/nodes/pairings", m.user(m.handleListPairings))
	mux.HandleFunc("POST "+v0+"/nodes/pairings/approve", m.user(m.handleApprovePairing))
	mux.HandleFunc("POST "+v0+"/nodes/pairings/deny", m.user(m.handleDenyPairing))
	mux.HandleFunc("GET "+v0+"/node/mqtt-credentials", m.node(m.handleMQTTCredentials))

	// Settings snapshots and K2 (node_settings.py).
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/settings/requests", m.user(m.handleCreateSettingsRequest))
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/settings/requests", m.node(m.handleListSettingsRequests))
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/settings/requests/{request_id}", m.node(m.handleGetSettingsRequest))
	mux.HandleFunc("PUT "+v0+"/nodes/{node_id}/settings/requests/{request_id}/snapshot", m.node(m.handleUploadSnapshot))
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/settings/requests/{request_id}/result", m.user(m.handleSettingsResult))
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/k2", m.user(m.handleProvisionK2))
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/k2/provision/{request_id}", m.node(m.handleFetchK2))
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/k2/ack/{request_id}", m.node(m.handleAckK2))

	// Config push relay (doc 07 owns it; pending/ack are node-bound per D6).
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/config/push", m.handleCreateConfigPush)
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/config/pending", m.node(m.handlePendingConfig))
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/config/{push_id}/ack", m.node(m.handleAckConfig))

	// Commands and result sinks (node_commands.py, smart_home.py, node_tools.py, ...).
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/actions", m.user(m.handleNodeAction))
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/node-config", m.user(m.handleNodeConfig))
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/led/preview", m.user(m.handleLEDPreview))
	mux.HandleFunc("POST "+v0+"/commands/{request_id}/verify", m.node(m.handleVerifyCommand))
	// Computer tasks: the agent verifies a single-use approval token before a risky action.
	mux.HandleFunc("POST "+v0+"/computer/approvals/{token}/verify", m.node(m.handleVerifyApproval))
	// A recording the user stopped on their computer (the agent's menu, CLI or 30 minutes).
	mux.HandleFunc("POST "+v0+"/computer/recordings", m.node(m.handleUploadRecording))
	// Then each click's keyframe and its crop (recording_keyframes.go).
	mux.HandleFunc("PUT "+v0+"/computer/recordings/{id}/keyframes/{seq}", m.node(m.handlePutKeyframe(false)))
	mux.HandleFunc("PUT "+v0+"/computer/recordings/{id}/keyframes/{seq}/crop", m.node(m.handlePutKeyframe(true)))
	for _, p := range []string{"/device-control-results/", "/device-state-results/", "/mobile/node-tool-reports/"} {
		mux.HandleFunc("POST "+v0+p+"{request_id}", m.node(m.handleResult))
	}
	mux.HandleFunc("POST "+v0+"/mobile/voice-profile-results/{request_id}", m.node(m.handleVoiceProfileResult))

	// Phase 5b: the voice pipeline, tool loop, media proxy and node plugin API.
	m.registerVoice(mux)
	// Phase 5d: mobile chat (doc 13 §3.1-3.2) and the mobile voice-profile routes (doc 06 V1/V6).
	m.registerMobileChat(mux)
	m.registerVoiceProfiles(mux)
	m.registerMobileAudio(mux)
	// Phase 5c: smart home (doc 07).
	m.registerSmartHome(mux)
	// Packages, command data and the node tools view (doc 12).
	m.registerPackages(mux)
	m.registerPackageAssignments(mux)
	// Forge test install (doc 12 §3.3, ported after D5 was reversed 2026-10-08).
	m.pantryHTTP = &http.Client{Timeout: pantryDraftTimeout}
	m.registerTestInstall(mux)
	// Memory and knowledge (doc 04).
	m.registerMemory(mux)
	// Routines and errand schedules (doc 08).
	m.registerRoutines(mux)
	// Signals, proposals and the attention broker (doc 10).
	m.registerSignals(mux)
	// Watches (docs/autonomy/PLAN.md §2).
	m.registerWatches(mux)
	// Taught workflows and recordings (docs/agent/COMPUTER_CONTROL.md, plan P3 piece 26).
	m.registerWorkflows(mux)
	m.registerRecordings(mux)
	m.registerWorkflowLearning()
	// Phone calls, phonebook and call context (doc 11).
	m.registerPhone(mux)
	// Autonomy activity log and policy (docs/autonomy/PLAN.md §0).
	m.registerAutonomy()
	// Errands and workflows (doc 09).
	m.registerErrands()
	// Interactive callbacks (doc 13): after every subsystem that contributes server handlers.
	m.registerCallbacks(mux)
	// Household settings from mobile (doc 13 §3.7).
	m.registerHouseholdSettings(mux)
	m.registerHouseholdPrompt(mux)

	// Updates (node_updates.py).
	mux.HandleFunc("GET "+v0+"/releases/latest", m.handleLatestRelease)
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/update", m.user(m.handleRequestUpdate))
	mux.HandleFunc("POST "+v0+"/nodes/tasks/{task_id}/status", m.node(m.handleTaskStatus))
	mux.HandleFunc("GET "+v0+"/tasks/{task_id}", m.user(m.handleGetTask))
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/tasks/{task_id}/cancel", m.user(m.handleCancelTask))
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/tasks", m.user(m.handleListTasks))

	// Ambient noise (ambient_noise.py).
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/ambient-noise-measurements", m.user(m.handleTriggerAmbient))
	mux.HandleFunc("POST "+v0+"/nodes/{node_id}/ambient-noise-measurements/{request_id}/result", m.node(m.handleAmbientResult))
	mux.HandleFunc("GET "+v0+"/nodes/{node_id}/ambient-noise-measurements/{request_id}", m.user(m.handlePollAmbient))

	// Traces (traces.py admin router; the mobile route is cut).
	mux.HandleFunc("GET "+v0+"/admin/traces", m.admin(m.handleListTraces))
	mux.HandleFunc("GET "+v0+"/admin/traces/{trace_id}", m.admin(m.handleGetTrace))

	if deps.Queue != nil {
		deps.Queue.Register(cleanupJob, queue.Handler{Run: m.runCleanup})
		deps.Queue.Register(taskSweepJob, queue.Handler{Run: m.runTaskSweep})
		m.registerConvJobs(deps.Queue)
	}
}

// Start serves the broker and schedules the periodic loops (D27): token, settings-request
// and trace cleanup hourly (first run at startup, like the legacy token cleanup), and the
// node-task sweeper every 120 s.
func (m *Module) Start(ctx context.Context) error {
	if m.broker != nil {
		if err := m.broker.Start(ctx); err != nil {
			return err
		}
	}
	go m.convs.runSweeper(ctx, convSweep)
	go m.signals.runSweeper(ctx, convSweep)
	if err := m.startMemory(ctx); err != nil {
		return err
	}
	if err := m.startSignals(ctx); err != nil {
		return err
	}
	if err := m.startWatches(ctx); err != nil {
		return err
	}
	if err := m.startRecordings(ctx); err != nil {
		return err
	}
	if err := m.startPhone(ctx); err != nil {
		return err
	}
	m.startErrands(ctx)
	if m.deps.Scheduler == nil {
		return nil
	}
	if err := m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: cleanupJob, Kind: scheduler.KindInterval, JobType: cleanupJob,
		Spec: scheduler.Spec{Every: time.Hour, StartNow: true},
	}); err != nil {
		return err
	}
	if err := m.deps.Scheduler.Ensure(ctx, scheduler.Trigger{
		Name: taskSweepJob, Kind: scheduler.KindInterval, JobType: taskSweepJob,
		Spec: scheduler.Spec{Every: 120 * time.Second},
	}); err != nil {
		return err
	}
	if err := m.startCallbacks(ctx); err != nil {
		return err
	}
	return m.startRoutines(ctx)
}

// PurgeUser is the account-deletion hook (D20): the user's traces go; their id is removed
// from short-lived rows that outlive them.
func (m *Module) PurgeUser(ctx context.Context, tx *sql.Tx, userID int64) error {
	if m.convs != nil {
		m.convs.purgeUser(userID) // D20/M15: no in-memory identity outlives the account
	}
	if err := m.purgeErrands(ctx, tx, userID); err != nil {
		return err
	}
	if err := purgeMemoryUser(ctx, tx, userID); err != nil { // before transcripts: traces key off them
		return err
	}
	if err := m.purgeSignals(ctx, tx, userID); err != nil {
		return err
	}
	if m.watches != nil {
		if err := m.watches.PurgeUser(ctx, tx, userID); err != nil {
			return err
		}
	}
	if m.autonomy != nil {
		if err := m.autonomy.PurgeUser(ctx, tx, userID); err != nil {
			return err
		}
	}
	for _, q := range []string{
		// Taught workflows and recordings (plan P3 piece 26) are their owner's, shared or not.
		`DELETE FROM cc_taught_workflows WHERE owner_id = ?`,
		`DELETE FROM cc_recordings WHERE owner_id = ?`,
		`DELETE FROM cc_conversation_transcripts WHERE user_id = ?`,
		`DELETE FROM cc_request_traces WHERE user_id = ?`,
		`DELETE FROM cc_settings_requests WHERE user_id = ?`,
		`UPDATE cc_provisioning_tokens SET created_by_user_id = NULL WHERE created_by_user_id = ?`,
		// Smart home (D20): rooms and devices are the household's and stay; the user's OAuth
		// sessions (their provider tokens) go, and voice-scan rows forget them.
		`DELETE FROM cc_auth_sessions WHERE user_id = ?`,
		`UPDATE cc_bluetooth_scan_requests SET user_id = NULL WHERE user_id = ?`,
		`DELETE FROM cc_schedules WHERE user_id = ?`, // their trigger finds no row and lapses
		// Callback taps (doc 13): short-lived request rows carrying the tap's data; a queued
		// server-plane run finds no row and does nothing.
		`DELETE FROM cc_callback_jobs WHERE user_id = ?`,
	} {
		if _, err := tx.ExecContext(ctx, q, userID); err != nil {
			return err
		}
	}
	// Phone (D20): call drafts and call context go; sessions stay, de-identified.
	return phone.PurgeUser(ctx, tx, "cc_settings", userID)
}
