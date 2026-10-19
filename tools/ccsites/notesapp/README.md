# Jarvis Test Notes

A small desktop notes app for the computer-control suite's desktop cases
(`notes_x11`, `notes_wlroots`). The agent drives it like any desktop app; the suite then reads
what was saved from the state file (`NotesState` in `../notesapp.go`).

- One window, titled **Jarvis Test Notes**: the notes list on the left, a **New note** button, a
  **Title** entry, a **Body** text area, a **Save** button, and a menu bar (**File → New, Save**;
  also Ctrl+N, Ctrl+S).
- `--state FILE`: every save writes `{"notes": [{"title": "…", "body": "…"}]}` to `FILE`
  (through a temp file and a rename, so a reader never sees half a file). Nothing is written before
  the first save, so a missing file means nothing was saved.
- `--layout moved`: the **New note** and **Save** buttons swap places (New note goes under the
  editor, Save above the list), for the replay cases that expect a moved button.

Python 3 standard library only, but Tkinter needs the Tk libraries: `tk` on Arch,
`python3-tk` on Debian/Ubuntu. Check with `python3 -c 'import tkinter'`.

## Under Xvfb (X11)

```sh
xvfb-run -a -s '-screen 0 1280x800x24' python3 notes.py --state /tmp/notes.json
```

or with a long-lived display the suite and the agent share:

```sh
Xvfb :99 -screen 0 1280x800x24 &
DISPLAY=:99 python3 notes.py --state /tmp/notes.json --layout default
```

## Under headless sway (Wayland, Xwayland)

Tk is an X11 toolkit, so on Wayland it runs through sway's Xwayland:

```sh
export XDG_RUNTIME_DIR=${XDG_RUNTIME_DIR:-$(mktemp -d)}
WLR_BACKENDS=headless WLR_LIBINPUT_NO_DEVICES=1 sway &
# sway starts Xwayland on first use (xwayland enable is the default); find its socket:
swaymsg -t get_version >/dev/null && ls /tmp/.X11-unix/
DISPLAY=:1 python3 notes.py --state /tmp/notes.json   # the X display sway's Xwayland took
```

`swaymsg -t get_tree` shows the window (its `name` is `Jarvis Test Notes`, `shell` is `xwayland`).

## Tests

```sh
python3 -m unittest notes_test        # from this directory
```

The tests create a note, save it, edit it, add a second one, and read the state file back. They
skip without `tkinter` or without a display (`DISPLAY` or `WAYLAND_DISPLAY`), so run them under
`xvfb-run -a python3 -m unittest notes_test`. CI does not run them; `go test ./tools/ccsites/`
covers the Go reader against `testdata/notes.json`.
