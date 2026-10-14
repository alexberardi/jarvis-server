# macOS release binary: Developer ID + hardened runtime + notarized, entitlement disable-library-validation only

- **Date:** 2026-10-08
- **Moved from** the STATUS.md decision log on 2026-10-14.

**macOS release binary: Developer ID + hardened runtime + notarized, entitlement `disable-library-validation` only** (ID9). Signed before `SHA256SUMS` so the minisig covers the notarized binary; identifier `net.jarvisautomation.jarvisd`; no JIT entitlements (onnxruntime CPU needs none, verified on the MBP). A bare binary is not stapled (online ticket check).
