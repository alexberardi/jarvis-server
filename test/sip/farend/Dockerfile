# The L1 lane's far ends: baresip (WAV in, WAV out; driven over ctrl_tcp) for calls with
# audio, SIPp for signalling-only scenarios, sox to put TTS audio at 8 kHz. Base pinned by
# digest; the Debian packages are trixie's (baresip 1.1.0, SIPp 3.7.3).
FROM debian:trixie-slim@sha256:62bd423aeee912639ca8ee681c2b762cb3e25137e0a92b20be455898a9f034ad
RUN apt-get update \
 && apt-get install -y --no-install-recommends baresip-core sip-tester sox \
 && rm -rf /var/lib/apt/lists/*
