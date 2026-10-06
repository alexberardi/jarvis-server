# jarvis-server

This repository is the single-binary Go server for Jarvis, a private, self-hosted voice assistant. It is a work in progress.

`jarvisd` replaces the Python microservices with one executable:

- **Services it replaces:** config, auth, logs, notifications, command-center, LLM proxy, STT/TTS, recipes and OCR.
- **Infrastructure:** SQLite storage, a job queue and an MQTT broker, all embedded.
- **Inference:** text-to-speech and speaker recognition run in-process. GPU inference engines are downloaded on first run.

Supported targets: Linux (amd64, arm64), macOS (arm64) and Windows (amd64).

- [docs/PLAN.md](docs/PLAN.md) explains the architecture, migration phases and verification strategy.
- [docs/STATUS.md](docs/STATUS.md) tracks current progress.

License: AGPL-3.0.
