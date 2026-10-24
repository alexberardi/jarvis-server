from jarvis_command_sdk import AgentSchedule, IJarvisAgent


class FlakyAgent(IJarvisAgent):
    @property
    def name(self):
        return "flaky"

    @property
    def description(self):
        return "Always fails."

    @property
    def schedule(self):
        return AgentSchedule(interval_seconds=300, run_on_startup=False)

    @property
    def required_secrets(self):
        return []

    def get_context_data(self):
        return {}

    async def run(self):
        raise RuntimeError("boom")
