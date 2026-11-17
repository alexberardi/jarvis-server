"""utils.config_service as the Python node's Config, for what a package reads from it: the
node's identity (node_id, room, household_id, command_center_url) and the household's
home_context, from the Go agent (node.info). The node key is not handed out: the agent signs
command-center calls itself (clients.rest_client), so api_key reads as unset."""

import json
from typing import Any, Dict, Optional

import jarvis_node_compat as host


class Config:
    @staticmethod
    def _values() -> Dict[str, Any]:
        return host.node_info()

    @staticmethod
    def get_str(key: str, default: Optional[str] = None) -> Optional[str]:
        v = Config._values().get(key)
        if v is None or isinstance(v, (dict, list)):
            return default
        return str(v)

    @staticmethod
    def get_int(key: str, default: int) -> int:
        try:
            return int(Config._values()[key])
        except (KeyError, TypeError, ValueError):
            return default

    @staticmethod
    def get_bool(key: str, default: Optional[bool] = None) -> Optional[bool]:
        v = Config._values().get(key)
        if isinstance(v, bool):
            return v
        if isinstance(v, str):
            return v.strip().lower() in ("1", "true", "yes", "on")
        return default

    @staticmethod
    def get_float(key: str, default: float) -> float:
        try:
            return float(Config._values()[key])
        except (KeyError, TypeError, ValueError):
            return default

    @staticmethod
    def get_dict(key: str, default: Optional[Dict[str, Any]] = None) -> Optional[Dict[str, Any]]:
        v = Config._values().get(key)
        if isinstance(v, str):
            try:
                v = json.loads(v)
            except ValueError:
                return default
        return v if isinstance(v, dict) else default

    @staticmethod
    def get(key: str, default: Optional[str] = None) -> Optional[str]:
        return Config.get_str(key, default)
