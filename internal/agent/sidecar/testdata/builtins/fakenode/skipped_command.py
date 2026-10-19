from jarvis_command_sdk import CommandResponse, IJarvisCommand
from jarvis_command_sdk.command import CommandExample


class SkippedCommand(IJarvisCommand):
    command_name = "skipped"
    description = "Say hello."
    parameters = []
    keywords = ["skipped"]
    required_secrets = []

    def generate_prompt_examples(self):
        return [CommandExample(voice_command="skipped", expected_parameters={}, is_primary=True)]

    def generate_adapter_examples(self):
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs):
        return CommandResponse(success=True, context_data={"message": "hi"})
