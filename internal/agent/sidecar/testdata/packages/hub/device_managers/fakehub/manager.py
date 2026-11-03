"""A device manager that serves state and control (the SDK's optional get_state/control)."""

from jarvis_command_sdk import DeviceControlResult, DeviceManagerDevice, IJarvisDeviceManager

_TV = {"state": "playing", "position": 10.0, "duration": 300.0, "available_features": ["play", "pause", "stop"]}


class FakeHub(IJarvisDeviceManager):
    name = "fakehub"
    friendly_name = "Fake Hub"
    can_edit_devices = False

    async def collect_devices(self) -> list:
        return [DeviceManagerDevice(name="TV", domain="media_player", entity_id="media_player.tv", source="fakehub")]

    async def get_state(self, device):
        if device.entity_id == "media_player.broken":
            return {"state": "unknown", "error": "hub offline"}
        return dict(_TV) if device.entity_id == "media_player.tv" else None

    async def control(self, device, action, data=None):
        if action not in _TV["available_features"]:
            return DeviceControlResult(False, device.entity_id, action, error=f"{device.name} doesn't support {action}")
        _TV["state"] = {"play": "playing", "pause": "paused", "stop": "idle"}[action]
        return DeviceControlResult(True, device.entity_id, action)
