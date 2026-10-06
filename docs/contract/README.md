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
| `JARVIS_CONTRACT_CC_ADMIN_KEY` | for CC node tests | command-center's `ADMIN_API_KEY`. Creates the CC node fixture. Without it every `cc_*` test skips. |
| `JARVIS_CONTRACT_MQTT_PORT` | for the MQTT test | The target's broker port (MBP: `1884`). Without it `TestCCMQTTCatalogue` skips. |
| `JARVIS_CONTRACT_MQTT_USERNAME` / `JARVIS_CONTRACT_MQTT_PASSWORD` | if the broker has auth | The shared broker credential (CC's `MQTT_USERNAME`/`MQTT_PASSWORD`). Empty means anonymous. |
| `JARVIS_CONTRACT_SLOW_TIMEOUT` | no | Timeout for inference calls (LLM, STT, TTS synthesis). The default is `180s`. |

To get the admin token from the MBP without echoing it:

```sh
ssh alexanderberardi@10.0.0.103 'PATH=/opt/homebrew/bin:$PATH docker exec jarvis-auth-api printenv JARVIS_AUTH_ADMIN_TOKEN'
```

The CC admin key and the broker credential come from the CC container the same way
(`docker exec jarvis-command-center-jarvis-voice-api-1 printenv ADMIN_API_KEY MQTT_USERNAME MQTT_PASSWORD`).

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
| `cc_helpers.go` | The command-center node fixture (`NewCCNode`, `SharedCCNode`) and `CCAdminH`. |
| `cc_mqtt.go` | A minimal raw-bytes MQTT 3.1.1 client: the **fake node**. `DialMQTT` (clean session, broker credential from the env), `Subscribe` at QoS 1, `Next`/`NextTopic` to take a received PUBLISH, `Publish` (QoS 1) for response topics, `ExpectMQTT` to assert QoS 1, retain=false and the payload shape. |
| `media_helpers.go` | For the LLM/STT/TTS contracts: `SlowDo`/`SlowJSON` (raw bodies, slow timeout, keeps `Transfer-Encoding`), `ExpectChunked`, `ExpectMediaType`, `ExpectHeaderVal`; `MultipartBody`/`FormFile`/`FormField`; `SineWAV` and `ParseWAV`; strict SSE parsing (`ParseSSE` for a whole body, `OpenSSE`/`Next` for incremental reads). |

### Fixtures

| Fixture | Created by | Cleaned up by |
|---|---|---|
| App client | `POST /admin/app-clients` (admin token) | `POST /admin/app-clients/{id}/revoke`. Auth has no delete, so revoked `contract-*` rows accumulate on the target. |
| User + household | `POST /auth/register`, which creates the user's solo "My Home" household | `DELETE /auth/me`, which deletes the user, cascades the solo household and its nodes, and fans out a purge to CC and notifications. |
| Superuser | the above, plus `PUT /admin/users/{id}/superuser` and a fresh login | demote, then delete |
| Node | `POST /admin/nodes` in a user's household, with service grants | `DELETE /admin/nodes/{id}` (deactivate). A 404 after the household cascade is fine. |
| CC node | CC `POST /api/v0/admin/nodes` with `X-API-Key: <ADMIN_API_KEY>`. CC registers the node in auth itself (services `["jarvis-logs"]`, CC auto-granted) and inserts its own row, so it cannot adopt an existing auth node (auth 400s a duplicate id). | CC `DELETE /api/v0/admin/nodes/{id}` with the owner's JWT. That publishes factory-reset, deactivates in auth, and hard-deletes the row, its settings requests and snapshots, and (FK cascade) its callback jobs. Auth's admin deactivate follows as a backstop. |

The `Shared*` fixtures are created once per run, and `TestMain` tears them down. Tests that
mutate a fixture use `New*`, which cleans up with `t.Cleanup`.

Other data the CC tests create and how it goes away: inbox cards are read back and deleted
through notifications' `DELETE /api/v0/inbox/{id}` (user JWT); routines are deleted; signals are
posted with `ttl_seconds: 60` and removed by CC's 30-minute signal sweeper (there is no delete
route); ambient-noise results and verify entries are in-memory with a TTL; the K2 and
device-control `/tmp` files are consumed by the waiting request.

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

| CC node auth | `TestCCNodeAuth` | Node `X-API-Key` on CC: missing header → 400 CC validation body (`header -> x-api-key`); bare key → 401 `Invalid API Key`; wrong key `Invalid node credentials`; unknown `Node not found`; valid in auth but no CC row `Node not configured locally`; no CC grant `Node is not authorized to access service 'jarvis-command-center'`. CC user JWT: 401 `Missing or invalid Authorization header` + `WWW-Authenticate: Bearer`, `Invalid token`. Admin key: 401 `Invalid Admin API Key`. |
| CC nodes | `TestCCNodeCreateAndHeartbeat` | Admin create answers **200** (not 201); duplicate → 400 `Node already exists locally`. `GET /admin/nodes/{id}` NodeResponse key set (incl. `adapter_hash`). Heartbeat `{"status":"ok"}` with an empty or full body; fields reflected in NodeResponse. |
| | `TestCCNodeMQTTCredentials`, `TestCCNodeSettingsRequestsList` | `{username, password}` (string or null). Node's pending settings-request list; another node's path → 403 `Cannot access other node's requests`. |
| | `TestCCDateContext` | `/generate/date-context` against node-setup's strict `DateContext` model, plus the extra `relative_dates.last_night` and `time_expressions`. With a timezone: `user_timezone` string, `is_dst` bool. |
| Plugin API | `TestCCNodePlugin` | `/node/inbox-item` `{id, sent, withheld_by}`; household from the node; `metadata.node_id` injected with `setdefault` (caller wins); blank title → 200 `sent:false`. `/node/push-notification` `{sent, inbox_item_id, withheld_by}` and the delivered `confirmation` card (`metadata {command_name:"reminder", node_id, actions:[], draft:null}`). `/node/send-link`: non-http(s) → `sent:false`; the user-scoped `link` card with `metadata {url, type:"open_url"}`. The delivered cards are read back from notifications (`InboxItemResponse`). Validation and auth errors on all four. |
| | `TestCCNodeLLMChat` | `/node/llm/chat` → `{content: string}` (shape only, real model; skips if llm-proxy is unhealthy); a bad role → 400. |
| | `TestCCSignals` | `/signals` with node auth `{signal_id:int, mode:"open", proposed:false}`; household mismatch 403; no auth / bad node key 401 `Authentication required (X-Api-Key or X-Jarvis-App-Id/Key)`; app auth (LEGACY-BUG below). |
| MQTT | `TestCCMQTTCatalogue` | See the MQTT row in the checklist below. Each publish: exact topic, QoS 1, retain=false, payload key set. Plus the round trips around them: settings request → node GET/PUT snapshot → 409 on a second PUT → mobile 202-with-body pending and 200 fulfilled; K2 nudge → node pull (one-time; 403 `Node mismatch`) → ack → 200 `{ok, node_id, kid}`, and a failed ack → 502 with the node's error; callbacks create → node GET payload (404 for another node) → result → mobile status; `/commands/{rid}/verify` (mismatch false and not consumed, owner true, replay false); ambient-noise trigger/clamp/result/poll; `/nodes/{id}/actions` and routine run-now via `/device-control-results`; command-data request/response over MQTT; DELETE → factory-reset → `verify-reset` 200 then 404 `Invalid or expired reset token`. |

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

5. **`/signals` app-to-app auth is always 502** `Auth service unavailable` on the MBP: it reads
   jarvis-auth's URL from env `JARVIS_AUTH_URL` only (default `localhost:7701`, which inside the
   CC container is nothing), never discovery or `JARVIS_AUTH_BASE_URL`. Test:
   `TestCCSignals/app_auth`.
6. **`/generate/date-context` without a timezone** returns `user_timezone: null` and
   `is_dst: null`, which node-setup's strict model rejects; an unknown zone is a 500 with a
   non-FastAPI `{"error": …}` body. D40 Q11. Test: `TestCCDateContext`.
7. **`trusted: true` in published `action` and `routine` commands** (doc 05 §8.1, D4/D7). Test:
   `TestCCMQTTCatalogue/action`, `/routine_sync_and_run_now`.
8. **`/device-control-results/{rid}` is unauthenticated** (doc 05 §8.2, D4). Same tests.
9. **No household check** on the settings `/result` poll and on the ambient-noise trigger
   (doc 05 §8.4, D4/D5): a user from another household gets 202 / 200. Tests:
   `TestCCMQTTCatalogue/settings_request`, `/ambient_noise_and_verify`.
10. **Naive timestamps** on CC too (`last_seen`, settings requests, snapshots, callbacks), same
    as 3.

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
- [x] **MQTT topic catalogue**, partly: `TestCCMQTTCatalogue` with the fake node in
  `cc_mqtt.go`. Covered rows of docs/cc/05 §2.4: **1** `commands` (verbs `callback`,
  `update_node_config`, `preview_led_pattern`, `measure_ambient_noise`, `action`, `routine`),
  **2** `settings/request` (with and without `include_values`), **3** `k2/provision`, **4**
  `factory-reset` (the DELETE path, no `task_id`), **6** `routines/sync`, **13**
  `bluetooth-disconnect`, **14** `bluetooth-discoverable`, **20** `command-data/{op}` (op
  `commands`) with the node's `…/response/{correlation_id}`. It also asserts nothing else was
  published to the node. **Not covered:** 5 config/push, 7 device-scan, 8 device-list,
  9 device-state, 10 camera-credentials (D29 defers), 11 bluetooth-scan, 12 bluetooth-pair,
  16–18 package-*, 19 test-install (cut, D5), 21 context/query, 22 `jarvis/auth/+/ready`, and
  the verbs `tool_call`, `report_tools`, `enroll_voice`/`verify_voice`, `toggle_command`,
  `invalidate_device_cache`, `device_removed`. The client connects with `clean_session=true`
  (not the node's `false`) so nothing lingers on the broker. **Target gotcha:** CC must hold the
  broker's current credential. On 2026-10-06 the MBP's CC container predated an `.env` change,
  so mosquitto refused CC (CONNACK 5) and every publish was silently dropped; recreating the
  container fixed it. Original notes:
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
- [x] **Public plugin endpoints** on CC (`TestCCNodePlugin`, `TestCCNodeLLMChat`,
  `TestCCSignals`, callbacks in `TestCCMQTTCatalogue/callback`). `/node/llm/chat` is shape-only
  against the real model, not the fake LLM. Original notes: `/api/v0/node/inbox-item`,
  `/api/v0/node/push-notification`, `/api/v0/node/llm/chat`, `/api/v0/callbacks` and
  `/api/v0/signals`.
  - They need the CC node fixture (`X-API-Key: node_id:node_key` plus a CC `nodes` row). The
    `/signals` route also takes app credentials (node-or-app auth, docs/cc/00 §3.1).
  - `/node/llm/chat` needs the fake LLM to be deterministic. `push-notification` goes through
    notifications, so assert only CC's response, or add a notifications inbox read.
  - D4/D5/D6 change auth on some of these. Freeze the Python behaviour first, then mark the
    changes as divergences.
- [x] Node `X-API-Key` auth errors on CC (`TestCCNodeAuth`).
- [ ] `/internal/validate-household-access` and `/internal/users/batch`.

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
