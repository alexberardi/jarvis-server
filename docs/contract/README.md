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

Not frozen on purpose:

- Loki rejecting old timestamps with 502 (`Failed to push logs to Loki`). That is Loki's behaviour, not a contract.
- The current `auth.algorithm`. Tokens may be HS256 or RS256.
- `needs_setup`'s value.
- The LLM health `model_service` blob.

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

Seen while reading but not observable black-box: logs' node routes enrich entries with
`context.user_id` from validate-node's `user_id`, a field validate-node never returns, so it is
always `null` in Loki.

## Remaining wire contracts (PLAN §6 Phase 0 item 4)

- [ ] **LLM stream frames.** llm-proxy's OpenAI-compatible `POST /v1/chat/completions` with
  `stream:true`. Freeze the SSE framing (`data: {…}\n\n`, the chunk object keys, `delta`
  shape, the final `finish_reason`, the `data: [DONE]` terminator) and the non-stream response
  key set. Also the app-auth errors.
  - Against the MBP it can use the real Qwen3-8B, with `max_tokens` small and `temperature 0`.
    Assert framing only, never content.
  - For CC's own streaming (the voice path and `/node/llm/chat`), CC needs a **fake LLM**: an
    OpenAI-compatible scripted server in `contract/fakes/llm`. Point CC at it via
    `jarvis-llm-proxy-api` in `/services`, or via a CC setting. That requires re-registering the
    service on the target, so do it only on a dev box and restore it afterwards.
- [ ] **Voice PCM stream headers.** jarvis-tts `POST /speak/stream`, and CC's media proxy
  (`app/api/media.py`), which re-emits them. Covers `X-Audio-Sample-Rate`, `X-Audio-Channels`,
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
