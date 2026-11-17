"""scripts.text_to_speech as the Python node's: speak text on this node's speaker (the agent's
audio.speak; a desktop without one says nothing, as the SDK's audio_speak)."""

import jarvis_node_compat as host


def speak(text: str):
    try:
        host.ask("audio.speak", {"text": text})
    except Exception:  # noqa: BLE001 — no speaker on this node
        pass
