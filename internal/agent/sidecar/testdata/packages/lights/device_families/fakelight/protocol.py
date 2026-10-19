"""A LAN device family, as a Pantry package ships one (device_families/<name>/protocol.py)."""

from jarvis_command_sdk import DeviceControlResult, DiscoveredDevice, IJarvisDeviceProtocol, JarvisStorage


class FakeLight(IJarvisDeviceProtocol):
    protocol_name = "fakelight"
    supported_domains = ["light", "climate"]

    async def discover(self, timeout: float = 5.0) -> list:
        def dev(name, domain, entity_id, **kw):
            return DiscoveredDevice(name=name, domain=domain, manufacturer="Fake", model="F1", protocol="fakelight",
                                    entity_id=entity_id, **kw)

        return [
            dev("Lamp (old)", "light", "light.lamp", local_ip="10.0.0.4", mac_address="AA:BB:CC:00:00:01"),
            dev("Lamp", "light", "light.lamp", local_ip="10.0.0.5", mac_address="aa:bb:cc:00:00:01",
                extra={"token": "never leaves the node"}),
            dev("Hall", "climate", "climate.hall", cloud_id="t-1"),
            dev("Pair me", "light", "light.pair_me", local_ip="10.0.0.9"),
        ]

    async def control(self, ip, action, data=None, **kwargs):
        return DeviceControlResult(success=True, entity_id=kwargs.get("entity_id", ""), action=action)

    async def get_state(self, ip, **kwargs):
        entity_id = kwargs.get("entity_id")
        if entity_id == "light.pair_me":
            return {"needs_pairing": True}
        if entity_id == "climate.hall":
            # 20.5 rounds to 20 and 21.5 to 22, as Python's round() does on the Python node.
            return {"mode": "HEAT", "current_temperature": 20.5, "heat_setpoint": 21.5,
                    "available_modes": ["HEAT", "OFF"]}
        if entity_id == "light.lamp" and ip == "10.0.0.5":
            return {"state": "ON", "brightness": 255, "rgb_color": [255, 128, 0]}
        if entity_id == "light.odd":
            return {"state": "on", "level": 3}
        raise RuntimeError(f"no such device {entity_id}")

    async def on_removed(self, device, **kwargs):
        JarvisStorage("fakelight").save("removed", {"entity_id": kwargs.get("entity_id"), "ip": device.local_ip})
