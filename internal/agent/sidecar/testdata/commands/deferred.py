"""A media-style IJarvisCommand for the sidecar test: says one thing now, does the rest later."""

import pathlib
from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisCommand, JarvisParameter
from jarvis_command_sdk.command import CommandExample


class DeferredCommand(IJarvisCommand):
    @property
    def command_name(self) -> str:
        return "deferred"

    @property
    def description(self) -> str:
        return "Start something once the reply has been spoken."

    @property
    def parameters(self) -> List[JarvisParameter]:
        return [JarvisParameter("marker", "string", required=True, description="File the deferred step writes")]

    @property
    def required_secrets(self) -> list:
        return []

    @property
    def keywords(self) -> List[str]:
        return ["deferred"]

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command="play it later", expected_parameters={"marker": "x"}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs) -> CommandResponse:
        marker = pathlib.Path(kwargs["marker"])
        return CommandResponse(
            context_data={"message": "Playing now."},
            success=True,
            wait_for_input=False,
            clear_history=True,
            on_response_complete=lambda: marker.write_text("played"),
        )
