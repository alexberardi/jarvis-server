# The jarvisd container image, built from a release archive (docs/install/container.md).
#   docker build --build-arg ARCHIVE=jarvisd-<v>-linux-amd64.tar.gz --build-arg VERSION=<v> \
#     -f scripts/packaging/container/Dockerfile <dir with the archive and entrypoint.sh>
# scripts/container-check.sh builds and checks it.

FROM ubuntu:24.04 AS unpack
ARG ARCHIVE
# ADD extracts a local tar.gz: /unpack/jarvisd-<v>-linux-amd64/{jarvisd,LICENSE[,NOTICE]}.
ADD ${ARCHIVE} /unpack/
RUN set -eu; \
    src=$(echo /unpack/jarvisd-*/); \
    install -D -m0755 "$src/jarvisd" /out/usr/local/bin/jarvisd; \
    install -D -m0644 "$src/LICENSE" /out/usr/share/doc/jarvisd/LICENSE; \
    if [ -f "$src/NOTICE" ]; then install -m0644 "$src/NOTICE" /out/usr/share/doc/jarvisd/NOTICE; fi

FROM ubuntu:24.04
ARG VERSION=dev
LABEL org.opencontainers.image.title="jarvisd" \
      org.opencontainers.image.description="Jarvis home assistant server" \
      org.opencontainers.image.source="https://github.com/alexberardi/jarvis-server" \
      org.opencontainers.image.licenses="AGPL-3.0" \
      org.opencontainers.image.version="${VERSION}"

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl libgomp1 tini tzdata \
 && rm -rf /var/lib/apt/lists/*

COPY --from=unpack /out/ /
COPY entrypoint.sh /usr/local/bin/jarvisd-entrypoint
RUN chmod 0755 /usr/local/bin/jarvisd-entrypoint

ENV JARVIS_HOME=/data \
    JARVIS_CONTAINER=docker \
    JARVIS_NO_BROWSER=1 \
    PUID=99 \
    PGID=100 \
    NVIDIA_DRIVER_CAPABILITIES=compute,utility

EXPOSE 7700 7710 1884
VOLUME /data

HEALTHCHECK --interval=30s --timeout=5s --start-period=60s \
  CMD curl -fsS http://127.0.0.1:7700/health || exit 1

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/jarvisd-entrypoint"]
CMD ["serve", "--no-browser"]
