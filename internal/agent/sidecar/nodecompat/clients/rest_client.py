"""clients.rest_client as the Python node's RestClient. A call to command-center goes through
the Go agent (cc.request), which signs it with the node key as the node did; Python never holds
the key. Any other URL is a plain request, as the node made it (no key: the node sent its key
only because every caller targeted command-center). Same signatures, same None on failure."""

import json
import logging
import urllib.error
import urllib.request
from typing import Any, Dict, List, Optional
from urllib.parse import urlsplit

import jarvis_node_compat as host

logger = logging.getLogger("jarvis-node.rest_client")

MemoryItem = Dict[str, Any]


def _cc_path(url: str) -> Optional[str]:
    """url's path (and query) when it is command-center's, else None."""
    base = (host.node_info().get("command_center_url") or "").rstrip("/")
    if not base:
        return None
    u, b = urlsplit(url), urlsplit(base)
    if (u.scheme, u.netloc) != (b.scheme, b.netloc) or not u.path.startswith(b.path):
        return None
    path = u.path[len(b.path):] or "/"
    return path + ("?" + u.query if u.query else "")


def _call(method: str, url: str, data: Optional[Dict[str, Any]], timeout: float) -> Optional[Dict[str, Any]]:
    path = _cc_path(url)
    if path is not None:
        try:
            r = host.ask("cc.request", {"method": method, "path": path, "json": data, "timeout": timeout})
        except Exception as e:  # noqa: BLE001 — as requests.RequestException on the node
            logger.error("REST %s request failed: %s %s", method, url, e)
            return None
        if not r or not 200 <= int(r.get("status") or 0) < 300:
            logger.error("REST %s request failed: %s status %s", method, url, (r or {}).get("status"))
            return None
        return r.get("body")
    body = json.dumps(data).encode() if data is not None else None
    req = urllib.request.Request(url, data=body, method=method,
                                 headers={"Content-Type": "application/json"} if body is not None else {})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
        return json.loads(raw) if raw else None
    except (urllib.error.URLError, ValueError, OSError) as e:
        logger.error("REST %s request failed: %s %s", method, url, e)
        return None


class RestClient:
    """HTTP client for Jarvis service communication."""

    @staticmethod
    def post(url: str, data: Optional[Dict[str, Any]] = None, files: Optional[Dict[str, Any]] = None,
             timeout: int = 10) -> Optional[Dict[str, Any]]:
        if files is not None:
            logger.error("REST POST with files is not supported on this node: %s", url)
            return None
        return _call("POST", url, data, timeout)

    @staticmethod
    def get(url: str, timeout: int = 10) -> Optional[Dict[str, Any]]:
        return _call("GET", url, None, timeout)

    @staticmethod
    def put(url: str, data: Optional[Dict[str, Any]] = None, timeout: int = 10) -> Optional[Dict[str, Any]]:
        return _call("PUT", url, data, timeout)

    @staticmethod
    def post_stream(url: str, data: Optional[Dict[str, Any]] = None, timeout: int = 30, chunk_size: int = 4096):
        """The node's voice-stream transport; no package uses it, and the Go agent streams voice itself."""
        logger.error("REST POST stream is not available to packages on this node: %s", url)
        return None

    @staticmethod
    def post_binary(url: str, data: Optional[Dict[str, Any]] = None, timeout: int = 30) -> Optional[bytes]:
        """The node's TTS fetch; no package uses it, and the Go agent speaks itself (scripts.text_to_speech)."""
        logger.error("REST POST binary is not available to packages on this node: %s", url)
        return None

    @staticmethod
    def inject_memories(memories: List[MemoryItem], household_id: str | None = None) -> Optional[Dict[str, Any]]:
        if not memories:
            return {"injected": 0, "updated": 0, "deduplicated": 0, "errors": []}
        from utils.service_discovery import get_command_center_url

        cc_url = get_command_center_url()
        if not cc_url:
            logger.warning("Cannot inject memories — command center URL not configured")
            return None
        payload: Dict[str, Any] = {"memories": memories}
        if household_id:
            payload["household_id"] = household_id
        return RestClient.post(f"{cc_url}/api/v0/memories/inject", data=payload, timeout=15)
