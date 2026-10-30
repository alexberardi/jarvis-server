# Voice enrollment is shared across a member's household devices

- **Date:** 2026-10-29 (user request; implementation design)

A household member can record enrollment samples on multiple Pi nodes and desktop
agents. Adding a device must retain the member's existing samples. There is no
device count limit or single enrolled-node assignment: the existing profile key is
`(household_id, user_id)`, and the recording device supplies its microphone, not a
separate identity. Other members and other households keep their own profiles.

The mobile app appends either one sample or a three-take set from the selected
device. Replacing a profile requires explicitly deleting it; selecting another
microphone never deletes previous samples. The existing storage and sample
quality limits still apply. Enrollment does not enable household speaker
recognition by itself; [voice identity](../cc/06-media-and-voice-identity.md) owns
that setting and the matching rules.

Desktop agents advertise `voice_enrollment` in their heartbeat. Missing means an
older agent needs updating; false means this build cannot run the voice pipeline
(currently Windows); true means enrollment and verification commands are handled.
The microphone must still be available through the running voice pipeline. Legacy
Pi nodes retain their existing enrollment support without requiring this flag.

This supersedes only the desktop **enrollment exclusion** in the
[October 14 desktop identity decision](2026-10-14-desktop-voice-speaks-as-its-owner.md).
The desktop's signed-in identity for ordinary commands is retained in this change;
recording support does not change command identity or authorization. A recording
requested from mobile belongs to the mobile caller, even when the desktop is
signed in as another household member.
