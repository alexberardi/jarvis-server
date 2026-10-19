# The Go Pi node runs a package's apt and post_install through the Python node's wrappers

- **Date:** 2026-10-19 (loop #174; the user approves the merge: it widens the node user's sudoers)

On the Pi profile, `jarvis-agent` installs a Pantry package's `apt_sources`, `apt_packages` and
`post_install` the way the Python node does (`N:services/command_store_service.py:538-830`), and
answers Forge test installs (`test-install`). A desktop agent still refuses system setup
([2026-10-15](2026-10-15-pantry-packages-on-desktop-agents.md)).

- **The same root wrappers, unchanged.** `jarvis-apt-install`, `jarvis-apt-source` and
  `jarvis-post-install` are vendored byte for byte from jarvis-node-setup (`scripts/packaging/pi/`,
  N: 73b3660), installed by `install-pi.sh` root-owned 0755 in `/usr/local/sbin`, with the Python
  node's three `NOPASSWD: <wrapper> *` lines. They are the only privileged code; the agent (owned
  by the node user, who also owns `/opt/jarvis-agent`) is never given a sudoers line of its own,
  so a port of `jarvis-post-install` into the Go binary was ruled out. The agent calls them with
  `sudo -n` (fail rather than prompt). The wrappers shape-check their arguments; what a package
  may ask for is still Pantry's allow-list, checked at submission. `jarvis-post-install` is
  Python, so `install-pi.sh` adds `python3` to its apt list (Raspberry Pi OS has it).
- **Order, as Python:** apt sources, apt packages (≥ 500 MB free on `/`), pip, the files in
  place, the post_install ops (`run_as: jarvis_user` → the agent's own user, `${UID}`, `${USER}`,
  `${HOME}` expanded), then the sidecar check. The first failure aborts and puts the package
  back; a fresh install's drop-ins are removed (`remove-managed-dropins`), an update's previous
  version gets its ops again. Apt packages are never removed, and `set_config_file_value` edits
  are not reverted (as on the Python node).
- **Uninstall** removes the package's drop-ins; **revert** reruns the previous version's ops (no
  apt: its packages are still installed). **Boot** removes drop-ins whose owner package is no
  longer installed (`N:services/post_install_dropins.py`'s self-heal, owner check only).
- **Test installs** are served as `test:<name>` from `<packages>/tests/<name>/` for 20 minutes,
  then removed and the sidecar restarted. The draft is fetched from the Pantry URL in jarvisd's
  verify answer without credentials (the share code is the secret). Only requirement lines of its
  `requirements.txt` reach uv (options and URLs are skipped); pip failures don't fail the install,
  as on Python. What it pip-installed stays in the venv after expiry (Python uninstalls the diff).
  Its commands are the draft manifest's, else every `.py` that defines an `IJarvisCommand`. No
  system setup runs for a test install (Python's doesn't either).

Known gap: `jarvis-post-install` turns an `Environment=` value under `/.jarvis/packages/` into a
`ConditionPathExists=`; the Go node's packages live under `~/.config/jarvis-agent/packages/`, so a
package whose op points at its own shipped files gets no such guard (and its `${HOME}/.jarvis/...`
path doesn't exist on the Go node). audacy's mpd drop-in (`MPDCONF`) is the known case; it needs
a package-directory token or a path change before it works on the Go node.
