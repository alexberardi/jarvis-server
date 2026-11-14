"""A daemon as a chat channel's is one: it stays up, and its backend calls overlap (a slow
one doesn't hold up the next)."""
import asyncio

from jarvis_command_sdk import IJarvisDaemon, JarvisSecret, cc_post


class Steady(IJarvisDaemon):
    name = "steady"
    description = "Stays up."
    required_secrets = [JarvisSecret("BRIDGE_TOKEN", "The bridge's token", "integration", "string")]

    async def run(self):
        slow = asyncio.create_task(asyncio.to_thread(cc_post, "/api/v0/node/slow", {}))
        await asyncio.to_thread(cc_post, "/api/v0/node/quick", {})
        await slow
        await asyncio.to_thread(cc_post, "/api/v0/node/up", {})
        await asyncio.Event().wait()
