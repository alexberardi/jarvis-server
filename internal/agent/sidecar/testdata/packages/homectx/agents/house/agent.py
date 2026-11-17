"""A background agent as Home Assistant's is one: each run caches the house's devices, which
get_context_data() hands to the node's context (node_context.agents)."""

from jarvis_command_sdk import AgentSchedule, IJarvisAgent, get_home_context


class HouseAgent(IJarvisAgent):
    def __init__(self):
        self.devices = {}
        self.home = None

    @property
    def name(self):
        return "house"

    @property
    def description(self):
        return "Caches the house's devices."

    @property
    def schedule(self):
        return AgentSchedule(interval_seconds=300, run_on_startup=True)

    @property
    def required_secrets(self):
        return []

    @property
    def include_in_context(self):
        return True

    def get_context_data(self):
        return {"device_controls": self.devices, "home": self.home, "devices": ["the full cache"]}

    async def run(self):
        self.home = get_home_context()  # seeded by the scheduler, as on the node
        self.devices = {"switch": [{"entity_id": "switch.kitchen_plug", "name": "Kitchen Plug", "area": "Kitchen"}]}

    def get_alerts(self):
        return []
