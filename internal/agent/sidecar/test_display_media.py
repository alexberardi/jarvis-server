"""Assert the display relay uses the installed HA plugin and its advertised actions."""
import asyncio
import importlib.util
import pathlib
import sys
import types

script = pathlib.Path(__file__).with_name("jarvis_sidecar.py")
spec = importlib.util.spec_from_file_location("display_sidecar_test", script)
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
calls = []
features = 512 | 1 | 2 | 4096 | 16384

class HomeAssistantService:
    async def get_state(self, entity):
        return types.SimpleNamespace(success=True, state="idle", attributes={"supported_features": features})

    async def call_service(self, domain, service, entity, data):
        calls.append((domain, service, entity, data))
        return types.SimpleNamespace(success=True, error=None)

ha = types.ModuleType("ha_shared.home_assistant_service")
ha.HomeAssistantService = HomeAssistantService
sys.modules["ha_shared"] = types.ModuleType("ha_shared")
sys.modules["ha_shared.home_assistant_service"] = ha
for action, service in [("play", "play_media"), ("pause", "media_pause"), ("resume", "media_play"), ("stop", "media_stop"), ("seek", "media_seek")]:
    result = module.InvokeDisplayMedia({"entity_id": "media_player.test", "action": action, "url": "https://example.org/video.mp4", "seconds": 12})
    assert result["success"], result
    assert calls[-1][1] == service
assert calls[0][3] == {"media_content_id": "https://example.org/video.mp4", "media_content_type": "video"}
assert calls[-1][3] == {"seek_position": 12.0}
features = 0
assert not module.InvokeDisplayMedia({"entity_id": "media_player.test", "action": "play", "url": "https://example.org/x"})["success"]
assert not module.InvokeDisplayMedia({"entity_id": "lock.door", "action": "play"})["success"]
features = 512
assert not module.InvokeDisplayMedia({"entity_id": "media_player.test", "action": "play", "url": "file:///etc/passwd"})["success"]
module._run(asyncio.sleep(0))
print("Display media plugin relay checks passed")
