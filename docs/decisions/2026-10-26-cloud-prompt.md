# One vendor-neutral prompt for remote models: `Cloud`

- **Date:** 2026-10-26 (user: "a prompt provider for both anthropic and google, or make the chatgpt one more generically named")

The `ChatGPTOpenAI` built-in had nothing OpenAI-specific in it (it is the 9B native prompt with
`{{tools}}` for `{{tools_compact}}`), so it is renamed `Cloud` rather than copied per vendor,
and a remote live model now derives it (`LiveModelTraits`), so an Anthropic, Gemini or OpenAI
live label talks without anyone picking a prompt. `ChatGPTOpenAI` stays accepted as an alias:
installs and households that stored it, and the legacy goldens in `fixtures/golden/prompts/`,
which keep the name the Python provider had. A vendor gets its own built-in only if a model
needs different text. See `../cc/prompt-contract.md` §10.
