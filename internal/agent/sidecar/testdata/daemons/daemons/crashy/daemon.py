from jarvis_command_sdk import IJarvisDaemon


class Crashy(IJarvisDaemon):
    name = "crashy"
    description = "Dies at once."
    required_secrets = []

    async def run(self):
        raise RuntimeError("no token")
