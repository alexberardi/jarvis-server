# Regenerate testdata/pynode, a provisioned Python node as jarvis-node-setup leaves it, for
# adopt_test.go: python -I pynode-gen.py OUT (needs sqlcipher3 and cryptography).
# The tables are N:models' secrets, command_registry and command_data, with SQLAlchemy's naive
# DateTime strings; the keys are throwaway test keys.
import base64
import json
import os
import sys
from pathlib import Path

import sqlcipher3
from cryptography.fernet import Fernet

out = Path(sys.argv[1])
inst, sec = out / "opt-jarvis-node", out / "home-pi-.jarvis"
inst.mkdir(parents=True)
sec.mkdir(mode=0o700)
(inst / "config.json").write_text(json.dumps({
    "node_id": "pi-kitchen-7f3a", "api_key": "test-node-key-0123456789",
    "jarvis_config_service_url": "http://192.0.2.10:7700", "room": "kitchen",
    "mqtt_username": "pi-kitchen-7f3a", "mqtt_password": "not-carried",
}, indent=2))
(sec / ".provisioned").write_text("")
db_key = "ab" * 32
(sec / "db.key").write_text(db_key)
k1 = b"X" * 43 + b"="  # a valid Fernet key: 32 bytes, urlsafe base64
(sec / "secrets.key").write_bytes(k1)
(sec / "k2.enc").write_bytes(Fernet(k1).encrypt(bytes(range(32))))
(sec / "k2_metadata.json").write_text(json.dumps({"kid": "k2-2026-10", "created_at": "2026-10-01T12:00:00+00:00"}))

db = sqlcipher3.connect(str(inst / "jarvis_node.db"))
db.execute(f"PRAGMA key = '{db_key}'")
db.execute("PRAGMA cipher_compatibility = 4")
db.executescript("""
CREATE TABLE secrets (id VARCHAR(36) PRIMARY KEY, key VARCHAR(255) NOT NULL, value TEXT NOT NULL,
  scope VARCHAR(255) NOT NULL, value_type VARCHAR(255) NOT NULL, user_id INTEGER,
  created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL);
CREATE TABLE command_registry (command_name VARCHAR(255) PRIMARY KEY, enabled INTEGER NOT NULL);
CREATE TABLE command_data (id VARCHAR(36) PRIMARY KEY, command_name VARCHAR(255) NOT NULL,
  data_key VARCHAR(255) NOT NULL, data TEXT NOT NULL, expires_at DATETIME,
  created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL, UNIQUE (command_name, data_key));
""")
t = "2026-10-20 08:00:00.000000"
db.executemany("INSERT INTO secrets VALUES (?,?,?,?,?,?,?,?)", [
    ("s1", "OPENWEATHER_API_KEY", "weather-secret", "integration", "string", None, t, t),
    ("s2", "SPOTIFY_TOKEN", "alice-token", "user", "string", 7, t, t),
])
db.executemany("INSERT INTO command_registry VALUES (?,?)", [("get_sports_scores", 0), ("get_weather", 1)])
reminder = {"reminder_id": "r1", "text": "take the bins out", "due_at": "2030-01-01T19:00:00+00:00",
            "created_at": "2026-10-20T08:00:00+00:00", "recurrence": None, "announced": False,
            "snooze_until": None, "announce_count": 0, "last_announced_at": None, "user_id": 7}
timer = {"timer_id": "t1", "label": "pasta", "duration_seconds": 600,
         "started_at": "2029-12-31T23:50:00", "ends_at": "2030-01-01T00:00:00"}
db.executemany("INSERT INTO command_data VALUES (?,?,?,?,?,?,?)", [
    ("d1", "set_reminder", "r1", json.dumps(reminder), None, t, t),
    ("d2", "set_timer", "t1", json.dumps(timer), "2030-01-01 00:00:00.000000", "2026-10-20 08:00:01.000000", t),
])
db.commit()
db.close()
os.chmod(sec / "db.key", 0o600)
