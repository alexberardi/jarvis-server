"""utils.service_discovery as the Python node's: where command-center is, from the Go agent
(which resolved it from jarvisd's config service). The other services are command-center's to
proxy on the Go agent, so they resolve to "" here."""

import jarvis_node_compat as host


class ServiceUnresolvedError(RuntimeError):
    """A service that couldn't be resolved."""


def init() -> bool:
    return bool(get_command_center_url())


def is_initialized() -> bool:
    return init()


def get_command_center_url() -> str:
    return host.node_info().get("command_center_url") or ""


def get_whisper_url() -> str:
    return ""


def get_tts_url() -> str:
    return ""


def get_auth_url() -> str:
    return ""


def get_mqtt_broker_url() -> str:
    return ""
