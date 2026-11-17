"""A command that resolves a device the way the Home Assistant package's resolver does: from its
agent's cached context through the Python node's services.agent_scheduler_service, and reads a
secret through its services.secret_service (the Go agent's store, as the app set it)."""

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
        from services.secret_service import get_secret_value

        secret_service = get_secret_value("HOUSE_URL", "integration")
        from services.agent_scheduler_service import get_agent_scheduler_service

        context = get_agent_scheduler_service().get_aggregated_context()
        house = context.get("house", {})
        ids = [d["entity_id"] for d in house.get("device_controls", {}).get("switch", [])]
        return CommandResponse.success_response(
            context_data={"entities": ids, "secret_service": secret_service, "home": house.get("home")},
            wait_for_input=False)
