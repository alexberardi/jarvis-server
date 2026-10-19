"""IJarvisCommands that reach every SDK backend the Pi registers, as the node's built-ins do."""

from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisCommand, JarvisParameter, RequestInformation
from jarvis_command_sdk.alert_queue import alerts_count, alerts_flush, alerts_pending, alerts_remove
from jarvis_command_sdk.audio_control import (audio_adjust_volume_percent, audio_set_muted,
                                              audio_set_volume_percent, audio_speak)
from jarvis_command_sdk.cc_http import cc_post
from jarvis_command_sdk.command import CommandExample
from jarvis_command_sdk.command_invoker import invoke_command
from jarvis_command_sdk.inbox import JarvisInbox
from jarvis_command_sdk.llm import llm_chat_text
from jarvis_command_sdk.logger import get_logger
from jarvis_command_sdk.signals import JarvisSignals
from jarvis_command_sdk.timer import (timer_cancel, timer_clear_all, timer_find_by_label, timer_get,
                                      timer_list_active, timer_set)


class _Base(IJarvisCommand):
    @property
    def description(self) -> str:
        return self.command_name

    @property
    def parameters(self) -> List[JarvisParameter]:
        return [JarvisParameter("text", "string", required=False, description="anything")]

    @property
    def keywords(self) -> List[str]:
        return [self.command_name]

    @property
    def required_secrets(self) -> list:
        return []

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command=self.command_name, expected_parameters={}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()


class EverythingCommand(_Base):
    @property
    def command_name(self) -> str:
        return "everything"

    def run(self, request_info, **kwargs) -> CommandResponse:
        get_logger("everything").warning("to stderr")
        tid = timer_set(90, "pasta")
        pending = alerts_pending()
        echo = invoke_command("echo", request_info, text="inner")
        return CommandResponse(success=True, context_data={
            "llm": llm_chat_text("a joke"),
            "cc": cc_post("/api/v0/node/thing", {"a": 1}),
            "timer_set": tid,
            "timer_get": timer_get(tid),
            "timer_list": timer_list_active(),
            "timer_find": timer_find_by_label("pas"),
            "timer_cancel": timer_cancel(tid),
            "timer_clear": timer_clear_all(),
            "volume_set": audio_set_volume_percent(40),
            "volume_adjust": audio_adjust_volume_percent(-10),
            "muted": audio_set_muted(True),
            "spoke": audio_speak("hello"),
            "alerts": [[a.title, a.priority, a.created_at.year] for a in pending],
            "alerts_count": alerts_count(),
            "alerts_removed": alerts_remove([a.id for a in pending]),
            "alerts_flushed": len(alerts_flush()),
            "inbox": JarvisInbox("everything").post(title="List", body="milk"),
            "signal": JarvisSignals("everything").emit(kind="presence.seen", source_key="k", summary="here"),
            "echo": echo.context_data if echo else None,
            "missing": invoke_command("nope", request_info),
        })


class EchoCommand(_Base):
    @property
    def command_name(self) -> str:
        return "echo"

    def run(self, request_info: RequestInformation, **kwargs) -> CommandResponse:
        return CommandResponse(success=True, context_data={"text": kwargs.get("text"), "user": request_info.user_id})


class FailingCommand(_Base):
    """Every backend erroring: the SDK's safe defaults, never an exception."""

    @property
    def command_name(self) -> str:
        return "failing"

    def run(self, request_info, **kwargs) -> CommandResponse:
        return CommandResponse(success=True, context_data={
            "llm": llm_chat_text("x"), "timer": timer_set(5), "count": alerts_count(),
            "inbox": JarvisInbox("failing").post(title="t"),
        })
