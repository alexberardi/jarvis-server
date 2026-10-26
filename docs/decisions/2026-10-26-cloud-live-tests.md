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
