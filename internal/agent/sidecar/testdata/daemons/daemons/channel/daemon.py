"""A chat channel's daemon, as a package writes one: everything through the SDK's Channel.
It reports what Channel made of Jarvis's answers, so the test sees both directions."""
import asyncio

from jarvis_command_sdk import Channel, ChannelImage, IJarvisDaemon, cc_post


class Chat(IJarvisDaemon):
    name = "channel"
    description = "Talks to Jarvis the way a chat channel does."
    required_secrets = []

    async def run(self):
        ch = Channel("testchat")
        reply = await ch.message("ext-1", "dm:1", "hello", [ChannelImage(b"png-bytes", "image/png")])
        stranger = await ch.message("ext-2", "dm:2", "who are you")
        failed = await ch.message("ext-1", "dm:1", "break")
        linked = await ch.command("ext-1", "dm:1", "link", code="ABCD2345")
        waiting = await ch.outbox(ack=["r0"])
        await asyncio.to_thread(cc_post, "/api/v0/node/report", {
            "reply": [reply.linked, reply.say, reply.failed],
            "stranger": [stranger.linked, stranger.say],
            "failed": [failed.linked, failed.say, failed.failed],
            "command": [linked.text, linked.private],
            "outbox": [[m.id, m.external_user_id, m.text] for m in waiting],
        })
        await asyncio.Event().wait()
