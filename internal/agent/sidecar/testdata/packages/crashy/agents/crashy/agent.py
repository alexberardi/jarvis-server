"""A background agent whose every run raises, as a Forge draft's first try might."""

from jarvis_command_sdk import AgentSchedule, IJarvisAgent


class CrashyAgent(IJarvisAgent):
    @property
    def name(self):
        return "crashy"

    @property
    def description(self):
        return "Fails every run."

    @property
    def schedule(self):
        return AgentSchedule(interval_seconds=3600)

    @property
    def required_secrets(self):
        return []

    @property
    def include_in_context(self):
        return False

    def get_context_data(self):
        return {}

    async def run(self):
        raise KeyError("state")

    def get_alerts(self):
        return []
