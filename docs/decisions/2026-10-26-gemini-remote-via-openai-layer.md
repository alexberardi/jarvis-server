# Gemini remotes go through Google's OpenAI-compatible layer

- **Date:** 2026-10-26 (user asked for out-of-the-box Google API support, like Anthropic's)

A remote label pointed at `https://generativelanguage.googleapis.com` works with no other
setting: jarvisd fills in Google's OpenAI-compatible base (`/v1beta/openai`) and speaks the
existing OpenAI dialect to it. No native `generateContent` translator was written: Google's
layer covers chat, streaming, tools, images and embeddings, which is everything a label uses,
whereas Anthropic's needed the Messages API
([2026-10-12-remote-labels-can-speak-the-anthropic-messages-api.md](2026-10-12-remote-labels-can-speak-the-anthropic-messages-api.md)).
Gemini models get no token cap and `reasoning_effort: "minimal"` for thinking off, for the same
reason as OpenAI's reasoning models (#508). Details: `../llm/06-model-manager-api.md`, "Gemini remote".
