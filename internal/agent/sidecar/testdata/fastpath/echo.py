"""A command with a fast path, for the sidecar's pre_route test."""

import re
from typing import List

from jarvis_command_sdk import CommandResponse, FastPathPattern, IJarvisCommand, JarvisParameter
from jarvis_command_sdk.command import CommandExample, PreRouteResult


class EchoCommand(IJarvisCommand):
    @property
    def command_name(self) -> str:
        return "echo_fast"

    @property
    def description(self) -> str:
        return "Echo a phrase."

    @property
    def parameters(self) -> List[JarvisParameter]:
        return [JarvisParameter("text", "string", required=True, description="What to echo")]

    @property
    def required_secrets(self) -> list:
        return []

    @property
    def keywords(self) -> List[str]:
        return ["echo"]

    @property
    def fast_path_patterns(self) -> List[FastPathPattern]:
        return [FastPathPattern(id="echo.say", description="Echo what follows 'echo'", example="echo hello",
                                regex=r"^echo (?P<what>.+)$", handler="_say"),
                FastPathPattern(id="echo.fail", description="Always fails", example="echo fail now",
                                regex=r"^fail (?P<what>.+)$", handler="_fail")]

    def _say(self, match: "re.Match", voice_command: str):
        return PreRouteResult(arguments={"text": match.group("what")})

    def _fail(self, match: "re.Match", voice_command: str):
        return PreRouteResult(arguments={"text": ""})

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command="echo hello", expected_parameters={"text": "hello"}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs) -> CommandResponse:
        if not kwargs["text"]:
            return CommandResponse.error_response(error_details="nothing to echo")
        return CommandResponse.success_response(context_data={"message": kwargs["text"].upper()})
