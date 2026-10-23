"""Tests for the notes fixture. Run from this directory: python3 -m unittest.

They need Tk and a display (Xvfb or Xwayland) and skip without either.
"""

import json
import os
import tempfile
import unittest

try:
    import tkinter as tk
    import notes
    HAVE_TK = True
except ImportError:
    HAVE_TK = False

HAVE_DISPLAY = bool(os.environ.get("DISPLAY") or os.environ.get("WAYLAND_DISPLAY"))


@unittest.skipUnless(HAVE_TK and HAVE_DISPLAY, "needs tkinter and a display")
class NotesAppTest(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.state = os.path.join(self.dir.name, "notes.json")
        self.root = tk.Tk()

    def tearDown(self):
        self.root.destroy()
        self.dir.cleanup()

    def read_state(self):
        with open(self.state, encoding="utf-8") as f:
            return json.load(f)

    def type_note(self, app, title, body):
        app.title_entry.delete(0, tk.END)
        app.title_entry.insert(0, title)
        app.body_text.delete("1.0", tk.END)
        app.body_text.insert("1.0", body)

    def test_window_title(self):
        notes.NotesApp(self.root, state_path=self.state)
        self.assertEqual(self.root.title(), "Jarvis Test Notes")

    def test_buttons_advertise_their_shortcuts(self):
        # A Tk app gives the agent screenshots only (no AT-SPI), so the screen carries the
        # affordances: the buttons name their accelerators, as real apps' menus and tooltips do.
        app = notes.NotesApp(self.root, state_path=self.state)
        self.assertEqual(app.new_button["text"], "New note (Ctrl+N)")
        self.assertEqual(app.save_button["text"], "Save (Ctrl+S)")

    def test_create_save_read_state(self):
        app = notes.NotesApp(self.root, state_path=self.state)
        self.root.update()
        self.assertFalse(os.path.exists(self.state))

        app.new_button.invoke()
        self.type_note(app, "Groceries", "milk\neggs")
        app.save_button.invoke()
        self.root.update()
        self.assertEqual(self.read_state(),
                         {"notes": [{"title": "Groceries", "body": "milk\neggs"}]})

        # Saving again edits the same note; New note then adds a second one.
        self.type_note(app, "Groceries", "milk\neggs\nbread")
        app.save_button.invoke()
        app.new_button.invoke()
        self.type_note(app, "Call", "dentist")
        app.save_button.invoke()
        self.root.update()
        self.assertEqual(self.read_state(), {"notes": [
            {"title": "Groceries", "body": "milk\neggs\nbread"},
            {"title": "Call", "body": "dentist"},
        ]})
        self.assertEqual(app.listbox.get(0, tk.END), ("Groceries", "Call"))

    def test_moved_layout_swaps_buttons(self):
        default = notes.NotesApp(self.root, state_path=self.state)
        other = tk.Toplevel(self.root)
        moved = notes.NotesApp(other, layout="moved")
        self.root.update()

        # Default: "New note" in the left column (above the list), "Save" in
        # the right one (below the editor).
        self.assertIs(default.new_button.master.master, default.left)
        self.assertIs(default.save_button.master.master, default.right)
        self.assertEqual(default.new_button.master.winfo_name(), "top_left")
        self.assertEqual(default.save_button.master.winfo_name(), "bottom_right")

        # Moved: the two swap columns.
        self.assertIs(moved.new_button.master.master, moved.right)
        self.assertIs(moved.save_button.master.master, moved.left)
        self.assertEqual(moved.new_button.master.winfo_name(), "bottom_right")
        self.assertEqual(moved.save_button.master.winfo_name(), "top_left")

if __name__ == "__main__":
    unittest.main()
