"""Browser fixtures: commands that keep records through JarvisStorage, as Pantry packages do.

The same files are loaded by the Python node's own command_data_handler.py
(tools/golden/export_command_data.py) and by the agent's sidecar (the Go golden test), so the
two answer the app's browser requests from the same commands.
"""

from typing import List

from jarvis_command_sdk import CommandResponse, FieldSpec, IJarvisCommand, JarvisParameter, RecordSummary
from jarvis_command_sdk.command import CommandExample


class _Fixture(IJarvisCommand):
    @property
    def description(self) -> str:
        return f"{self.command_name} fixture"

    @property
    def parameters(self) -> List[JarvisParameter]:
        return []

    @property
    def required_secrets(self) -> list:
        return []

    @property
    def keywords(self) -> List[str]:
        return [self.command_name]

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command=self.command_name, expected_parameters={}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs) -> CommandResponse:
        return CommandResponse(context_data={}, success=True)


class NotesCommand(_Fixture):
    """Editable records, the SDK's default create (owner-stamped), a custom summary."""

    @property
    def command_name(self) -> str:
        return "notes"

    @property
    def data_browser_supports_create(self) -> bool:
        return True

    def editable_fields(self) -> List[FieldSpec]:
        return [
            FieldSpec("title", "string", label="Title", required=True),
            FieldSpec("body", "text", placeholder="Write something"),
            FieldSpec("done", "bool"),
            FieldSpec("scope", "enum", enum_values=["personal", "household"], editable=False, create_only=True),
            FieldSpec("owner", "user_ref", editable=False),
            FieldSpec("items", "array", item_type="object",
                      fields=[FieldSpec("name", "string"), FieldSpec("qty", "int")]),
            FieldSpec("data_key", "id"),  # never writable, whatever the spec says
        ]

    def display_summary(self, record: dict) -> RecordSummary:
        if record.get("title") == "boom":
            raise RuntimeError("bad record")
        return RecordSummary(title=record.get("title") or "untitled", subtitle=record.get("body"), icon="note")


class JournalCommand(_Fixture):
    """The SDK's defaults: mode enabled, no fields, no create, the first string as the title.
    Its run reports get_all's bookkeeping keys, which the Pi's repository adds."""

    @property
    def command_name(self) -> str:
        return "journal"

    def run(self, request_info, **kwargs) -> CommandResponse:
        from jarvis_command_sdk import JarvisStorage

        rows = JarvisStorage("journal").get_all()
        return CommandResponse(context_data={"rows": [[r.get("_data_key"), r.get("_expires_at"), list(r)] for r in rows]},
                               success=True)


class AlarmCommand(_Fixture):
    """Read-only, stored under another name (as the Pi's reminder is under set_reminder)."""

    @property
    def command_name(self) -> str:
        return "alarm"

    @property
    def data_browser_storage_name(self) -> str:
        return "alarm_store"

    @property
    def data_browser_mode(self) -> str:
        return "readonly"

    def editable_fields(self) -> List[FieldSpec]:
        return [FieldSpec("at", "datetime")]


class VaultCommand(_Fixture):
    """Hidden from the browser."""

    @property
    def command_name(self) -> str:
        return "vault"

    @property
    def data_browser_mode(self) -> str:
        return "disabled"


class DoseCommand(_Fixture):
    """A custom create with domain validation and its own key."""

    @property
    def command_name(self) -> str:
        return "dose"

    @property
    def data_browser_supports_create(self) -> bool:
        return True

    def editable_fields(self) -> List[FieldSpec]:
        return [FieldSpec("med", "string"), FieldSpec("at", "time")]

    def data_browser_create(self, fields, requesting_user_id):
        from jarvis_command_sdk import JarvisStorage

        if not fields.get("at"):
            raise ValueError("dose time required")
        if fields.get("med") == "crash":
            raise KeyError("med")
        key = f"{fields['med']}@{fields['at']}"
        record = {"med": fields["med"], "at": fields["at"], "user_id": requesting_user_id, "_internal": 1}
        JarvisStorage(self.data_browser_storage_name).save(key, record)
        return key, record
