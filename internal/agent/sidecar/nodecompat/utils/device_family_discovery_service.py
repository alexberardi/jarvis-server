"""utils.device_family_discovery_service as the Python node's: the device families this node
has, by protocol name. On the Go agent they are the families this sidecar loaded from the
installed packages (the node scanned its device_families/ tree for the same)."""

import jarvis_node_compat as host


class DeviceFamilyDiscoveryService:
    def discover_families(self) -> dict:
        return dict(host.families())

    def get_family(self, name: str):
        return host.families().get(name)

    def get_all_families(self) -> dict:
        return dict(host.families())

    def get_all_families_for_snapshot(self) -> dict:
        return dict(host.families())

    def refresh(self) -> None:
        """The sidecar reloads families when packages change."""


_SERVICE = DeviceFamilyDiscoveryService()


def get_device_family_discovery_service() -> DeviceFamilyDiscoveryService:
    return _SERVICE
