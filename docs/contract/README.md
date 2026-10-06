# Contract suite (`contract/`)

The black-box wire-contract suite: PLAN §4 layer 1, Phase 0 items 2 (harness) and 4 (freeze
the wire contracts).

- It is written in **Go** and speaks only HTTP (and later MQTT) to a target. It never imports
  `internal/`. The same tests run against the legacy Python stack now and against `jarvisd`
  later.
- **A test only counts once it passes against Python.** After that it is the oracle for Go.
- It freezes **shapes, not data**: status codes, JSON key sets and types, and the exact error
  `detail` strings that clients branch on. Ids, tokens and timestamps are matched by type.
- Every file has the `contract` build tag, so a plain `go test ./...` skips the package.

## Running

```sh
cp .contract.env.example .contract.env   # gitignored; fill in the values
scripts/contract.sh                      # all tests, verbose
scripts/contract.sh -run 'TestAuth|TestConfig'
```

The script sources `.contract.env` (or `$JARVIS_CONTRACT_ENV_FILE`) and runs
`GOTOOLCHAIN=local CGO_ENABLED=0 mise exec go@1.25 -- go test -tags contract -count=1 ./contract/... -v`.
Extra arguments are passed to `go test`. A full run against the MBP takes about 30 s.

Tests skip with a message when the environment they need is missing. With no
`JARVIS_CONTRACT_HOST` set, every test skips.

### Environment

| Variable | Required | Meaning |
|---|---|---|
| `JARVIS_CONTRACT_HOST` | yes | Host or IP of the stack under test, e.g. the MBP `10.0.0.103`. **Never prod.** |
| `JARVIS_CONTRACT_AUTH_ADMIN_TOKEN` | for fixtures | jarvis-auth's master admin token (`JARVIS_AUTH_ADMIN_TOKEN` on the target). Without it, every test that needs a user, app or node skips. |
| `JARVIS_CONTRACT_APP_ID` / `JARVIS_CONTRACT_APP_KEY` | no | Reuse an existing app client instead of minting a throwaway one. |
| `JARVIS_CONTRACT_SKIP` | no | Comma list of listeners the target doesn't run (`config,auth,logs,command-center,llm,whisper,tts,notifications,recipes,ocr`). |
| `JARVIS_CONTRACT_PORT_<LISTENER>` | no | Port override per listener. Dashes become underscores, e.g. `JARVIS_CONTRACT_PORT_COMMAND_CENTER=17703`. |
| `JARVIS_CONTRACT_SCHEME` | no | `http` (default) or `https`. |
| `JARVIS_CONTRACT_TIMEOUT` | no | Per-request timeout as a Go duration. The default is `15s`. |
| `JARVIS_CONTRACT_SLOW_TIMEOUT` | no | Timeout for inference calls (LLM, STT, TTS synthesis). The default is `180s`. |

To get the admin token from the MBP without echoing it:

```sh
ssh alexanderberardi@10.0.0.103 'PATH=/opt/homebrew/bin:$PATH docker exec jarvis-auth-api printenv JARVIS_AUTH_ADMIN_TOKEN'
```

Default ports are the legacy ones (PLAN §3.1): 7700 config, 7701 auth, 7702 logs,
7703 command-center, 7704 llm, 7706 whisper, 7707 tts, 7712 notifications, 7030 recipes and
7031 ocr.

## Harness

| File | What |
|---|---|
| `target.go` | Loads the env into a `Target`, which builds URLs per listener. `Do`/`Get`/`Post` send JSON. `T(t)` skips when no target is set; `NeedAdmin`/`Need` skip on missing credentials or on skipped listeners. A 429 with `Retry-After` ≤ 65 s is waited out and retried once or twice, because auth's per-IP flood guard is not under test. |
| `resp.go` | `Resp` with `ExpectStatus`, `ExpectShape`, `ExpectError(status, detail)` and so on. Every failure prints the request line, the status and the body. |
| `shape.go` | Shape matchers. `Obj` is an exact key set, as in pydantic models; `Open` is a subset. Also `Optional`, `String`, `Int`, `Bool`, `Eq`, `OneOf`, `NullOr`, `ArrayOf`, `MapOf`, `UUID`, `TimestampUTC`, `TimestampNaive`, and `ValidationError(loc…)` for FastAPI 422s. |
| `fixtures.go` | Throwaway fixtures created through the real APIs, tagged `contract-<runid>`. |
| `media_helpers.go` | For the LLM/STT/TTS contracts: `SlowDo`/`SlowJSON` (raw bodies, slow timeout, keeps `Transfer-Encoding`), `ExpectChunked`, `ExpectMediaType`, `ExpectHeaderVal`; `MultipartBody`/`FormFile`/`FormField`; `SineWAV` and `ParseWAV`; strict SSE parsing (`ParseSSE` for a whole body, `OpenSSE`/`Next` for incremental reads). |

### Fixtures

| Fixture | Created by | Cleaned up by |
|---|---|---|
| App client | `POST /admin/app-clients` (admin token) | `POST /admin/app-clients/{id}/revoke`. Auth has no delete, so revoked `contract-*` rows accumulate on the target. |
| User + household | `POST /auth/register`, which creates the user's solo "My Home" household | `DELETE /auth/me`, which deletes the user, cascades the solo household and its nodes, and fans out a purge to CC and notifications. |
| Superuser | the above, plus `PUT /admin/users/{id}/superuser` and a fresh login | demote, then delete |
| Node | `POST /admin/nodes` in a user's household, with service grants | `DELETE /admin/nodes/{id}` (deactivate). A 404 after the household cascade is fine. |

The `Shared*` fixtures are created once per run, and `TestMain` tears them down. Tests that
mutate a fixture use `New*`, which cleans up with `t.Cleanup`.

> **Command-center node rows are not created yet.** CC rejects a node that is valid in auth
> but missing from its own `nodes` table ("Node not configured locally", docs/cc/00 §3.1).
> CC node routes need a CC-side fixture, through admin `POST /api/v0/admin/nodes` with
> `ADMIN_API_KEY` or through provisioning. Add `JARVIS_CONTRACT_CC_ADMIN_KEY` when the first
> CC node test lands.

If a run dies before its cleanup, the leftovers are `contract-*` users, nodes and app clients.
A stuck user can be removed with a superuser's `POST /superuser/users/{id}/temp-password`,
then a login as that user and `DELETE /auth/me`.

## Covered so far (green against the MBP Python stack, 2026-10-06)

| Area | Test | Frozen |
|---|---|---|
| Health | `TestHealth/*`, `TestLogsPing` | `GET /health` on all 10 listeners: status, body shape, and the identity literals (`service`, `status` values). Logs `GET /ping`. |
| Discovery | `TestConfigInfo` | `GET /info` == `{"service":"jarvis-config-service"}` (mobile LAN probe) |
| | `TestConfigServices` | `GET /services` (ServiceResponse key set). The core names are present. `style=external&remote_host=H` rewrites to H, and the advertised auth and CC URLs actually answer `/health`. `style=dockerized` uses `host.docker.internal`. An unknown style is 422, which mobile's fallback relies on. |
| | `TestConfigServiceByName` | `GET /services/{name}` shape, and the 404 detail `Service '<name>' not found` |
| App-to-app | `TestAuthAppPing` | 200 `{app_id,name}`. 401 `Missing app credentials` / `Invalid app credentials` for wrong key, unknown app or revoked app. |
| Node validation | `TestAuthValidateNode` | 200 valid with `household_member_ids`. 200 `valid:false` with the exact reasons: `Invalid node credentials`, `Node not found`, `Node is inactive`, and `Node is not authorized to access service '<id>'`. 401 without app credentials; 422 on missing fields. |
| RS256 | `TestAuthPublicKey` | `{public_key (PEM), algorithm:"RS256", kid}`, and the PEM parses as RSA |
| User tokens | `TestAuthRegisterLoginRefresh` | RegisterResponse and TokenResponse key sets. The username defaults to the email's local part. Error details for duplicate email, bad password and unknown email (same detail, no account probing). Refresh rotates. Replaying within the grace window returns the same successor. `Invalid refresh token`. |
| | `checkAccessToken` | JWT header `alg` ∈ {HS256, RS256}; RS256 tokens verify against `/auth/public-key`. Claim key set `{sub, email, is_superuser, household_id?, jti, exp, iat}`. |
| | `TestAuthMe` | UserOut. 401 `Not authenticated` + `WWW-Authenticate: Bearer`. 401 `Could not validate credentials`. |
| | `TestAuthSetupStatus`, `TestAuthAdminToken` | `{needs_setup: bool}`; admin 401 `Unauthorized`; the app-client list shape |
| Log ingest | `TestLogsBatchApp` | `POST /api/v0/logs/batch` and `/api/v0/logs` with app credentials: 204 with an empty body, including for an empty batch. 422 on a bad level. 401 details. |
| | `TestLogsBatchNode` | `POST /api/v0/node/logs/batch` with `X-Node-Id`/`X-Node-Key`: 204. 401 `Missing node credentials`. 403 with validate-node's reason, including when the node lacks the `jarvis-logs` grant. |
| Settings | `TestSettingsAppAuth/*` | `/settings/`, `/settings/categories` and `/settings/{key}` on auth, logs, CC, llm, whisper, tts and recipes, with app credentials. Covers the SettingResponse key set, `total`, secret masking, the nested 404 `{"detail":{"error":{type,message,code}}}`, the 401 details, and 403 `Superuser access required` for a plain user. |
| | `TestSettingsConfigService` | config-service `/settings` is **superuser-only**: app credentials get 401 `Missing or invalid Authorization header`; a garbage token gets 401 `Invalid or expired token`. |
| | `TestSettingsSuperuserRead`, `TestSettingsOptionsOnSingleRead` | Superuser JWT reads; the single-read `options` bug (below) |
| LLM | `TestLLMChatCompletion` | Non-stream `POST /v1/chat/completions` key set: `id` (`chatcmpl-` + 8 hex), `object:"chat.completion"`, `created`, `model` (echoes `live`/`background`; anything else is forced to `live`), one choice `{index:0, message:{role:"assistant", content, tool_calls:null, tool_call_id:null}, finish_reason}`, `usage{prompt,completion,total}_tokens`, and the Jarvis `date_keys`: `null` unless `include_date_context:true`, then an array (`[]` when no date is mentioned). 422 for missing `messages`/`model`. |
| | `TestLLMChatStream` | `stream:true`: 200 `text/event-stream`, chunked, `Cache-Control: no-cache`, `X-Accel-Buffering: no`, `X-Request-Id` echoed (or a generated 32-hex id). Every event is exactly `data: <json>\n\n`. Frames are `{"delta": "<tok>"}`, then a final `{"done":true, "content", "usage", "tool_calls":null, "finish_reason"}` whose `content` equals the concatenated deltas. **No `[DONE]` sentinel, not OpenAI chunks, no `date_keys`.** Cancel: `POST /v1/chat/completions/cancel/{id}` → `{"status":"cancelling","request_id"}`, the stream ends with `{"cancelled": true}` and no done frame; an unknown id is 404 `{"detail":{"error":{type:"not_found", message:"No active stream with request id '<id>'", code:"request_not_found"}}}`. |
| | `TestLLMImageToTextModel` | An image to a text-only model (non-stream: the OpenAI error shape, see LEGACY-BUGs; stream: not refused) |
| | `TestLLMModels`, `TestLLMEngine`, `TestLLMDateKeyVocabulary` | Unauthenticated `GET /v1/models` (`{object:"list", data:[{id, object:"model", created:0, owned_by:"jarvis", supports_images?, context_length?}]}`), `GET /v1/engine` (`{inference_engine, backend_type, allows_caching, description}`, CC reads `allows_caching`), `GET /v1/adapters/date-keys` (`static_keys` etc., CC loads the vocabulary from it). |
| | `TestLLMEmbeddings` | `POST /v1/embeddings`: string or list input, `{object:"list", data:[{object:"embedding", embedding, index}], model, usage{prompt_tokens,total_tokens}}`, indices in input order, `[]` input → empty data and zero usage. |
| | `TestLLMQueueEnqueue` | `POST /internal/queue/enqueue` (CC's background-job payload): `{accepted:true, job_id, deduped:false}`, then `deduped:true` for the same `job_id`+`idempotency_key`. 400 nested errors with codes `expired` (`Job already expired`), `invalid_request`, `missing_schema`; 422 without `callback`. |
| | `TestLLMAppAuth` | 401 `Missing app credentials` / `Invalid app credentials` on chat, cancel, embeddings and enqueue (the proxy's own `auth/app_auth.py`). |
| TTS | `TestTTSAudioFormat` | `GET /audio/format` `{sample_rate, channels:1, sample_width:2, provider}` |
| | `TestTTSSpeakStream` | `POST /speak/stream`: 200 `audio/raw`, chunked, decimal `X-Audio-Sample-Rate`/`X-Audio-Channels`/`X-Audio-Sample-Width` that agree with `/audio/format`, `X-Audio-Provider`; no `X-Assistant-Message` (that is CC's); headerless body of whole `channels × width` frames. |
| | `TestTTSSpeak`, `TestTTSEmptyText`, `TestTTSAppAuth` | `POST /speak`: 200 `audio/wav`, a complete RIFF WAV whose fmt chunk matches `/audio/format` and whose data is whole frames. Empty text (bug, below). The `require_app_auth` 401s on all three routes. |
| STT | `TestWhisperTranscribe` | `POST /transcribe` multipart: `file` (required, 422 `[body, file]` without it), optional `speaker_audio`; CC's extra form fields (`conversation_id`, `language`, `task`) are accepted and ignored. Response `{text, segments:[{t0_ms,t1_ms,text}], speaker:{user_id, confidence}, affect}`; `affect` is present and `null` (D38); `speaker` is always present, `{user_id:null, confidence:0}` with `?speaker_recognition=false`. 401s. |
| | `TestWhisperVoiceProfiles` | For a throwaway user: `GET /voice-profiles/check` `{exists, user_id, sample_count}`; `POST /voice-profiles/enroll?user_id&household_id[&sample_index]` `{status:"enrolled", user_id, household_id, sample_index, total_samples}` with auto-allocated indices, 400 `sample_index must be in [0, 999]`; `GET /voice-profiles/{uid}/samples`; `GET /voice-profiles?household_id` (hashed filenames); `POST /voice-profiles/verify` `{matched, confidence, user_id}`, 404 `No voice profile enrolled for user <id>`; `DELETE …/samples/{i}` `{status, user_id, sample_index, remaining_samples}`, 404 `Sample <i> not found for user <id>`; `DELETE /voice-profiles/{uid}` `{status, user_id, household_id}`, 404 `Voice profile not found`; `DELETE /voice-profiles/user/{uid}` `{status, user_id, households}` (idempotent). 422 on missing query params. |

Not frozen on purpose:

- Loki rejecting old timestamps with 502 (`Failed to push logs to Loki`). That is Loki's behaviour, not a contract.
- The current `auth.algorithm`. Tokens may be HS256 or RS256.
- `needs_setup`'s value.
- The LLM health `model_service` blob.
- Any LLM content, transcript text, audio samples, or the TTS sample rate itself (16000 Piper on the MBP, 24000 Kokoro in prod); only that the headers, `/audio/format` and the WAV agree.
- The queue **callback envelope** `{job_id, job_type, finished_at, status, result, error, timing, metadata}` (llm-proxy `queues/tasks.py`). Freezing it needs a listener the target can POST to; the enqueue test points the callback at a closed port on the target's loopback.
- The stream **error frame** `{"error": "Model service error <status>"}` / `{"error": "Model service connection error: …"}`. The model service only refuses a stream for an unloaded slot, a non-streaming backend or a bad internal token, none of which a black-box test can cause. It is documented here so the Go port keeps it.

## LEGACY-BUGs frozen as-is

These are marked `// LEGACY-BUG:` in the tests. Per D8, Go fixes bugs by default, so each one
becomes an intended divergence: when the Go module lands, flip the assertion and log it.

1. **Single-key settings read drops `options`.** `GET /settings/{key}` never passes `options`,
   so it is always `null` even when the list shows options (jarvis-settings-client `routes.py`
   `get_setting`). Test: `TestSettingsOptionsOnSingleRead`.
2. **Logs answers bad node credentials with 403, not 401.** The detail is validate-node's
   reason. Every other service uses 401. Test: `TestLogsBatchNode/invalid_credentials`.
3. **Naive timestamps** with no zone, in config-service `created_at`/`updated_at` and in the logs
   and CC `/health` `timestamp`. jarvis-auth emits zoned (`…Z`) timestamps. Tests:
   `serviceShape`, `healthShapes`.
4. **Logs `/health` reports `degraded` with HTTP 200** when Loki is down, so the registry probe
   shows it as healthy. Test: `healthShapes[Logs]`.

LLM, STT and TTS (`llm_test.go`, `tts_test.go`):

- **TTS empty text is 200 JSON `{"error":"No text provided"}`** on `/speak` and `/speak/stream`, which CC relabels as audio (docs/cc/06 §8 item 7). Test: `TestTTSEmptyText`.
- **Stream final frame `usage` is `{}`** with the in-process GGUF backend (the REST backend sends counts). Test: `streamFinal`.
- **Non-stream image to a text-only model is 500 `internal_server_error`** wrapping the model service's 400 in the message. llm-proxy #86 (2026-10-03) fixed this to a 400 `invalid_request_error`, but the MBP's running proxy predates it; the test accepts either. Test: `TestLLMImageToTextModel/non-stream`.
- **The stream path never checks for images**: the same request streams a normal answer. Test: `TestLLMImageToTextModel/stream`.

Also seen, not frozen: on the stream path `reasoning_budget: 0` did not stop Qwen3 thinking on the MBP (deltas start with `<think>`). The queue worker's expiry branch calls `_send_callback` without the required `job_type`/`metadata` arguments (a `TypeError` instead of an `expired` callback). Whisper's account purge removes the user directory but leaves the empty `voice_profiles/<household_id>/` directory behind, so **each run of `TestWhisperVoiceProfiles` leaves one empty directory on the target** (no API removes it).

Seen while reading but not observable black-box: logs' node routes enrich entries with
`context.user_id` from validate-node's `user_id`, a field validate-node never returns, so it is
always `null` in Loki.

## Remaining wire contracts (PLAN §6 Phase 0 item 4)

- [x] **LLM stream frames.** Done against the MBP's Qwen3-8B (`llm_test.go`): framing, delta,
  final and cancelled frames, non-stream key set, `date_keys`, models/engine/date-key vocabulary,
  embeddings, enqueue + dedup, app-auth errors. There is no `[DONE]` terminator.
  - Still open: the queue callback envelope (needs a reachable listener) and the stream error
    frame (not triggerable black-box); see "Not frozen on purpose".
  - For CC's own streaming (the voice path and `/node/llm/chat`), CC needs a **fake LLM**: an
    OpenAI-compatible scripted server in `contract/fakes/llm`. Point CC at it via
    `jarvis-llm-proxy-api` in `/services`, or via a CC setting. That requires re-registering the
    service on the target, so do it only on a dev box and restore it afterwards.
- [~] **Voice PCM stream headers.** jarvis-tts side done (`tts_test.go`); STT `/transcribe` and the
  voice-profile routes done (`whisper_test.go`). Still open: CC's media proxy
  (`app/api/media.py`, needs the CC node fixture), which re-emits the headers.
  Original scope: jarvis-tts `POST /speak/stream`, and CC's media proxy, which re-emits them. Covers `X-Audio-Sample-Rate`, `X-Audio-Channels`,
  `X-Audio-Sample-Width`, `X-Audio-Provider`, the content type, and chunked raw PCM whose
  length is a multiple of `channels × width`.
  - TTS needs node or app auth: a node fixture with the tts service grant. CC's proxy also needs
    a CC node row (see the note above).
  - The STT side: whisper `/transcribe` request and response shape with a short WAV fixture
    (`internal/audio` can generate a sine).
- [ ] **MQTT topic catalogue** (docs/cc/05 §2.4, about 21 topics). This needs a **fake MQTT node**:
  - a client with `client_id=jarvis-node-<id>`, `clean_session=false`, subscribed to
    `jarvis/nodes/<id>/#` and `jarvis/auth/+/ready` at QoS 1;
  - triggering each CC publish through its HTTP route, then asserting topic, QoS 1,
    retain=false and the payload shape (for example `commands` is a JSON **array**
    `[{"command", "details":{…,"request_id"}}]`);
  - publishing the two response topics back.

  The broker on the MBP is mosquitto on **1884** (container 1883), advertised in `/services` as
  `jarvis-mqtt-broker` (`mqtt://localhost:1884`). Credentials come from
  `GET /api/v0/node/mqtt-credentials` (node auth on CC), so this also needs the CC node fixture.
  Use `github.com/mochi-mqtt/server/v2`'s packets, or a minimal 3.1.1 client. No new dependency
  is allowed without a go.mod change.
- [ ] **Public plugin endpoints** on CC: `/api/v0/node/inbox-item`,
  `/api/v0/node/push-notification`, `/api/v0/node/llm/chat`, `/api/v0/callbacks` and
  `/api/v0/signals`.
  - They need the CC node fixture (`X-API-Key: node_id:node_key` plus a CC `nodes` row). The
    `/signals` route also takes app credentials (node-or-app auth, docs/cc/00 §3.1).
  - `/node/llm/chat` needs the fake LLM to be deterministic. `push-notification` goes through
    notifications, so assert only CC's response, or add a notifications inbox read.
  - D4/D5/D6 change auth on some of these. Freeze the Python behaviour first, then mark the
    changes as divergences.
- [ ] Also from §6 item 4 but cheap once the CC fixture exists: node `X-API-Key` auth errors on
  CC (401 `Node not configured locally`), `/internal/validate-household-access`, and
  `/internal/users/batch`.

The fakes from Phase 0 item 2 still to build: **fake LLM** (scripted OpenAI-compatible),
**fake relay**, and **fake MQTT node**. They go in `contract/fakes/…` with the same build tag.

## Running against jarvisd (parity)

A contract test proves parity once the same test passes against jarvisd. During the strangler
phase jarvisd serves only some listeners, so point the suite at it with a separate env file and
skip the rest:

```bash
JARVIS_HOME=$(mktemp -d) JARVIS_PORT_CONFIG=17700 JARVIS_CONFIG_ADMIN_TOKEN=tok JARVIS_MDNS=0 jarvisd serve &
# Registry rows for services jarvisd doesn't serve yet (here: the Python ones), e.g.
curl -X POST -H 'X-Admin-Token: tok' -H 'Content-Type: application/json' \
  -d '{"name":"jarvis-auth","host":"localhost","port":7701}' localhost:17700/services
cat > /tmp/jarvisd.env <<EOT
JARVIS_CONTRACT_HOST=127.0.0.1
JARVIS_CONTRACT_PORT_CONFIG=17700
JARVIS_CONTRACT_AUTH_ADMIN_TOKEN=unused
EOT
JARVIS_CONTRACT_ENV_FILE=/tmp/jarvisd.env scripts/contract.sh -run 'TestConfig|TestHealth/config'
```

| Module | Parity status |
|---|---|
| config | `TestConfigInfo`, `TestConfigServices` (all URL styles, 422), `TestConfigServiceByName`, `TestHealth/config` pass against jarvisd (2026-10-06). `/settings` and `/v1/services/*` wait for the auth module (superuser JWT). |
