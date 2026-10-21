# jarvisd in a container, built from the signed release archive. There is no published image yet.
# The llama.cpp builds jarvisd downloads at first run need glibc 2.38 or newer, so use Debian 13 (trixie) or Ubuntu 24.04;
# Debian 12 is too old and Alpine (musl) cannot run them. The archive is checked against a pinned SHA-256 at build time.
# To change version, set JARVISD_VERSION and both checksums together in .env (they come from the release's SHA256SUMS).
FROM debian:trixie-slim

ARG TARGETARCH
ARG JARVISD_VERSION
ARG JARVISD_SHA256_AMD64
ARG JARVISD_SHA256_ARM64

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl tzdata libstdc++6 libgomp1 libasound2t64 \
 && rm -rf /var/lib/apt/lists/*

RUN set -eu; \
    case "${TARGETARCH}" in \
      amd64) sha="${JARVISD_SHA256_AMD64}" ;; \
      arm64) sha="${JARVISD_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture: ${TARGETARCH}" >&2; exit 1 ;; \
    esac; \
    dir="jarvisd-${JARVISD_VERSION}-linux-${TARGETARCH}"; \
    curl -fsSL -o /tmp/jarvisd.tgz "https://github.com/alexberardi/jarvis-server/releases/download/${JARVISD_VERSION}/${dir}.tar.gz"; \
    echo "${sha}  /tmp/jarvisd.tgz" | sha256sum -c -; \
    tar -xzf /tmp/jarvisd.tgz -C /tmp; \
    install -m 0755 "/tmp/${dir}/jarvisd" /usr/local/bin/jarvisd; \
    rm -rf /tmp/jarvisd* "/tmp/${dir}"

ENV JARVIS_HOME=/data JARVIS_NO_BROWSER=1
VOLUME /data
ENTRYPOINT ["jarvisd"]
CMD ["serve", "--no-browser"]
