"""services.secret_service as the Python node's, over the Go agent's store (the settings the app
set), not the node's own database. Same functions, same answers."""

from dataclasses import dataclass

import jarvis_node_compat as host
from jarvis_command_sdk.storage import get_backend


@dataclass
class Secret:
    """A stored secret, with models.secret.Secret's fields."""

    key: str
    value: str
    scope: str
    value_type: str = "string"
    user_id: int | None = None


def _backend():
    b = get_backend()
    if b is None:
        raise RuntimeError("no storage on this node")
    return b


def set_secret(key: str, value: str, scope: str, value_type: str = "string", user_id: int | None = None):
    _backend().set_secret(key, value, scope, value_type=value_type, user_id=user_id)


def get_secret(key: str, scope: str, user_id: int | None = None) -> Secret | None:
    value = get_secret_value(key, scope, user_id=user_id)
    if value is None:
        return None
    return Secret(key=key, value=value, scope=scope, user_id=user_id if scope == "user" else None)


def get_secret_value(key: str, scope: str, user_id: int | None = None):
    b = get_backend()
    return b.get_secret(key, scope, user_id=user_id) if b else None


def get_secret_value_int(key: str, scope: str, user_id: int | None = None):
    secret = get_secret(key, scope, user_id=user_id)
    try:
        return int(secret.value)
    except (TypeError, ValueError, AttributeError):
        raise ValueError(f"The stored {key} is not a number")


def delete_secret(key: str, scope: str, user_id: int | None = None):
    _backend().delete_secret(key, scope, user_id=user_id)


def get_all_secrets(scope: str, user_id: int | None = None) -> list[Secret]:
    """Every secret of scope; for "user", one user's when user_id is set, else every user's."""
    rows = host.ask("storage.all_secrets", {"scope": scope, "user_id": user_id}) or []
    return [Secret(key=r["key"], value=r["value"], scope=r["scope"], value_type=r.get("value_type") or "string",
                   user_id=r.get("user_id")) for r in rows]


def ensure_secret_exists(key: str, scope: str, value_type: str) -> None:
    """The node pre-seeded an empty row; the Go store has no empty secrets (an unset one reads as
    None either way), so there is nothing to do."""


def get_secret_scope(key: str) -> str | None:
    if any(s.key == key for s in get_all_secrets("user")):
        return "user"
    return host.ask("storage.secret_scope", {"key": key})


def seed_command_secrets(secrets) -> int:
    """As ensure_secret_exists: nothing to seed in the Go store."""
    return 0
