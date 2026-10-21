# The jarvisd container image

One image, `jarvisd`, built from the release's `jarvisd-<v>-linux-amd64.tar.gz` (the same
binary as the bare-metal install). It runs jarvisd in container mode: updates come from
pulling a new image, not from jarvisd's own updater.

## Build

`scripts/packaging/container/` holds the `Dockerfile` and the entrypoint. The build context
is a directory with the archive and `entrypoint.sh`:

```sh
ctx=$(mktemp -d)
cp dist/jarvisd-v1.2.3-linux-amd64.tar.gz scripts/packaging/container/entrypoint.sh "$ctx/"
docker build -t jarvisd:v1.2.3 -f scripts/packaging/container/Dockerfile \
  --build-arg ARCHIVE=jarvisd-v1.2.3-linux-amd64.tar.gz --build-arg VERSION=v1.2.3 "$ctx"
```

`scripts/container-check.sh DIST VERSION` builds the image from `DIST/` and checks it: health
within 60 s, exactly ports 7700/7710/1884 listening, `/services` URLs with paths, jarvisd
running as uid 99, the setup token in `docker logs`, and the data surviving `docker rm` and a
new `docker run`.

The base is `ubuntu:24.04` with `ca-certificates curl libgomp1 tini tzdata`. tini is PID 1;
`HEALTHCHECK` curls `/health` on 7700.

## Run

```sh
docker run -d --name jarvis -p 7700:7700 -p 7710:7710 -p 1884:1884 \
  -v /srv/jarvis:/data jarvisd:v1.2.3
docker logs jarvis   # "Setup token: ..." for the setup wizard on http://<host>:7710/
```

Ports: 7700 API (every service under its path, e.g. `/cc`), 7710 admin, 1884 MQTT.
`/data` holds everything (`jarvis.db`, `setup-token`, models, engines).

## Environment

| Variable | Default | |
|---|---|---|
| `JARVIS_HOME` | `/data` | jarvisd's home |
| `JARVIS_CONTAINER` | `docker` | container mode (Unraid's template sets `unraid`) |
| `JARVIS_NO_BROWSER` | `1` | never open a browser |
| `PUID` / `PGID` | `99` / `100` | the user jarvisd runs as (Unraid's `nobody:users`) |
| `NVIDIA_DRIVER_CAPABILITIES` | `compute,utility` | what the NVIDIA runtime mounts |

Any other `JARVIS_*` variable jarvisd reads works too (e.g. `JARVIS_MDNS=0`).

**PUID/PGID.** Started as root (the default), the entrypoint makes `$JARVIS_HOME` belong to
`PUID:PGID` (a recursive `chown` only when its owner differs) and runs jarvisd as that user
with no supplementary groups. A value that isn't a number stops the container; an empty one
means the default. Started with `--user`, it runs jarvisd as that user and changes nothing.

**GPU (NVIDIA).** With the NVIDIA Container Toolkit on the host, add
`--runtime=nvidia -e NVIDIA_VISIBLE_DEVICES=all`. jarvisd then finds the
GPU through `nvidia-smi` and the setup wizard's Hardware step lists it.

## Unraid

The Community Applications template is `scripts/packaging/unraid/jarvis.xml` (its README
says how it is published); the install guide is the Unraid page in jarvis-docs
(`docs/getting-started/unraid.md`).
