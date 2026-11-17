"""A command that resolves a device the way the Home Assistant package's resolver does: from its
agent's cached context through the Python node's services.agent_scheduler_service, and that
falls back as the package does when services.secret_service is missing."""

from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisCommand, JarvisParameter


class Which(IJarvisCommand):
    @property
    def command_name(self) -> str:
        return "homectx_which"

    @property
    def description(self) -> str:
        return "Which devices the house agent knows."

    @property
    def parameters(self) -> List[JarvisParameter]:
        return []

    @property
    def required_secrets(self) -> list:
        return []

    @property
    def keywords(self) -> List[str]:
        return ["which"]

    def generate_prompt_examples(self):
        return []

    def generate_adapter_examples(self):
        return []

    def run(self, request_info, **kwargs) -> CommandResponse:
        try:
            from services.secret_service import get_secret_value  # noqa: F401
            secret_service = "present"
        except ImportError:
            secret_service = "missing"
        from services.agent_scheduler_service import get_agent_scheduler_service

        context = get_agent_scheduler_service().get_aggregated_context()
        ids = [d["entity_id"] for d in context.get("house", {}).get("device_controls", {}).get("switch", [])]
        return CommandResponse.success_response(
            context_data={"entities": ids, "secret_service": secret_service}, wait_for_input=False)
