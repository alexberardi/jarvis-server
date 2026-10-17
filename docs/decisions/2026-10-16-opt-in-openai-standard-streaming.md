# Opt-in standard OpenAI streaming on a separate route

- **Date:** 2026-10-16 (the user asked for an opt-in mode; the selector and the details below are
  the implementer's)

Stock OpenAI clients (OpenCode, the AI SDK's openai-compatible provider, the openai Python and JS
SDKs) could not stream from jarvisd, because `/v1/chat/completions` with `stream: true` sends the
frozen legacy Jarvis frames (`{"delta"}`, `{"done":true,…}`, no `[DONE]`;
[02 §3.2](../llm/02-api-and-streaming.md)). The loop's Qwen trial had to put a non-streaming shim
in front of prod to work around it.

- **Selector: a separate route, `POST /openai/v1/chat/completions`**, next to the legacy one on
  the llm listener, with the same app credentials, body parsing and request id. A client only sets
  `baseURL = http://<host>:7704/openai/v1`. A request header was rejected: every client would
  need a custom header configured, and some (OpenCode's provider config) make that awkward. A body
  flag would be read by nothing standard. The legacy route is untouched: its bytes are pinned by
  `TestStreamLegacyBytes`, and `contract/llm_test.go` is unchanged.
- **Only the stream differs.** Non-stream answers on the new route are the legacy completion
  (the extra `date_keys` key is ignored by OpenAI clients). Errors before the stream opens stay
  the legacy HTTP errors.
- **The stream** follows OpenAI's: a role chunk, content deltas as they arrive, each tool call
  whole in its own `delta.tool_calls` chunk (calls reach the HTTP layer already merged), a final
  chunk with `finish_reason`, then `[DONE]`. With `stream_options.include_usage`, usage comes in
  a trailing `choices: []` chunk with `usage: null` on the earlier ones, exactly as OpenAI does,
  because every client already parses that shape.
- **Errors and cancels after the 200** send `{"error":{"message","type"}}` (`server_error`, or
  `cancelled`) and end **without `[DONE]`**, so an agent never takes a cut-off reply for a
  finished one. The openai SDKs raise on an `error` payload; the AI SDK reports an error part.
- **Not mirrored under `/openai/v1`:** models, embeddings, cancel. Cancel by request id stays
  `POST /v1/chat/completions/cancel/{id}` for both routes. Add a route there only when a client
  needs it.
- **Not on the admin gateway.** Like the legacy route, it is not inference exposed through
  `/api/llm` (`TestPassThroughIsAnAllowList`).
