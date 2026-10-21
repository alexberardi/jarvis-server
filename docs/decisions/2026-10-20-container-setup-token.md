# In a container, the first-run announcement prints the setup token

- **Date:** 2026-10-20 (plan P4, "Unraid install", approved by the user; built in #364)

Since #224 jarvisd prints the setup token only when stderr is a person's terminal: under
systemd, launchd or a Windows service stderr is a journal or log file that other local users
may read, so it gets a line pointing at `jarvisd setup-link` instead.

With `JARVIS_CONTAINER` set (the image sets `docker`, the Unraid template `unraid`), stderr is
the container log, and the token is printed there anyway:

- **Only the host's admin reads that log.** On Unraid it is the Docker tab's log view, behind
  the Unraid login; with plain Docker it is `docker logs`, which needs the Docker socket (root
  or the `docker` group, already full control of the host).
- **The token is in their data folder anyway** (`setup-token` in appdata, or the `/data`
  volume), which the same admin owns. Printing it reveals nothing they can't already read.
- **`jarvisd setup-link` is out of reach** for most Unraid users: it needs a shell in the
  container, which the Docker tab hides. The log is where they look.

The token never goes into the structured log record (`first-run setup` names the token file,
as before), so the logs module's DB sink still doesn't carry it.

The announcement also doesn't name a URL: the address jarvisd sees is the container's bridge
IP, which a browser on the LAN can't reach. It says to open the WebUI (port 7710 on the server)
and paste the token instead.

This narrows #224's rule for containers only; every other kind of install keeps it.
