# Desktop installs listen for "Hey Jarvis" by default; the status icon's menu is the voice control

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Desktop installs listen for "Hey Jarvis" by default; the status icon's menu is the voice control** (user). `voiceByDefault` is true; the menu's "Listen for \"Hey Jarvis\"" toggle turns it on and off live and saves agent.json `voice`. `--voice`/`--voice=false` and `config set voice` stay for scripts and development but leave the user-facing docs and help; status and errors point at the menu.
