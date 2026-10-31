# Local touch display with centrally inherited configuration

- **Date:** 2026-10-23, user plan and follow-up instructions.

The display is for any Linux device with a screen, not one model of hardware. The server owns display defaults and sparse node overrides. Built-ins, defaults and node values merge
in that order, and a content revision covers the result and the protected PIN verifier. Nodes fetch,
cache and acknowledge through their existing authentication. Themes receive a separate versioned
message API and cannot perform privileged operations directly. See [Use the touch display](../agent/touch-display.md).

The user replaced the planned six-digit PIN with **4 to 20 numeric digits**, requested, for the pilot Surface, a dedicated
kiosk user with desktop automatic login, and requested a circular waveform reacting to spoken
replies, subsequently clarified to occupy the full screen during replies. The waveform uses existing voice amplitude callbacks rather than adding audio capture.
The pilot deployment keeps a reversible copy of the original voice installation and runs only
one microphone owner at a time. Cast V2 reception is experimental and separately switchable.

The subsequent visual reference uses fine glowing filaments around the ring. The user asked for
fixed radial bursts resembling solar flares, with no wave travelling around the circle, and an idle
clock occupying approximately 75% of the display. Both preserve the trusted exit control.

The user subsequently requested centrally configurable strand appearance with node overrides and a
more pronounced effect. `waveform_colour` and `waveform_emphasis` use the existing sparse settings
and revision delivery, with 150% emphasis by default. A 50% larger Home clock is independent of the
75% idle clock. Missing appearance fields remain omitted when validating older offline caches.
