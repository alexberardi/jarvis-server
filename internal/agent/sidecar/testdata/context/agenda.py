"""Context-provider fixtures: commands that declare plan-time context operations."""

from typing import List

from jarvis_command_sdk import CommandResponse, ContextOperation, ContextResult, IJarvisCommand, JarvisParameter
from jarvis_command_sdk.command import CommandExample
from jarvis_command_sdk.context import get_current_user_id


class _Fixture(IJarvisCommand):
    @property
    def description(self) -> str:
        return f"{self.command_name} fixture"

    @property
    def parameters(self) -> List[JarvisParameter]:
        return []

    @property
    def required_secrets(self) -> list:
        return []

    @property
    def keywords(self) -> List[str]:
        return [self.command_name]

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command=self.command_name, expected_parameters={}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs) -> CommandResponse:
        return CommandResponse(context_data={}, success=True)


class AgendaCommand(_Fixture):
    """Answers availability as the requesting user; "explode" raises."""

    @property
    def command_name(self) -> str:
        return "agenda"

    @property
    def context_operations(self) -> List[ContextOperation]:
        return [
            ContextOperation(name="availability", description="Free/busy between two times",
                             params_schema={"start": {"type": "string", "required": True},
                                            "end": {"type": "string", "required": True}}),
            ContextOperation(name="explode", description="Always raises"),
        ]

    def execute_context_operation(self, operation, params):
        if operation == "explode":
            raise RuntimeError("calendar offline")
        return ContextResult(data={"user": get_current_user_id(), "busy": [[params["start"], params["end"]]]})


class BrokenOpsCommand(_Fixture):
    """Its context_operations raises: it declares none rather than breaking describe."""

    @property
    def command_name(self) -> str:
        return "broken_ops"

    @property
    def context_operations(self) -> List[ContextOperation]:
        raise RuntimeError("bad declaration")


class PlainCommand(_Fixture):
    @property
    def command_name(self) -> str:
        return "plain"
