"""A trivial IJarvisCommand for the sidecar spike test."""

from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisButton, IJarvisCommand, JarvisParameter, callback, get_current_user_id
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
        if kwargs["text"] == "confirm":
            resp = CommandResponse.follow_up_response(context_data={"message": "Shout it?"})
            resp.actions = [IJarvisButton("Shout", "shout_click", "primary", "send", "Shouted!")]
            return resp
        return CommandResponse.success_response(context_data={"message": kwargs["text"].upper(), "user_id": request_info.user_id})

    def handle_action(self, action_name, context):
        if action_name == "again":
            return CommandResponse.success_response(
                context_data={"message": context.get("text", "").upper(), "user_id": get_current_user_id()})
        if action_name == "ask":
            return CommandResponse.success_response(
                context_data={"input_required": {"type": "text", "prompt": "Louder?", "session_id": "s2"}})
        if action_name == "crash":
            raise RuntimeError("lost my voice")
        return super().handle_action(action_name, context)

    @callback("echo")
    def echo(self, data, request_info):
        return CommandResponse.success_response(context_data={
            "echo": data, "user_id": get_current_user_id(), "voice_command": request_info.voice_command,
            "conversation_id": request_info.conversation_id})

    @callback("refuse")
    def refuse(self, data, request_info):
        return CommandResponse.error_response(error_details="not today")

    @callback("crash")
    def crash(self, data, request_info):
        raise RuntimeError("callback blew up")
