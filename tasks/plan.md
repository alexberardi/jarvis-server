# Node settings implementation

Approved scope: docs/agent/NODE-SETTINGS.md. Implement without another planning approval.

Use the existing verified command/result channel for non-secret readback and
confirmed writes. Share an explicit schema between the Go node and server;
construct responses from allowed scalar values, never from encrypted snapshots.
Local desktop settings use the protected setup page and user-only control socket.
Older Pi nodes retain explicit unconfirmed updates through the existing command.

Order and verification are tracked in todo.md. New API access registrations hold
merge for review. No installed assistant or production operations.
