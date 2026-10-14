# Image parameters on any tool; photo tools fixed per conversation

- **Date:** 2026-10-10
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Image parameters on any tool; photo tools fixed per conversation** ([cc/chat-images.md §9](../cc/chat-images.md), CI7). A tool is a photo tool by its schema (`x-jarvis-type: image`), server or node; the hard-coded `PhotoTools` list is gone. Photo tools are offered only in vision-capable mobile chats, fixed at warmup (prefix cache), and refuse with `no_image` until a photo arrives, rather than appearing at the first photo (that would re-prefill the whole prompt on the photo turn). jarvisd swaps the model's numbers for `{mime,data}` only in the node's MQTT `tool_call`; history, traces and transcripts keep the numbers. The marker is stripped before the LLM.
