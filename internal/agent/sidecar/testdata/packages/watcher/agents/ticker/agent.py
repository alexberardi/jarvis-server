"""A background agent as comfyui's comfyui_jobs is one: each run reads its record through
storage, posts an inbox item with a picture attached and raises an alert."""

from jarvis_command_sdk import AgentSchedule, Alert, Attachment, IJarvisAgent, JarvisInbox, JarvisStorage

# A 1x1 PNG.
PNG = bytes.fromhex(
    "89504e470d0a1a0a0000000d4948445200000001000000010806000000"
    "1f15c4890000000d49444154789c6360000002000154a24f5d0000000049454e44ae426082"
)


class TickerAgent(IJarvisAgent):
    runs = 0

    @property
    def name(self):
        return "ticker"

    @property
    def description(self):
        return "Posts a picture every run."

    @property
    def schedule(self):
        return AgentSchedule(interval_seconds=20, run_on_startup=True)

    @property
    def required_secrets(self):
        return []

    @property
    def include_in_context(self):
        return False

    def get_context_data(self):
        return {}

    async def run(self):
        TickerAgent.runs += 1
        job = JarvisStorage("ticker_jobs").get("job1") or {}
        self.tag = JarvisInbox("ticker").post(
            title="Your picture is ready", summary=job.get("request", ""), user_id=7, target_type="user",
            create_push_notification=True, attachments=[Attachment(data=PNG, mime="image/png", filename="dot.png")])

    def get_alerts(self):
        return [Alert(source_agent="ticker", title=f"run {TickerAgent.runs}", summary=self.tag,
                      dedupe_key=f"ticker:{TickerAgent.runs}")]
