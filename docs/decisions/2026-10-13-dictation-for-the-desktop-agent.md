# Dictation for the desktop agent

- **Date:** 2026-10-13
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Dictation for the desktop agent** ([agent/DESIGN.md §14](../agent/DESIGN.md)): `desktop_type_text {text, app?, submit?}`, `desktop_press_key {key}` (Enter/Escape/Tab/Shift+Tab/Space/arrows: answers native OK/Cancel dialogs), `browser_fill {field?, text, submit?}`, `browser_click {text}`, and an opt-in confirmation (`--confirm-dictation off|submit|all`, `desktop_confirm`, asked through `request_validation` so voice, tap and typed answers all work; 60 s → cancel). Typing: wtype / ydotool / xdotool, CGEvent (macOS), SendInput (Windows), never the clipboard. A named app is focused and the focus confirmed before anything is typed; one match or `ambiguous_window`. The island shows what is typed (`Island.Note`). Verified on a headless nested sway, headless Chromium/Firefox and through a throwaway jarvisd; macOS/Windows built and vetted only. Extension 0.2.0 (store upload + re-sign needed).

**Update 2026-10-17 (#117, user's decision):** `--confirm-dictation` now defaults to `submit`, not `off`. Text the model read from a web page or the screen can steer it, and the untrusted fence is only advice, so a send (Enter, a click, a submitting fill) waits for the user's yes; `off` must be chosen. A saved `confirm_dictation` in `agent.json` still wins. A server-side gate for `is_risky` calls after untrusted content is a separate item.
