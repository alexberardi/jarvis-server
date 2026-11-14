from jarvis_command_sdk import IJarvisDaemon


class Returns(IJarvisDaemon):
    name = "returns"
    description = "Thinks it is an agent."
    required_secrets = []

    async def run(self):
        return None
