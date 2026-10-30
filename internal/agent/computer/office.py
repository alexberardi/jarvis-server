"""Reads or edits an office document through a headless LibreOffice (computer_files doc_read and
doc_edit, document.go). Go hands it a private copy of the file and gets the result back: the
request is JSON on stdin, the answer JSON on stdout. LibreOffice runs with its own profile and no
window, so it never meets the user's running instance.

request: {"soffice", "profile", "pipe", "src" (a private copy, or "" to create), "dst" (where
to save, edit only), "kind" ("text"|"sheet"), "filter", "ops" ([...], edit only)}
answer: {"ok": true, "text"?: [...], "sheets"?: [...], "done"?: [...]} or {"ok": false, "error"}
"""

import json
import subprocess
import sys
import time

import uno
from com.sun.star.beans import PropertyValue
from com.sun.star.text.ControlCharacter import PARAGRAPH_BREAK
from com.sun.star.document.MacroExecMode import NEVER_EXECUTE

MAX_ROWS, MAX_COLS = 500, 60


def prop(name, value):
    p = PropertyValue()
    p.Name, p.Value = name, value
    return p


def why(e):
    """An exception's message: a UNO one's is in .Message, and str() of it is often empty."""
    return getattr(e, "Message", "") or str(e) or type(e).__name__


def connect(req):
    proc = subprocess.Popen([req["soffice"], "--headless", "--invisible", "--norestore", "--nologo",
                             "--nodefault", "--nofirststartwizard",
                             "-env:UserInstallation=" + uno.systemPathToFileUrl(req["profile"]),
                             "--accept=pipe,name=%s;urp;" % req["pipe"]],
                            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    resolver = uno.getComponentContext().ServiceManager.createInstanceWithContext(
        "com.sun.star.bridge.UnoUrlResolver", uno.getComponentContext())
    deadline = time.time() + 60  # a first start builds the profile
    while True:
        try:
            ctx = resolver.resolve("uno:pipe,name=%s;urp;StarOffice.ComponentContext" % req["pipe"])
            return proc, ctx.ServiceManager.createInstanceWithContext("com.sun.star.frame.Desktop", ctx)
        except Exception:
            if proc.poll() is not None or time.time() > deadline:
                raise RuntimeError("LibreOffice didn't start")
            time.sleep(0.25)


def read_text(doc):
    out = []
    e = doc.Text.createEnumeration()
    while e.hasMoreElements():
        p = e.nextElement()
        if p.supportsService("com.sun.star.text.Paragraph"):
            out.append({"style": p.ParaStyleName, "text": p.getString()})
        elif p.supportsService("com.sun.star.text.TextTable"):
            rows = p.getDataArray() if hasattr(p, "getDataArray") else ()
            out.append({"style": "Table", "text": "\n".join("\t".join(str(c) for c in r) for r in rows)})
    return out


def used(sheet):
    c = sheet.createCursor()
    c.gotoEndOfUsedArea(False)
    a = c.getRangeAddress()
    return a.EndRow, a.EndColumn


def read_sheets(doc):
    out = []
    for i in range(doc.Sheets.Count):
        s = doc.Sheets.getByIndex(i)
        last_row, last_col = used(s)
        rows = []
        for r in range(min(last_row + 1, MAX_ROWS)):
            rows.append([s.getCellByPosition(c, r).getFormula() for c in range(min(last_col + 1, MAX_COLS))])
        while rows and not any(rows[-1]):
            rows.pop()
        out.append({"name": s.Name, "rows": rows, "truncated": last_row + 1 > MAX_ROWS or last_col + 1 > MAX_COLS})
    return out


def add_paragraphs(text, cursor, op, first):
    """Writes op's text as paragraphs at cursor (one per line), in op's style; first: the cursor
    is in an empty paragraph to fill rather than after one."""
    for line in op["text"].split("\n"):
        if not first:
            text.insertControlCharacter(cursor, PARAGRAPH_BREAK, False)
        first = False
        cursor.ParaStyleName = op.get("style") or "Standard"
        text.insertString(cursor, line, False)


def find(doc, s):
    d = doc.createSearchDescriptor()
    d.SearchString = s
    return d


def edit_text(doc, op):
    kind, text = op.get("op"), doc.Text
    if kind == "append":
        c = text.createTextCursor()
        c.gotoEnd(False)
        add_paragraphs(text, c, op, first=text.getString() == "")
        return "appended"
    if kind == "replace":
        d = find(doc, op["find"])
        d.ReplaceString = op.get("text", "")
        n = doc.replaceAll(d)
        if n == 0:
            raise ValueError("no text %r to replace" % op["find"])
        return "replaced %d" % n
    if kind == "insert_after":
        hit = doc.findFirst(find(doc, op["find"]))
        if hit is None:
            raise ValueError("no paragraph with %r" % op["find"])
        t = hit.getText()
        c = t.createTextCursorByRange(hit.getEnd())
        c.gotoEndOfParagraph(False)
        add_paragraphs(t, c, op, first=False)
        return "inserted"
    raise ValueError("a text document takes append, replace or insert_after, not %r" % kind)


def sheet_of(doc, name):
    if not name:
        return doc.Sheets.getByIndex(0)
    if not doc.Sheets.hasByName(name):
        raise ValueError("no sheet %r" % name)
    return doc.Sheets.getByName(name)


def put(cell, v):
    if isinstance(v, bool) or v is None:
        cell.setString("" if v is None else str(v).upper())
    elif isinstance(v, (int, float)):
        cell.setValue(v)
    elif isinstance(v, str) and v.startswith("="):
        cell.setFormula(v)
    else:
        cell.setString(str(v))


def edit_sheet(doc, op, created):
    kind = op.get("op")
    if kind == "add_sheet":
        if created and doc.Sheets.Count == 1 and doc.Sheets.getByIndex(0).Name == "Sheet1" and not any(used(doc.Sheets.getByIndex(0))):
            doc.Sheets.getByIndex(0).Name = op["name"]  # a new workbook's empty first sheet becomes it
        else:
            doc.Sheets.insertNewByName(op["name"], doc.Sheets.Count)
        return "added"
    s = sheet_of(doc, op.get("sheet"))
    if kind == "set":
        put(s.getCellRangeByName(op["cell"]), op.get("value"))
        return "set"
    if kind == "append_row":
        last_row, _ = used(s)
        empty = last_row == 0 and not any(s.getCellByPosition(c, 0).getFormula() for c in range(MAX_COLS))
        row = 0 if empty else last_row + 1
        for c, v in enumerate(op.get("values") or []):
            put(s.getCellByPosition(c, row), v)
        return "row %d" % (row + 1)
    raise ValueError("a spreadsheet takes set, append_row or add_sheet, not %r" % kind)


def main():
    req = json.load(sys.stdin)
    proc, desktop = connect(req)
    doc = None
    try:
        # Files come from strangers (email attachments): never run their macros.
        hidden = (prop("Hidden", True), prop("MacroExecutionMode", NEVER_EXECUTE))
        if req["src"]:
            doc = desktop.loadComponentFromURL(uno.systemPathToFileUrl(req["src"]), "_blank", 0, hidden)
        else:
            doc = desktop.loadComponentFromURL(
                "private:factory/" + ("scalc" if req["kind"] == "sheet" else "swriter"), "_blank", 0, hidden)
        if doc is None:
            raise RuntimeError("LibreOffice couldn't open the file")
        if not req.get("ops"):
            if req["kind"] == "sheet":
                return {"ok": True, "sheets": read_sheets(doc)}
            return {"ok": True, "text": read_text(doc)}
        done = []
        for i, op in enumerate(req["ops"]):
            try:
                done.append(edit_sheet(doc, op, not req["src"]) if req["kind"] == "sheet" else edit_text(doc, op))
            except Exception as e:
                raise ValueError("edit %d: %s" % (i + 1, why(e)))
        if req["kind"] == "sheet":
            doc.calculateAll()
        doc.storeToURL(uno.systemPathToFileUrl(req["dst"]), (prop("FilterName", req["filter"]),))
        return {"ok": True, "done": done}
    finally:
        if doc is not None:
            doc.close(True)
        try:
            desktop.terminate()
        except Exception:
            pass  # it's gone already: the bridge drops as it quits
        try:
            proc.wait(timeout=20)
        except subprocess.TimeoutExpired:
            proc.kill()


if __name__ == "__main__":
    try:
        out = main()
    except Exception as e:
        out = {"ok": False, "error": why(e)}
    json.dump(out, sys.stdout)
