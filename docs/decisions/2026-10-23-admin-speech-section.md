# Admin Speech section: routes, test clip, where the UI lives

Date: 2026-10-23. Context: remote speech piece 5/5 (docs/llm/07-remote-speech.md §4, §6 step 5),
item #495.

## Two new routes, both superuser

- `GET /api/speech/presets` → `{"stt": [Preset...], "tts": [Preset...]}`, the static catalog
  from `internal/voice/speechpresets` served as is. Presets are code, not settings (07 §1):
  picking one only fills the admin's fields, so the route is read-only and carries no secrets.
- `POST /api/speech/stt-test` (no body) → `{"text", "engine", "latency_ms", "clip_says"}`:
  transcribes a clip bundled in the binary through `stt.Module.Transcribe` (the same in-process
  path command-center uses), with the speaker pass off. `engine` is `stt.Result.Engine`
  (`local`, `remote`, `local-cpu`), so the button shows whether the fallback answered. Engine
  not ready → 409; a provider/engine failure → 502 with the error text (never the key: the stt
  module's remote errors already exclude it).

The TTS test reuses `POST /api/tts/sample` (no third route): the response gains an
`X-Speech-Engine` header (`local`, `remote`, `local-cpu`), read from the tts module's fallback
gate (`FallbackStatus().LastPath`) right after the render. The gate is global, so a concurrent
voice turn could race the header; accepted for a test button. Latency is measured client-side.

## The bundled clip

`internal/modules/admin/speech_test_clip.wav` is a copy of
`internal/voice/oww/testdata/tell_me_a_joke_george.wav`: TTS-rendered "tell me a joke"
(en_GB George), 16 kHz mono s16, ~4 s, ~133 KB embedded with `go:embed`. Chosen because it
already lives in the repo, contains no real person's voice, and its phrase is short enough to
eyeball against the transcript (the route returns `clip_says` so the UI can show the expected
text). A copy, not a cross-package embed: `go:embed` cannot reach another package's testdata,
and the oww tests keep owning their file. The doc said "~2 s"; the existing 4 s clip was
preferred over recording a new asset.

## Where the UI lives

The Speech section stays on `/admin/models` (07 §4 — no new page, `e2e/coverage.json`'s
existing `/admin/models` entry covers it):

- STT lives in the existing `stt` label card (`LabelsEditor`), which already edits
  engine/URL/model/key: the preset picker, the dialect (openai|elevenlabs), the fallback
  setting and the test button are added there rather than duplicating the form elsewhere.
- TTS gets a card of its own (`SpeechCard`) on the Jobs tab, because remote TTS is settings
  (`tts.provider`, `tts.remote_*`), not an engine label. Voice comes from `GET /api/tts/voices`
  (provider-aware; free text when the provider lists none) and is written to the `setting` that
  response names — which also fixes `web/admin/src/api/tts.ts`, which hardcoded
  `tts.kokoro_voice` and would have written the wrong key with `tts.provider=remote`.
