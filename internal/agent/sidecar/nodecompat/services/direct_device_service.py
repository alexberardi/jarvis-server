"""services.direct_device_service as the Python node's: control of the household's direct WiFi
devices (LIFX, Kasa, Govee, ...) without Home Assistant. The registry is the Go agent's
(devices.list, from GET /api/v0/node/devices); the protocol adapters are this sidecar's loaded
device families. Control and state call the adapters as the node did."""

from dataclasses import dataclass
from typing import Any

import jarvis_node_compat as host
from device_families.base import DeviceControlResult, DiscoveredDevice


@dataclass
class DeviceRecord:
    """Cached device info for routing control commands."""

    entity_id: str
    protocol: str
    local_ip: str
    mac_address: str
    domain: str
    name: str
    cloud_id: str = ""
    model: str = ""
    room_name: str = ""


class DirectDeviceService:
    """Routes device control to the correct protocol adapter."""

    def __init__(self, cc_base_url: str = "", node_id: str = "", api_key: str = "", household_id: str = "") -> None:
        self._device_cache: dict[str, DeviceRecord] = {}
        self._protocols = dict(host.families())

    def _load_protocols(self) -> None:
        self._protocols = dict(host.families())

    def register_device(self, record: DeviceRecord) -> None:
        self._device_cache[record.entity_id] = record

    async def refresh_from_cc(self) -> int:
        """The registry as the Go agent caches it (the node fetched the same from command-center)."""
        return self._refresh()

    def _refresh(self) -> int:
        rows = host.ask("devices.list", {}) or []
        cache = {}
        for r in rows:
            rec = DeviceRecord(entity_id=r.get("entity_id") or "", protocol=r.get("protocol") or "",
                               local_ip=r.get("local_ip") or "", mac_address=r.get("mac_address") or "",
                               domain=r.get("domain") or "", name=r.get("name") or "", cloud_id=r.get("cloud_id") or "",
                               model=r.get("model") or "", room_name=r.get("room_name") or "")
            if rec.entity_id:
                cache[rec.entity_id] = rec
        self._device_cache = cache
        return len(cache)

    def _ensure(self) -> None:
        if not self._device_cache:
            try:
                self._refresh()
            except Exception:  # noqa: BLE001 — no registry yet: not a direct device
                pass

    def get_device(self, entity_id: str) -> DeviceRecord | None:
        self._ensure()
        return self._device_cache.get(entity_id)

    def is_direct_device(self, entity_id: str) -> bool:
        return self.get_device(entity_id) is not None

    def list_devices(self) -> list[DeviceRecord]:
        self._ensure()
        return list(self._device_cache.values())

    async def control_device(self, entity_id: str, action: str, data: dict[str, Any] | None = None) -> DeviceControlResult:
        device = self._device_cache.get(entity_id)
        if not device:
            await self.refresh_from_cc()
            device = self._device_cache.get(entity_id)
        if not device:
            return DeviceControlResult(success=False, entity_id=entity_id, action=action, error=f"Device not found: {entity_id}")
        adapter = self._protocols.get(device.protocol)
        if not adapter:
            self._load_protocols()
            adapter = self._protocols.get(device.protocol)
        if not adapter:
            return DeviceControlResult(success=False, entity_id=entity_id, action=action,
                                       error=f"No adapter for protocol: {device.protocol}")
        discovered = DiscoveredDevice(entity_id=entity_id, name=device.name, domain=device.domain,
                                      manufacturer=device.protocol, model=device.model, protocol=device.protocol,
                                      cloud_id=device.cloud_id, local_ip=device.local_ip, mac_address=device.mac_address)
        return await adapter.control(discovered, action, data or {})

    def invalidate_cache(self) -> None:
        self._device_cache = {}

    async def get_state(self, entity_id: str) -> dict[str, Any] | None:
        device = self._device_cache.get(entity_id)
        if not device:
            return None
        adapter = self._protocols.get(device.protocol)
        if not adapter:
            return None
        return await adapter.get_state(ip=device.local_ip or "", device=device, mac_address=device.mac_address)

    def get_context_data(self) -> dict[str, Any]:
        controls: dict[str, list] = {}
        for d in self._device_cache.values():
            controls.setdefault(d.domain, []).append({"entity_id": d.entity_id, "name": d.name, "area": d.room_name,
                                                      "state": "unknown"})
        return {"device_controls": controls}


_SERVICE: DirectDeviceService | None = None


def get_direct_device_service() -> DirectDeviceService:
    global _SERVICE
    if _SERVICE is None:
        _SERVICE = DirectDeviceService()
    return _SERVICE
