#!/usr/bin/env python3
"""Jarvis Test Notes: a fixture desktop notes app for the computer-control suite.

One window, titled "Jarvis Test Notes": the notes list on the left, a title
entry and a text area on the right, a "New note" and a "Save" button, and a
menu bar (File -> New, Save). With --state FILE every save writes
{"notes": [{"title": ..., "body": ...}]} to FILE, which is how the suite reads
back what the agent did. --layout moved swaps the two buttons' positions, for
the replay cases that expect a button to have moved.

Python 3 standard library only (Tkinter). See README.md for running it under
Xvfb and headless sway.
"""

import argparse
import json
import os
import tempfile
import tkinter as tk

TITLE = "Jarvis Test Notes"
LAYOUTS = ("default", "moved")


class NotesApp:
    """The app's widgets and state, built on a Tk root without running mainloop."""

    def __init__(self, root, state_path=None, layout="default"):
        if layout not in LAYOUTS:
            raise ValueError("unknown layout %r" % layout)
        self.root = root
        self.state_path = state_path
        self.layout = layout
        self.notes = []
        self.current = None  # index of the note being edited, None for a new one

        root.title(TITLE)
        root.geometry("720x480")

        menubar = tk.Menu(root)
        filemenu = tk.Menu(menubar, tearoff=0)
        filemenu.add_command(label="New", command=self.new_note, accelerator="Ctrl+N")
        filemenu.add_command(label="Save", command=self.save, accelerator="Ctrl+S")
        menubar.add_cascade(label="File", menu=filemenu)
        root.config(menu=menubar)
        root.bind("<Control-n>", lambda _e: self.new_note())
        root.bind("<Control-s>", lambda _e: self.save())

        left = tk.Frame(root)
        left.pack(side=tk.LEFT, fill=tk.Y, padx=8, pady=8)
        right = tk.Frame(root)
        right.pack(side=tk.LEFT, fill=tk.BOTH, expand=True, padx=8, pady=8)

        # Default: "New note" above the list, "Save" below the editor.
        # Moved: the two swap places.
        top_left = tk.Frame(left)
        top_left.pack(side=tk.TOP, fill=tk.X)
        tk.Label(left, text="Notes").pack(side=tk.TOP, anchor=tk.W)
        self.listbox = tk.Listbox(left, width=24, exportselection=False)
        self.listbox.pack(side=tk.TOP, fill=tk.Y, expand=True)
        self.listbox.bind("<<ListboxSelect>>", self._on_select)

        tk.Label(right, text="Title").pack(side=tk.TOP, anchor=tk.W)
        self.title_entry = tk.Entry(right)
        self.title_entry.pack(side=tk.TOP, fill=tk.X)
        tk.Label(right, text="Body").pack(side=tk.TOP, anchor=tk.W)
        bottom_right = tk.Frame(right)
        bottom_right.pack(side=tk.BOTTOM, fill=tk.X, pady=(8, 0))
        self.body_text = tk.Text(right, wrap=tk.WORD, undo=True)
        self.body_text.pack(side=tk.TOP, fill=tk.BOTH, expand=True)

        new_parent, save_parent = top_left, bottom_right
        if layout == "moved":
            new_parent, save_parent = bottom_right, top_left
        self.new_button = tk.Button(new_parent, text="New note", command=self.new_note)
        self.new_button.pack(side=tk.LEFT, pady=(0, 8))
        self.save_button = tk.Button(save_parent, text="Save", command=self.save)
        self.save_button.pack(side=tk.LEFT, pady=(0, 8))

        self.title_entry.focus_set()

    def new_note(self):
        """Clear the editor; the next save adds a note."""
        self.current = None
        self.listbox.selection_clear(0, tk.END)
        self.title_entry.delete(0, tk.END)
        self.body_text.delete("1.0", tk.END)
        self.title_entry.focus_set()

    def save(self):
        """Store the editor's note (new or the selected one) and write the state file."""
        note = {
            "title": self.title_entry.get(),
            "body": self.body_text.get("1.0", "end-1c"),
        }
        if self.current is None:
            self.notes.append(note)
            self.current = len(self.notes) - 1
        else:
            self.notes[self.current] = note
        self._refresh_list()
        self.write_state()

    def write_state(self):
        """Write {"notes": [...]} atomically, so a reader never sees half a file."""
        if not self.state_path:
            return
        directory = os.path.dirname(os.path.abspath(self.state_path))
        fd, tmp = tempfile.mkstemp(prefix=".notes-", suffix=".json", dir=directory)
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as f:
                json.dump({"notes": self.notes}, f, ensure_ascii=False, indent=2)
                f.write("\n")
            os.replace(tmp, self.state_path)
        except BaseException:
            try:
                os.unlink(tmp)
            except OSError:
                pass
            raise

    def _refresh_list(self):
        self.listbox.delete(0, tk.END)
        for note in self.notes:
            self.listbox.insert(tk.END, note["title"] or "(untitled)")
        if self.current is not None:
            self.listbox.selection_set(self.current)

    def _on_select(self, _event=None):
        sel = self.listbox.curselection()
        if not sel:
            return
        self.current = sel[0]
        note = self.notes[self.current]
        self.title_entry.delete(0, tk.END)
        self.title_entry.insert(0, note["title"])
        self.body_text.delete("1.0", tk.END)
        self.body_text.insert("1.0", note["body"])


def main(argv=None):
    parser = argparse.ArgumentParser(description=TITLE)
    parser.add_argument("--state", metavar="FILE", help="write the notes here as JSON on every save")
    parser.add_argument("--layout", choices=LAYOUTS, default="default",
                        help="'moved' swaps the New note and Save buttons")
    args = parser.parse_args(argv)
    root = tk.Tk()
    NotesApp(root, state_path=args.state, layout=args.layout)
    root.mainloop()


if __name__ == "__main__":
    main()
