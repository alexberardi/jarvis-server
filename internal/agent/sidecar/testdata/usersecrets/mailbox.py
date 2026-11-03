"""An IJarvisCommand whose settings follow a user-scoped one, as the email package's do: the
IMAP host is asked for only once that user's provider isn't gmail."""

from typing import List

from jarvis_command_sdk import CommandResponse, IJarvisCommand, JarvisParameter, JarvisSecret, JarvisStorage
from jarvis_command_sdk.command import CommandExample

_storage = JarvisStorage("mailbox")


class MailboxCommand(IJarvisCommand):
    @property
    def command_name(self) -> str:
        return "mailbox"

    @property
    def description(self) -> str:
        return "Read mail."

    @property
    def parameters(self) -> List[JarvisParameter]:
        return []

    @property
    def required_secrets(self) -> list:
        out = [JarvisSecret("MAIL_PROVIDER", "Mail provider", "user", "string", required=False, is_sensitive=False,
                            enum_values=["gmail", "proton"])]
        if (_storage.get_secret("MAIL_PROVIDER", scope="user") or "gmail") != "gmail":
            out.append(JarvisSecret("MAIL_IMAP_HOST", "IMAP host", "user", "string", required=False,
                                    is_sensitive=False))
        return out

    @property
    def keywords(self) -> List[str]:
        return ["mailbox"]

    def generate_prompt_examples(self) -> List[CommandExample]:
        return [CommandExample(voice_command="read my mail", expected_parameters={}, is_primary=True)]

    def generate_adapter_examples(self) -> List[CommandExample]:
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs) -> CommandResponse:
        return CommandResponse(context_data={}, success=True)
