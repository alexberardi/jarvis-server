"""An IJarvisMusicCommand for the sidecar test: a fake radio whose player is a dict."""

from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisMusicCommand, PlaybackArtwork, PlaybackState
from jarvis_command_sdk.command import CommandExample

PLAYER = {"state": "idle", "volume": 50}


class RadioCommand(IJarvisMusicCommand):
    @property
    def command_name(self) -> str:
        return "radio"

    @property
    def description(self) -> str:
        return "Play the radio."

    @property
    def parameters(self) -> list:
        return []

    @property
    def required_secrets(self) -> list:
        return []

    @property
    def keywords(self) -> List[str]:
        return ["radio"]

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command="play the radio", expected_parameters={}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs) -> CommandResponse:
        PLAYER["state"] = "playing"
        self.report()  # queued in the sidecar until the agent drains it
        return CommandResponse.success_response(context_data={"message": "Playing Jazz FM"})

    @property
    def playback_binary(self) -> str:
        return "mpv"

    def playback_status(self):
        if PLAYER["state"] == "idle":
            return None
        return PlaybackState(state=PLAYER["state"], media_title="Jazz FM", volume=PLAYER["volume"],
                             actions=("play", "pause", "volume"))

    def playback_control(self, action: str, **args):
        if action == "pause":
            PLAYER["state"] = "paused"
        elif action == "play":
            PLAYER["state"] = "playing"
        elif action == "volume":
            PLAYER["volume"] = args["volume"]
        else:
            raise ValueError(f"radio can't {action}")
        return self.playback_status()

    def playback_artwork(self):
        if PLAYER["state"] == "idle":
            return None
        return PlaybackArtwork(data=b"\xff\xd8\xffjpeg", mime="image/jpeg")
