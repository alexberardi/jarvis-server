#!/bin/sh
# The jarvisd image's entrypoint (installed as /usr/local/bin/jarvisd-entrypoint, run by tini).
# As root it gives $JARVIS_HOME to PUID:PGID (Unraid's nobody:users, 99:100, by default) and runs
# jarvisd as that user; started as any other user (docker run --user) it runs jarvisd as is.
set -eu

: "${JARVIS_HOME:=/data}"
: "${PUID:=99}"
: "${PGID:=100}"

if [ "$(id -u)" != 0 ]; then
	exec jarvisd "$@"
fi

for v in "PUID=$PUID" "PGID=$PGID"; do
	case ${v#*=} in
	'' | *[!0-9]*)
		echo "jarvisd-entrypoint: $v is not a number" >&2
		exit 1
		;;
	esac
done

mkdir -p "$JARVIS_HOME"
if [ "$(stat -c %u:%g "$JARVIS_HOME")" != "$PUID:$PGID" ]; then
	chown -R "$PUID:$PGID" "$JARVIS_HOME"
fi

exec setpriv --reuid="$PUID" --regid="$PGID" --clear-groups env HOME="$JARVIS_HOME" jarvisd "$@"
