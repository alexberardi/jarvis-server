"""services.command_auth_service as the Python node's: a provider's "needs (re-)auth" flag, kept
in the Go agent's store (the node kept a command_auth table)."""

from dataclasses import dataclass
from datetime import datetime, timezone

from jarvis_command_sdk import JarvisStorage

_store = JarvisStorage("_command_auth")


@dataclass
class CommandAuthStatus:
    """Read-only view of a provider's auth status."""

    provider: str
    needs_auth: bool
    auth_error: str | None
    last_checked_at: str | None
    last_authed_at: str | None


def _status(provider: str, row: dict) -> CommandAuthStatus:
    return CommandAuthStatus(provider=provider, needs_auth=bool(row.get("needs_auth")), auth_error=row.get("auth_error"),
                             last_checked_at=row.get("last_checked_at"), last_authed_at=row.get("last_authed_at"))


def get_auth_status(provider: str) -> CommandAuthStatus | None:
    row = _store.get(provider)
    return _status(provider, row) if row else None


def set_needs_auth(provider: str, error: str | None = None) -> None:
    now = datetime.now(timezone.utc).isoformat()
    row = _store.get(provider) or {}
    _store.save(provider, {**row, "needs_auth": 1, "auth_error": error, "last_checked_at": now})


def clear_auth_flag(provider: str) -> None:
    now = datetime.now(timezone.utc).isoformat()
    row = _store.get(provider) or {}
    _store.save(provider, {**row, "needs_auth": 0, "auth_error": None, "last_checked_at": now, "last_authed_at": now})


def get_all_auth_statuses() -> list[CommandAuthStatus]:
    out = []
    for row in _store.get_all():
        provider = row.get("_data_key")
        if provider:
            out.append(_status(provider, row))
    return out
