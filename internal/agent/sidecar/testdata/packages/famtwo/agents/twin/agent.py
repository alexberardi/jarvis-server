from jarvis_command_sdk import AgentSchedule, IJarvisAgent


class TwinAgent(IJarvisAgent):
    """Imports its own family by absolute name, as the zwave package's agent does, while
    another package also has a device_families/."""

    @property
    def name(self):
        return "twin"

    @property
    def description(self):
        return "Reads both packages' families."

    @property
    def schedule(self):
        return AgentSchedule(interval_seconds=60, run_on_startup=False)

    @property
    def required_secrets(self):
        return []

    def get_context_data(self):
        return {}

    async def run(self):
        from device_families.one.helper import NAME as one
        from device_families.two.helper import NAME as two

        assert (one, two) == ("one", "two"), (one, two)
