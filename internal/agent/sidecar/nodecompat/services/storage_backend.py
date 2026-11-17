"""services.storage_backend as the Python node's. The node set its SQLCipher backend here; on the
Go agent the sidecar has already set the agent's (storage.* to the Go store), so this keeps it."""

from jarvis_command_sdk.storage import StorageBackend, get_backend


def init_storage_backend() -> None:
    """The agent's backend is already in place."""


NodeStorageBackend = type(get_backend()) if get_backend() is not None else StorageBackend
