"""A Pantry device manager (device_managers/<name>/manager.py)."""

from jarvis_command_sdk import DeviceManagerDevice, IJarvisDeviceManager


class FakeManager(IJarvisDeviceManager):
    name = "fakemgr"
    friendly_name = "Fake Hub"
    description = "Devices from a hub"
    can_edit_devices = False

    async def collect_devices(self) -> list:
        return [DeviceManagerDevice(name="Hub plug", domain="switch", entity_id="switch.hub_plug", source="fakehub",
                                    area="Kitchen", state="on", extra={"hub_id": "h1"})]
