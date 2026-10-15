"""An installed package's command that imports its own module by absolute name from shared code at its root."""

from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisCommand, JarvisParameter
from jarvis_command_sdk.command import CommandExample

from beta_shared import WORD


class Greet(IJarvisCommand):
    @property
    def command_name(self) -> str:
        return "beta_greet"

    @property
    def description(self) -> str:
        return "Say beta's word."

    @property
    def parameters(self) -> List[JarvisParameter]:
        return []

    @property
    def required_secrets(self) -> list:
        return []

    @property
    def keywords(self) -> List[str]:
        return ["greet"]

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command="greet", expected_parameters={}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs) -> CommandResponse:
        return CommandResponse.success_response(context_data={"message": WORD}, wait_for_input=False)
