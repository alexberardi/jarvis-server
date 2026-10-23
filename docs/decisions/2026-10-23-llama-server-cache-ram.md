# llama-server's host prompt cache: jarvisd sizes it, min(2048 MiB, RAM/10), floor 256, 0 for embeddings

- **Date:** 2026-10-23 (agent, loop #547; the user asked for "e.g. min(2048 MiB, 10% of RAM)" and
  left the edges to the agent)

llama.cpp b11457's `llama-server` keeps a server-side prompt cache in host RAM whose size is
`-cram, --cache-ram N`, **default 8192 MiB** (ggml-org/llama.cpp#16391). jarvisd never set the
flag, so every install paid up to 8 GiB of anonymous host RAM per llama engine on top of the
model's VRAM — measured on the devbox (31 GiB RAM, RTX 12 GiB): the Qwen3.5-9B engine, `-ngl 999`
with every layer on the GPU, held `RssAnon 8545144 kB`. On a 16 GiB mini PC that swaps.

`instanceKey.args` now always passes `--cache-ram`
([../llm/01-backends-and-engines.md](../llm/01-backends-and-engines.md) §3.4). The choices a later
reader would otherwise rediscover:

**The cache stays on for chat labels.** Turning it off would be the smaller change, but it is
what makes an alternating conversation cheap: live, background and computer turns share one slot
and one system prompt, and the cache saves re-processing that prompt on every switch. So only its
*size* is ours to pick.

**`min(2048, RAM/10)` MiB, with a 256 MiB floor.** The cap is the user's figure: 2 GiB holds many
turns' worth of prompt and is a tenth of the 16 GiB box we care about. The tenth keeps a small
host proportional (a 4 GiB Pi gets 409 MiB). The floor keeps the cache useful on a tiny box
(a 2 GiB one would otherwise get 204 MiB); `--cache-ram` is a ceiling, not a reservation, so a
floor costs nothing unless the cache is actually used.

**Unknown RAM also means the floor, 256 MiB.** `sysinfo.TotalMemory()` returns 0 when the OS
won't say (and on the platforms `sysinfo_other.go` covers). A tenth of 0 would be
`--cache-ram 0`, i.e. prompt caching *off* — the opposite of the intent — so the unknown host
gets the floor rather than a second, separate fallback constant.

**Embeddings get `--cache-ram 0`.** An `--embedding` engine never reuses a prompt, and
`--cache-idle-slots` (on by default) requires `cache-ram`, so 0 switches the whole thing off for
that engine. It is harmless either way and records the intent.

**`extra_args` wins by omission, not by ordering.** llama-server accepts the flag twice and takes
the last, so an operator's `--cache-ram` in `extra_args` would already win (as
`--chat-template-file` does, §3.4). We still *skip* emitting ours when `extra_args` contains the
exact token `--cache-ram` or `-cram`, so that `ps` and the admin Status argv show one value — the
one in force. An exact-token scan is enough because llama-server rejects the `--cache-ram=N`
equals form.

**Read from the host, not from the label.** `hostMemory` is a package var in
`internal/modules/llm/engine/spec.go` (`var hostMemory = sysinfo.TotalMemory`, the pattern of
`internal/update/disk.go`'s `diskFree`), so the argv tests pin it and never read the real box. The
value is deliberately **not** part of `instanceKey`: it is host-constant, so it cannot split one
model into two engines (LD1), and `keyFor` is untouched.
