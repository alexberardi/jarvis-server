# Mobile routines have Simple instructions and the existing Advanced builder

- **Date:** 2026-11-09 (user)

Add a Simple / Advanced control at the top of the mobile routine editor. Advanced
retains the current step builder, and existing routines remain Advanced. Simple
retains the routine's name and trigger phrases, with one text box containing the
instruction to execute.

A matching shortcut expands into that saved instruction and enters the normal
model-and-tools flow. The model interprets the instruction each time the routine
runs; saving does not compile it into fixed command steps. Simple routines support
voice triggers, Run in the app and schedules. The purpose is to make routine creation
accessible without requiring knowledge of command names or arguments.

Save makes whichever mode is currently selected the active definition for later
runs. The other mode's content is retained for switching back; there is no automatic
conversion between text and steps. Selecting a different mode in the editor alone
does not change the live routine, and there is no separate Publish action. The user
confirmed this after asking what retaining separate "drafts" meant.

The user also explicitly endorsed voice follow-up questions when more information
is needed. Use the normal conversation flow to ask and continue.

Implementation uses failed/partial results for unattended clarification, with a useful
message directing the user to voice or a more specific instruction. Capability detection
and inert old-node definitions keep mixed versions safe. Details and verification live
in the [specification](../cc/simple-routines.md). Production deployment and mobile release
are separate from this implementation.
