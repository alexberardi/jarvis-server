from jarvis_command_sdk import CommandResponse, IJarvisCommand
from jarvis_command_sdk.command import CommandExample


class HelloCommand(IJarvisCommand):
    command_name = "hello"
    description = "Say hello."
    parameters = []
    keywords = ["hello"]
    required_secrets = []

    def generate_prompt_examples(self):
        return [CommandExample(voice_command="hello", expected_parameters={}, is_primary=True)]

    def generate_adapter_examples(self):
        return self.generate_prompt_examples()

    def run(self, request_info, **kwargs):
        return CommandResponse(success=True, context_data={"message": "hi"})
