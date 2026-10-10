"""A trivial IJarvisCommand for the sidecar spike test."""

from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisCommand, JarvisParameter
from jarvis_command_sdk.command import CommandExample


class ShoutCommand(IJarvisCommand):
    @property
    def command_name(self) -> str:
        return "shout"

    @property
    def description(self) -> str:
        return "Repeat a phrase in capitals."

    @property
    def parameters(self) -> List[JarvisParameter]:
        return [JarvisParameter("text", "string", required=True, description="What to shout")]

    @property
    def required_secrets(self) -> list:
        return []

    @property
    def keywords(self) -> List[str]:
        return ["shout"]

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command="shout hello", expected_parameters={"text": "hello"}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs) -> CommandResponse:
        print("this print must not corrupt the protocol")
        return CommandResponse.success_response(context_data={"message": kwargs["text"].upper(), "user_id": request_info.user_id})
