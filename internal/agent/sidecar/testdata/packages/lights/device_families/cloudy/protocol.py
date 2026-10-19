"""A cloud family behind OAuth: configured only once its token is stored."""

from jarvis_command_sdk import DeviceControlResult, DiscoveredDevice, IJarvisDeviceProtocol, JarvisSecret, JarvisStorage
from jarvis_command_sdk.authentication import AuthenticationConfig


class Cloudy(IJarvisDeviceProtocol):
    protocol_name = "cloudy"
    supported_domains = ["light"]
    connection_type = "cloud"
    friendly_name = "Cloudy Lights"
    authentication = AuthenticationConfig(type="oauth", provider="cloudy", friendly_name="Cloudy", client_id="c",
                                          keys=["access_token"])

    @property
    def required_secrets(self) -> list:
        return [JarvisSecret("CLOUDY_TOKEN", "OAuth token", "integration", "string")]

    def validate_secrets(self) -> list:
        return [] if JarvisStorage("cloudy").get_secret("CLOUDY_TOKEN") else ["CLOUDY_TOKEN"]

    def store_auth_values(self, values: dict) -> None:
        token = f"{values.get('access_token')}@{values.get('_base_url')}"
        JarvisStorage("cloudy").set_secret("CLOUDY_TOKEN", token, "integration")
        # As the user who ran the flow (the sidecar sets them for this call only).
        JarvisStorage("cloudy").set_secret("CLOUDY_USER", str(values.get("access_token")), "user")

    async def discover(self, timeout: float = 5.0) -> list:
        # The same entity_id as fakelight's lamp: the scan keeps both.
        return [DiscoveredDevice(name="Cloud lamp", domain="light", manufacturer="Cloudy", model="C1",
                                 protocol="cloudy", entity_id="light.lamp", cloud_id="c-1")]

    async def control(self, ip, action, data=None, **kwargs):
        return DeviceControlResult(success=True, entity_id="", action=action)

    async def get_state(self, ip, **kwargs):
        return {"state": "off"}

    async def on_removed(self, device, **kwargs):
        # Records whose user-scoped secret a call without a user sees (none).
        JarvisStorage("cloudy").save("removed", {"user_token": JarvisStorage("cloudy").get_secret("CLOUDY_USER", "user")})
