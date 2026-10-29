# What's on screen is matched first

- **Date:** 2026-10-28 (user)

The page is inspected without drawing an overlay. "Click Images", "go to videos", and
"type dogs into Search" act on the matching visible control, with a unique name match or a
clearly distinct sound-alike resolved directly.

Stage 1 of the funnel ([VOICE-FLOWS.md](../agent/VOICE-FLOWS.md) Q27) asks what's on the page
**before** opening a site, switching to a tab or window, or launching an app by name. The things
on screen are a short list that can be matched quickly and for certain; a site or app could be
called anything. So "go to Google" with a Google link on the page clicks it, and only a name
nothing on screen matches goes on to the web, tab, window and app handlers. A "click X" that
nothing matches is said back on the island, never passed to a free model turn (it read "click
line over" as a smart-home device).

**Later the same day (user):** never draw an overlay. While a voice turn listens, the extension
reads a bounded snapshot of visible controls, including role, viewport position and short nearby
text for semantic containers. Direct name and clear sound-alike matches are tried first. If those
do not settle the target, the live model may choose from the snapshot through jarvisd's node chat with no tools. It
must return a valid element id and confidence of at least 0.75 to act; lower confidence asks the
user to repeat the command with another detail. Page text is untrusted, and ids not in the
snapshot are ignored. Risky actions still use the existing confirmation flow after a target is
resolved.
