"""An IJarvisCommand that reads its settings through JarvisStorage, as a Pantry package does."""

from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisCommand, JarvisParameter, JarvisSecret, JarvisStorage
from jarvis_command_sdk.command import CommandExample


class SecretiveCommand(IJarvisCommand):
    @property
    def command_name(self) -> str:
        return "secretive"

    @property
    def description(self) -> str:
        return "Report the configured API key and the caller's nickname."

    @property
    def parameters(self) -> List[JarvisParameter]:
        return [JarvisParameter("note", "string", required=False, description="Saved as command data")]

    @property
    def required_secrets(self) -> list:
        return [
            JarvisSecret("TEST_API_KEY", "The service's API key", "integration", "string"),
            JarvisSecret("NICKNAME", "What to call you", "user", "string", required=False, is_sensitive=False,
                         friendly_name="Nickname"),
        ]

    @property
    def keywords(self) -> List[str]:
        return ["secretive"]

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command="what's my key", expected_parameters={}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs) -> CommandResponse:
        storage = JarvisStorage("secretive")
        if kwargs.get("note"):
            storage.save("last", {"note": kwargs["note"]})
        return CommandResponse(
            context_data={
                "api_key": storage.get_secret("TEST_API_KEY"),
                "nickname": storage.get_secret("NICKNAME", scope="user"),
                "last": (storage.get("last") or {}).get("note"),
            },
            success=True,
        )
