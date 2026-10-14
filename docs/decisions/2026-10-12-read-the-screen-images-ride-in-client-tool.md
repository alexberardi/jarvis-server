# Read the screen: images ride in client tool results

- **Date:** 2026-10-12
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Read the screen: images ride in client tool results** ([agent/DESIGN.md §12](../agent/DESIGN.md), [cc/chat-images.md §10](../cc/chat-images.md)). `desktop_read_screen` (grim on Hyprland/sway, `screencapture` on macOS; window by default, 1568 px JPEG) attaches the shot as `image_attachments` in its output; command-center lifts it out on arrival and shows it to the live model on that tool message only, never storing it (dropped at commit; OCR text when live has no vision). Chosen over a separate upload endpoint. User approved 2026-10-11.
