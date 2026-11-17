"""services.agent_scheduler_service as packages see it on the Python node: the agents' context
after their runs (get_aggregated_context) and the direct-device service (_direct_device_service).
The Go agent schedules the agents (internal/agent/pkgagents); this sidecar keeps their context."""

import jarvis_node_compat as host


class AgentSchedulerService:
    def get_aggregated_context(self) -> dict:
        return host.agent_context()

    @property
    def _direct_device_service(self):
        from services.direct_device_service import get_direct_device_service

        return get_direct_device_service()


_SCHEDULER = AgentSchedulerService()


def get_agent_scheduler_service() -> AgentSchedulerService:
    return _SCHEDULER
