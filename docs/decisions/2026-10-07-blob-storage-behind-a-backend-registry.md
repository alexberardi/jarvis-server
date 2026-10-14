# Blob storage behind a backend registry

- **Date:** 2026-10-07
- **Moved from** the STATUS.md decision log on 2026-10-14.

**Blob storage behind a backend registry** (user: facade so another provider is easy later): `blob.Store` (S3-shaped) was already the only API callers use; `blob.Open(JARVIS_BLOB_STORE)` now picks a backend by URL scheme (`file://…` or a bare path = FS, default `<home>/blobs`), `blob.Register(scheme, Opener)` adds one, and `blobtest.Run` is the conformance suite a new backend must pass.
