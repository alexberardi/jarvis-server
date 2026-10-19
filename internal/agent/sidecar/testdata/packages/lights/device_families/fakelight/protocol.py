"""A LAN device family, as a Pantry package ships one (device_families/<name>/protocol.py)."""

import asyncio

from jarvis_command_sdk import DeviceControlResult, DiscoveredDevice, InputRequest, IJarvisDeviceProtocol, JarvisStorage


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
        # The Python node passes the device itself where the SDK says ip (and the whole
        # action context as data); a family written for it reads the device's fields.
        dev = ip
        if not isinstance(dev, DiscoveredDevice):
            return DeviceControlResult(success=False, entity_id="", action=action, error=f"not a device: {dev!r}")
        if action == "explode":
            raise RuntimeError("the bulb popped")
        if action == "pair_start":
            # A pairing session lives on the loop it started on (pyatv's do), across calls.
            self._pairing = asyncio.get_running_loop()
            return DeviceControlResult(success=False, entity_id=dev.entity_id, action=action, error="PIN needed",
                                       input_required=InputRequest(type="pin", prompt="Enter the PIN", session_id="s1"))
        if action == "pair_finish":
            same = getattr(self, "_pairing", None) is asyncio.get_running_loop()
            ok = same and (data or {}).get("pin") == "1234"
            return DeviceControlResult(success=ok, entity_id=dev.entity_id, action=action,
                                       error=None if ok else f"pairing lost (same loop: {same})")
        got = (dev.entity_id, dev.name, dev.domain, dev.local_ip, dev.mac_address, (data or {}).get("brightness"))
        JarvisStorage("fakelight").save("controlled", {"action": action, "device": list(got)})
        return DeviceControlResult(success=True, entity_id=dev.entity_id, action=action)

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
