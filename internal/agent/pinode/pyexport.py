# Dump the Python node's local state as JSON on stdout, for the Go node's adoption (pinode/adopt.go).
# Run with the Python node's own venv (it has sqlcipher3 and cryptography):
#   python -I pyexport.py DB SECRET_DIR
# Everything is opened read-only: the Python node's files stay byte for byte as they were.
import base64
import json
import sys
from datetime import datetime, timezone
from pathlib import Path

import sqlcipher3


def iso(v):
    if v is None:
        return None
    t = v if isinstance(v, datetime) else datetime.fromisoformat(str(v))
    if t.tzinfo is None:  # the node stores UTC; a naive value is UTC (timer_service.from_dict)
        t = t.replace(tzinfo=timezone.utc)
    return t.isoformat()


def rows(db, sql):
    try:
        cur = db.execute(sql)
    except sqlcipher3.OperationalError as e:
        if "no such table" in str(e):
            return []
        raise
    names = [d[0] for d in cur.description]
    return [dict(zip(names, r)) for r in cur.fetchall()]


def k2(secret_dir):
    enc, meta = secret_dir / "k2.enc", secret_dir / "k2_metadata.json"
    if not enc.exists() or not meta.exists():
        return None
    from cryptography.fernet import Fernet

    raw = Fernet((secret_dir / "secrets.key").read_bytes().strip()).decrypt(enc.read_bytes())
    m = json.loads(meta.read_text())
    return {"k2": base64.urlsafe_b64encode(raw).rstrip(b"=").decode(), "kid": m["kid"], "created_at": m["created_at"]}


def main(db_path, secret_dir):
    secret_dir = Path(secret_dir)
    out = {"secrets": [], "disabled_commands": [], "command_data": [], "k2": k2(secret_dir)}
    if Path(db_path).exists():
        db = sqlcipher3.connect(f"file:{db_path}?mode=ro", uri=True)
        key = (secret_dir / "db.key").read_text().strip()
        db.execute(f"PRAGMA key = '{key}'")  # db.py's own key statement (a hex token)
        db.execute("PRAGMA cipher_compatibility = 4")
        for r in rows(db, "SELECT key, value, scope, value_type, user_id FROM secrets"):
            out["secrets"].append(r)
        for r in rows(db, "SELECT command_name FROM command_registry WHERE enabled = 0"):
            out["disabled_commands"].append(r["command_name"])
        for r in rows(db, "SELECT command_name, data_key, data, expires_at FROM command_data ORDER BY created_at"):
            data = json.loads(r["data"])
            if r["command_name"] == "set_timer":
                data["started_at"], data["ends_at"] = iso(data.get("started_at")), iso(data.get("ends_at"))
            out["command_data"].append({"command_name": r["command_name"], "data_key": r["data_key"],
                                        "data": data, "expires_at": iso(r["expires_at"])})
        db.close()
    json.dump(out, sys.stdout)


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
