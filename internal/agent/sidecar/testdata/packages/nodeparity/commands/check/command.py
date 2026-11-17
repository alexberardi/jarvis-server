"""Calls the Python node's modules the way Pantry packages do (2026-11-16 inventory of the 29
published packages), and reports what each answered."""

import asyncio
from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisCommand, JarvisParameter


class Check(IJarvisCommand):
    @property
    def command_name(self) -> str:
        return "nodeparity_check"

    @property
    def description(self) -> str:
        return "Exercise the node modules."

    @property
    def parameters(self) -> List[JarvisParameter]:
        return []

    @property
    def required_secrets(self) -> list:
        return []

    @property
    def keywords(self) -> List[str]:
        return ["check"]

    def generate_prompt_examples(self):
        return []

    def generate_adapter_examples(self):
        return []

    def run(self, request_info, **kwargs) -> CommandResponse:
        out = {}
        # services.secret_service (email, calendar, news, home-assistant)
        from services.secret_service import get_all_secrets, get_secret_value, set_secret

        set_secret("HA_URL", "http://ha.local", "integration")
        out["secret"] = get_secret_value("HA_URL", "integration")
        out["user_secret_users"] = sorted({s.user_id for s in get_all_secrets("user")})
        # services.command_auth_service (email, calendar, home-assistant, nest)
        from services.command_auth_service import clear_auth_flag, get_auth_status, set_needs_auth

        set_needs_auth("home_assistant", "401")
        flagged = get_auth_status("home_assistant")
        clear_auth_flag("home_assistant")
        out["auth"] = [flagged.needs_auth, flagged.auth_error, get_auth_status("home_assistant").needs_auth]
        # device_families.domains (home-assistant control_device)
        from device_families.domains import get_domain_handler

        out["domain_action"] = get_domain_handler("switch").resolve_action("turn_on").name
        # utils.* and constants (sports, weather, calendar, drive time)
        from constants.relative_date_keys import RelativeDateKeys  # noqa: F401
        from utils.date_util import extract_date_from_datetime  # noqa: F401
        from utils.timezone_util import convert_utc_to_local, get_user_timezone  # noqa: F401
        from utils.ha_training_data import get_ha_training_data  # noqa: F401
        from utils.config_service import Config

        out["config"] = [Config.get_str("node_id", ""), Config.get_str("household_id", ""),
                         Config.get_str("api_key", ""), Config.get_dict("home_context")]
        from utils.service_discovery import get_command_center_url

        out["cc_url"] = get_command_center_url()
        # clients.rest_client, services.node_llm_client, services.proposal_client (news, email)
        from clients.rest_client import RestClient
        from services.node_llm_client import ask_llm
        from services.proposal_client import get_suppression_signals

        out["rooms"] = RestClient.get(get_command_center_url() + "/api/v0/households/hh1/rooms")
        out["llm"] = ask_llm("hi", system="be brief")
        out["suppressions"] = get_suppression_signals("get_news", 7)
        out["memories"] = RestClient.inject_memories([{"content": "x", "key": "k", "category": "news"}])
        # services.agent_scheduler_service + direct_device_service (home-assistant)
        from services.agent_scheduler_service import get_agent_scheduler_service

        direct = get_agent_scheduler_service()._direct_device_service
        out["direct"] = [direct.is_direct_device("light.lamp"), direct.is_direct_device("switch.ha_plug")]
        out["direct_control"] = asyncio.run(direct.control_device("light.lamp", "turn_on")).error
        # utils.device_family_discovery_service (home-assistant, homekit)
        from utils.device_family_discovery_service import get_device_family_discovery_service

        out["families"] = sorted(get_device_family_discovery_service().get_all_families())
        # scripts.text_to_speech (openweather), services.storage_backend (medication tracker)
        from scripts.text_to_speech import speak
        from services.storage_backend import init_storage_backend

        speak("hello")
        init_storage_backend()
        out["secret_after_init"] = get_secret_value("HA_URL", "integration")
        # commands.custom_commands.<x> (the node's path to another package's command)
        from commands.custom_commands.helper.command import HELPER_WORD

        out["custom_command"] = HELPER_WORD
        return CommandResponse.success_response(context_data=out, wait_for_input=False)
