# bridge

External integrations. Each bridge is independent and, where it feeds the mesh, delivers
through an injected callback rather than importing the
[dispatch](../dispatch/README.md)/[meshcore](../meshcore/README.md) pipeline directly.

| Package | What it is |
|---------|------------|
| [`tgbridge/`](tgbridge/README.md) | Telegram bridge (both directions: sources → mesh, mesh → targets) |
| [`mqttbridge/`](mqttbridge/README.md) | MQTT publishing to a custom broker + Let's Mesh analyzers (JWT/Ed25519) |
| [`llm/`](llm/README.md) | LLM providers (OpenRouter, Ollama) — used by `.llm` and shrink |
| [`agentclient/`](agentclient/README.md) | universal AI agent client (OpenAI-compatible / legacy picoclaw WebSocket) — used by `.agent` |
| [`alerts/`](alerts/README.md) | hazard-alert poller (FPV/UAV incident cards, МЧС RSS) → mesh channel |
