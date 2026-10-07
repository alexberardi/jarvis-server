# llm 06: engines, labels and the model manager API

This doc is the contract for the admin UI (Models page, setup flow, hardware screen), for
track B (`llm.Service`) and for the voice module. It covers everything that turns "a model the
user picked" into "a process answering on 127.0.0.1" (or a path an in-binary model loads from).

Code:

| Package | What |
|---|---|
| `internal/modules/llm/engine` | engine kinds and pinned builds, GPU detection, build download, label config, the `Resolver` |
| `internal/modules/llm/models` | catalog, Hugging Face client, install jobs, store, voice models, HTTP API, `Stack` (wiring) |
| `internal/modules/llm/migrations/00002_models.sql` | `llm_models`, `llm_installs` |

Decisions it implements: LD1 (labels, engines keyed by path + load settings + placement,
shared), LD2 (remote OpenAI-compatible labels), LD3 (model manager, no auto-download), LD4
(vision follows labels), LD6/LD7 (embeddings on llama-server, MiniLM), D7 (STT = whisper-server, same machinery), PLAN §3.3 (GPU auto-detect, always
configurable, multi-GPU, Metal on macOS).

## 1. Concepts

**Engine kinds.** Two programs, one code path: `llama-server` (LLM, vision, embeddings) and
`whisper-server` (speech-to-text). Both are ggml programs with the same GPU flavours, release
layout and `/health` (503 while loading).

**Flavours.** `cpu`, `cuda`, `rocm`, `vulkan`, `metal`. A flavour is a build of a kind.

**Labels.** What callers ask for. Each label has a model kind and a settings prefix:

| Label | Kind | Runs | Settings prefix |
|---|---|---|---|
| `live` | llm | llama-server | `llm.live.*` |
| `background` | llm | llama-server | `llm.background.*` |
| `embeddings` | embedding | llama-server `--embedding --pooling mean` | `llm.embeddings.*` |
| `stt` | stt | whisper-server | `stt.*` |
| `tts` | tts | in-binary (sherpa-onnx Kokoro) | `tts.model` |
| `speaker` | speaker | in-binary (sherpa-onnx ERes2Net) | `speaker.model` |

**Engine instances (LD1).** For every engine label the resolver computes an instance key from
(kind, binary, flavour, model path, mmproj path, context, parallel, gpu_layers, devices,
split mode, tensor split, KV type, flash attention, embedding, extra args). Labels with equal
keys share one supervised process. The label name, model id and thinking default are not part
of the key. Give `live` and `background` the same model and settings (or set
`llm.background.engine=shared`) and one model is loaded once.

**Model kinds.** `llm`, `mmproj` (vision projector), `embedding`, `stt` (whisper ggml `.bin`),
`tts` (a directory), `speaker` (`.onnx`).

## 2. Pinned builds

| Kind | Build | Source |
|---|---|---|
| llama-server | `b11457` | `github.com/ggml-org/llama.cpp/releases/download/<build>/<asset>` |
| whisper-server | `b5454` | `github.com/ggml-org/whisper.cpp/releases/download/<build>/<asset>` |

| Platform | llama-server flavours | whisper-server flavours |
|---|---|---|
| linux/amd64 | cpu, cuda (12.8 + cudart), rocm (10.0), vulkan | cpu |
| linux/arm64 | cpu, cuda (13.4 + cudart), vulkan | cpu |
| darwin/arm64 | metal (also used as `cpu` with `-ngl 0`) | **none upstream** |
| windows/amd64 | cpu, cuda (12.4 + cudart), rocm, vulkan | cpu, cuda (12.4) |
| windows/arm64 | cpu, vulkan | cpu |

Every asset is pinned by name, size and sha256 (`engine.Releases`). A build installs to
`~/.jarvis/engines/<kind>/<build>-<flavour>/` (archive root stripped, CUDA runtime extracted
beside the binary, `LD_LIBRARY_PATH` set to that directory on Linux). A `.jarvis-complete`
marker is written last, so a half-extracted build is never used.

Upstream whisper.cpp ships no macOS or Linux GPU binaries. Until jarvis hosts its own
builds (point `stt.engine_base_url` at them), a macOS box sets `stt.engine_path` to an
installed binary, e.g. Homebrew's `whisper-server`, which is Metal. With `gpu_backend=auto`, a
kind without a build for the detected flavour falls back to its CPU build.

## 3. Settings

All live in the llm module's settings table (`llm_settings`), declared by
`models.SettingDefinitions()` (which includes `engine.SettingDefinitions()`). Every key is
`requires_reload`; the resolver picks a change up on its next pass (at once after an API
write, else within 15 s): only the affected engine restarts.

Per engine label (`<p>` = `llm.live`, `llm.background`, `llm.embeddings`, `stt`; "llm" = live
and background only):

| Key | Default | Notes |
|---|---|---|
| `<p>.engine` | `local` | `local`, `remote` (not stt), `off`; background also `shared` |
| `<p>.model` | `""` | installed model id or absolute path. Embeddings: empty = `all-minilm-l6-v2` once installed |
| `<p>.mmproj` (llm) | `""` | empty = the model's own projector if installed; `none`; or an id / path |
| `<p>.context` | 0 (embeddings 512) | `-c`, shared by the parallel slots; 0 = the model's catalog default, else 8192 |
| `<p>.parallel` | 1 (embeddings 4) | `-np` |
| `<p>.gpu_backend` | `auto` | `auto` = detection's flavour |
| `<p>.gpu_devices` | `""` | `"1"`, `"0,1"`: sets `CUDA_VISIBLE_DEVICES` / `HIP_VISIBLE_DEVICES` / `GGML_VK_VISIBLE_DEVICES` (an operator-set variable wins) |
| `<p>.split_mode`, `<p>.tensor_split` | `""` | `-sm`, `-ts` (llama only) |
| `<p>.gpu_layers` | 999 (embeddings 0) | `-ngl`; 0 on a GPU build adds `-dev none`; for stt 0 means `--no-gpu` |
| `<p>.kv_cache_type` (llm) | `f16` | `-ctk/-ctv` |
| `<p>.flash_attn` | `auto` | `-fa on/off` (stt: `off` → `-nfa`) |
| `<p>.extra_args` | `""` | shell-quoted extra flags |
| `<p>.remote_url`, `remote_model`, `remote_api_key` (secret), `remote_vision` (llm) | | LD2 remote endpoint |

Global:

| Key | Default |
|---|---|
| `llm.hf_token` (secret, env `HF_TOKEN`) | `""` |
| `llm.hf_endpoint` (env `HF_ENDPOINT`) | `https://huggingface.co` |
| `llm.engine_base_url`, `stt.engine_base_url` | the GitHub release URLs |
| `llm.engine_path`, `stt.engine_path` | `""`: an operator binary to use instead of a download |
| `tts.model`, `speaker.model` | `""` = the default (`kokoro-multi-lang-v1_0`, `eres2net-voxceleb-16k`) when installed, else any installed model of that kind |

## 4. Go API

### For track B: `Resolver.Resolve`

```go
ep, err := stack.Resolver.Resolve(ctx, "live") // "background", "embeddings", "stt"

type Endpoint struct {
    Label, Kind     // engine.KindLlama / KindWhisper
    BaseURL string  // llama & remote: OpenAI base incl. /v1 ("…/chat/completions", "…/embeddings");
                    // whisper: root URL (POST /inference)
    APIKey string   // Bearer; random per local llama-server instance; "" for whisper
    Model string    // send as "model"
    Vision, Embeddings, Remote bool
    ContextLength, Parallel int  // the engine's total -c and -np (0 for remote); per request = ContextLength/Parallel
    Engine string                // instance name; shared labels report the same one
    Degraded bool                // failing health checks, not yet restarted
}
```

Errors:

| Error | Meaning | Suggested HTTP |
|---|---|---|
| `engine.ErrNotConfigured` | no model, or `engine=off` | 503 `model_not_loaded`, state `not_configured` |
| `*engine.NotReadyError` (`errors.Is(err, engine.ErrNotReady)`) | `State`: `starting`, `restarting`, `draining`, `failed`, `stopped`, `fetching_engine`, `no_engine_build`, `misconfigured`; `Reason` has detail | 503 `model_not_loaded` with `state` |
| `engine.ErrUnknownLabel` | not a label | force to `live` before calling (02) |

`Resolve` is cheap (settings reads + a map lookup) and safe to call per request; it also
starts or swaps the label's engine when settings changed. Remote URLs are normalised
(`…/v1/chat/completions` → `…/v1`). `Resolver.Status(ctx)` returns every engine label's state
(`ready`, `degraded`, `remote`, `not_configured`, or a not-ready state) for `/health`;
`Resolver.Instances()` lists the processes (state, pid, port, labels, argv, last output lines)
for doctor.

### For the voice module

```go
vm, ok := stack.Manager.ModelPath(ctx, "tts")     // vm.Path: the Kokoro directory
vm, ok := stack.Manager.ModelPath(ctx, "speaker") // vm.Path: the .onnx file
```

`Manager.VoiceStatus(ctx)` reports both, with `problem` when missing.

### Wiring (llm module, track B; main untouched)

```go
// Register (thinking defaults, llm.<label>.reasoning_budget, stay in the module's own Definitions):
set, _ := settings.New(deps.DB, "llm", append(Definitions, models.SettingDefinitions()...), deps.Log)
set.Migrate(ctx)
m.stack = models.NewStack(deps, set)       // registers queue jobs llm.models.install, llm.engine.fetch
m.stack.Mount(mux, m.SettingsWrite)        // main sets it to settings.SuperuserGuard(auth.VerifyUser)
// Start:
m.stack.Start(ctx)                          // reconcile loop; stops every engine when ctx ends
```

The migration `00002_models.sql` is in the llm module's embedded migrations already.

## 5. HTTP API (llm listener, 7704)

Every route is behind the superuser guard (Bearer token of a superuser). Errors are
`{"detail": "..."}`; validation errors are 422.

### `GET /v1/hardware[?refresh=true]`

```json
{
  "hardware": {"os": "linux", "arch": "amd64", "flavour": "cuda",
    "devices": [{"backend": "cuda", "index": 0, "id": "CUDA0", "name": "NVIDIA GeForce RTX 3090", "total_mb": 24576, "free_mb": 23700}],
    "ignored": [], "sources": ["llama-server --list-devices (cuda)", "nvidia-smi"], "detected_at": "…"},
  "proposal": {"live": {"gpu_backend": "cuda", "gpu_devices": "1"}, "background": {…}, "embeddings": {…}},
  "builds": {"llama-server": {"build": "b11457", "flavours": ["cpu","cuda","rocm","vulkan"]}, "whisper-server": {…}},
  "installed": [{"kind": "llama-server", "build": "b11457", "flavour": "cuda", "path": "…", "pinned": true}],
  "engines": [{"name": "llama-9c93c3a3", "state": "healthy", "pid": 123, "port": 34633, "labels": ["background","live"], "args": […], "output": […]}],
  "voice": [{"label": "tts", "id": "kokoro-multi-lang-v1_0", "kind": "tts", "path": "…"}, {"label": "speaker", "problem": "not configured"}]
}
```

Detection (subprocess only, never in-process): `llama-server --list-devices` of each installed
GPU build first, then `nvidia-smi`, `rocm-smi --json`, `vulkaninfo --summary`; on Apple
silicon `sysctl` (Metal device ≈ 70% of RAM). iGPUs (APU gfx list, llvmpipe, "integrated")
land in `ignored`. Proposal: one GPU → both LLM labels on it; two or more → live on the
largest, background on the next; Apple → Metal; none → CPU. Detection only proposes; the
settings decide.

### `POST /v1/hardware/engines` `{"kind": "llama-server", "flavour": "vulkan"}`

Queues a build download (`flavour` empty = detected). 202 `{job_id}`, or 200 `{installed: true, path}`.

### `GET /v1/models/catalog`

```json
{"models": [{"id": "qwen3-8b", "display": "Qwen 3 8B (Q4_K_M)", "kind": "llm", "repo": "Qwen/Qwen3-8B-GGUF",
   "revision": "<commit>", "file": "Qwen3-8B-Q4_K_M.gguf", "size": 5027783488, "sha256": "…",
   "context_default": 16384, "context_max": 40960, "kv_bytes_per_token": 147456,
   "prompt_provider": "Qwen3_8B_Compressed", "thinking": true, "tags": ["live","background"],
   "mmproj": "…(when it has one)", "url"/"archive": "(direct-URL models)",
   "fit": {"verdict": "fits", "needed_mb": 8412, "context": 16384, "device": "RTX 3090", "device_mb": 24576, "free_mb": 23700, "kv_estimated": false},
   "installed": false, "state": ""}],
 "recommended": {"live": "qwen3.8-27b", "background": "qwen3.8-27b", "embeddings": "all-minilm-l6-v2", "stt": "whisper-large-v3-turbo", "tts": "kokoro-multi-lang-v1_0", "speaker": "eres2net-voxceleb-16k"},
 "hardware": {…}}
```

Catalog (D12): Qwen3 4B/8B/14B, Qwen3.5-9B (+ projector), Qwen3.8-27B UD-Q4_K_M (+ projector,
prod's model), all-MiniLM-L6-v2 F16, whisper large-v3-turbo / large-v3-turbo-q5_0 / small.en /
base.en, Kokoro multi-lang v1.0, ERes2Net. Hugging Face entries pin a commit.

Fit verdicts: `fits`, `tight` (within 10% of the largest card), `split` (only with tensor
split across cards), `too_big`, `cpu` (no usable GPU), `in_binary` (tts/speaker). The estimate
is weights + KV (`kv_bytes_per_token` × context, f16) + ~3% + 600 MB, per card, never summed.

### `GET /v1/models/hf/{owner}/{name}[?revision=…&context=…]`

Lists a pasted repo's installable files, with size, guessed kind, quant and fit; split GGUFs
are grouped (`shards`). 403 = gated (set `llm.hf_token`), 404 = no such repo.

```json
{"repo": "unsloth/Qwen3-8B-GGUF", "revision": "<commit>", "gated": false,
 "files": [{"file": "Qwen3-8B-Q4_K_M.gguf", "kind": "llm", "quant": "Q4_K_M", "size": 5027783488,
            "shards": [{"name": "…", "size": …, "sha256": "…"}], "fit": {…}}]}
```

### `POST /v1/models/install`

```json
{"catalog_id": "qwen3.8-27b", "with_mmproj": true, "assign": ["live", "background"], "gpu_backend": "auto"}
{"repo": "unsloth/Qwen3-8B-GGUF", "file": "Qwen3-8B-UD-Q4_K_XL.gguf", "revision": "", "kind": "llm",
 "id": "", "display": "", "mmproj_file": "", "context_default": 16384, "assign": ["live"]}
```

202 `{"install": Install, "existing": false}`; 200 with `existing: true` when that model is
already installing. One durable job fetches, in order, the engine build the model needs
(`gpu_backend`, else the first assigned label's setting, else detection; `note` explains a
CPU fallback or a missing build), the projector, then the model, then assigns the labels.
Downloads resume (HTTP Range into `~/.jarvis/models/.partial/`, sha256 state saved every
64 MB), are size- and sha256-checked, and are renamed into place only when verified. Long
downloads run in 8-minute job slices, so after a crash or restart the download resumes
within one lease (10 min). Auth/404 errors fail at once; others retry with backoff.

Layout: `~/.jarvis/models/<owner>--<repo>/<file>` (Hugging Face), `~/.jarvis/models/<id>/`
(direct-URL models; archives such as Kokoro are extracted there).

### `GET /v1/models/installs`, `GET /v1/models/installs/{id}`

```json
{"id": 7, "model_id": "qwen3.8-27b", "mmproj_id": "qwen3.8-27b-mmproj", "engine_kind": "llama-server",
 "engine_flavour": "cuda", "assign": ["live"], "state": "running", "phase": "model",
 "bytes_total": 18158418249, "bytes_done": 5120000000, "error": "", "note": "", "job_id": 31, …}
```

`state`: queued, running, done, failed, cancelled. `phase`: engine, mmproj, model, done.
`error` while running is the last transient failure being retried. Poll about once a second.

### `POST /v1/models/installs/{id}/cancel`

Cancels a queued or running install and deletes partial downloads of models that never
finished. 409 when already final.

### `GET /v1/models/installed`

```json
{"models": [{"id": "qwen3.8-27b", "kind": "llm", "display": "…", "catalog_id": "…", "repo": "…", "revision": "…",
   "files": [{"name": "…", "size": …, "sha256": "…"}], "path": "…", "size": …, "mmproj_id": "…",
   "context_default": 16384, "prompt_provider": "Qwen3_14B_Compressed", "state": "ready", "bytes_done": …,
   "external": false, "labels": ["live", "background"]}],
 "disk_bytes": 17392047712, "dir": "/home/…/.jarvis/models"}
```

Every kind is listed here, voice models included.

### `POST /v1/models/installed` (register in place)

`{"path": "/abs/file.gguf", "kind": "llm", "id": "", "display": "", "mmproj_id": "", "context_default": 0}`
→ 201 Model with `external: true`. Deleting it later never removes the file (05 §4).

### `DELETE /v1/models/installed/{id}[?force=true]`

204. 409 when a label uses it (lists them; `force=true` clears those labels) or while it is
installing. Removes the files (whole directory for archive models).

### `GET /v1/models/labels`

```json
{"labels": [{"label": "live", "state": "ready", "reason": "",
   "config": {"engine": "local", "model": "qwen3.8-27b", "model_path": "…", "mmproj": "…", "context": 13312, "parallel": 4,
              "gpu_backend": "cuda", "gpu_devices": "1", …, "shared_with": "", "problem": ""},
   "endpoint": {"base_url": "http://127.0.0.1:41234/v1", "model": "qwen3.8-27b", "vision": true, "engine": "llama-1a2b3c4d", …}}],
 "engines": [InstanceStatus…], "voice": [VoiceModel…], "proposal": {…}, "recommend": {…}}
```

`config` never includes the remote API key. "Which labels can see images" (LD4) is
`endpoint.vision`.

### `PUT /v1/models/labels`

```json
{"live": {"model": "qwen3.8-27b", "context": 13312, "parallel": 4, "gpu_devices": "1"},
 "background": {"model": "qwen3.8-27b", "context": 131072, "kv_cache_type": "q8_0", "flash_attn": "on", "gpu_devices": "0"},
 "stt": {"model": "whisper-large-v3-turbo"}, "tts": {"model": "kokoro-multi-lang-v1_0"}}
```

Fields are the per-label keys without the prefix (§3). The whole body is validated before
anything is written (types, options, model exists and is the right kind, flavour names,
quoting in `extra_args`); 422 otherwise. Answers like `GET`. Voice labels take only `model`.

## 6. Tests

- Unit (`-race`): parsers on captured `--list-devices` / `nvidia-smi` / `rocm-smi` /
  `vulkaninfo` output, detection and proposals per platform, the G9 argv table (prod's two
  labels, split, Metal, CPU, embeddings, whisper), instance-key sharing, resumable download
  (cut connection, no-Range server, saved hash state, checksum/size mismatch, 401/404),
  build fetch (CUDA + runtime, zip, traversal and symlink escapes), the resolver against a
  fake engine (this test binary re-executed: sharing, swap, remote, missing build → fetch,
  loading, crash restart, failed-engine retry, stt, embeddings), and the manager against an
  `httptest` Hugging Face + release server (catalog install with projector + engine +
  assignment, split GGUF with slices and a cut connection, failures, cancel, delete/force,
  register, label validation, voice models, the HTTP API end to end through `NewStack`).
- Integration, build tag `llamaserver`: real pinned CPU builds of llama-server and
  whisper-server, `stories15M` (completion), MiniLM (384-d embedding) and whisper tiny.en
  (transcription), installed through the manager; `JARVIS_ENGINE_TEST_GPU=1` adds a live
  label on the detected GPU build.
