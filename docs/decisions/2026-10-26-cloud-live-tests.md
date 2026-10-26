# Live cloud tests for every provider, on pull requests too

- **Date:** 2026-10-26 (user: "build out full AI tests against chatgpt and gemini before we cut a release")

The Anthropic-only live check (`anthropic-live.yml`) becomes `cloud-live.yml`, which runs the
same checks against Anthropic, OpenAI and Gemini (each whose secret is set), plus a
command-center test of real voice turns through the `Cloud` prompt with a real node's tools.
Every OpenAI and Gemini bug found by hand on 2026-10-25/26 (`chat_template_kwargs`,
`max_tokens`, `temperature`, empty replies, the `/v1` path, `minimal` effort, missing thought
signatures, 503s) was a request a provider refuses, which only a live call shows.

It now also runs on pull requests from this repository that touch `internal/modules/llm`,
`internal/modules/cc` or the prompt goldens, so a refusal is found before merge rather than
after (the old check ran on main only). Fork PRs get no secrets and are skipped. The models are
the providers' smallest, so a run costs cents. Supersedes the "never on pull requests" note of
the 2026-10-12 Anthropic live check.

A check that fails while the provider is still answering 429 or 5xx after jarvisd's retries
skips instead (`livetest.Busy`): the first CI run hit Google's "experiencing high demand"
503s, which are the provider's capacity, not a request it refused. A 400 or a malformed tool
call still fails. The streamed voice turn accepts any well-formed tool call, since a model may
answer the weather with `quick_search` (gpt-5-nano did); which tool a model picks is a prompt
question, not a format one.

The first runs found two bugs: voice turns on an Anthropic remote sent Claude no tools (cc's
raw-only tools were dropped by `anthropicTools`), and Gemini ends a tool-call reply with
`finish_reason: "stop"` (now reported as `tool_calls`).
