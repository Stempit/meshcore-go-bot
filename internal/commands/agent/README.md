# agent — universal AI agent command

Package `agent` registers the `.agent` command and an `@[BotName]` mention
interceptor, both routed to the user's AI agent through
[agentclient](../../bridge/agentclient/README.md). The backend is picked by
`Agent.type` — `openai` for OpenAI-compatible APIs (nanobot, LiteLLM, vLLM,
llama.cpp, …) or `picoclaw` for a legacy picoclaw gateway.
Responses are auto-shrunk at the send site (the `agent` split override).

```
.agent <вопрос>      – ask your AI agent
```

`agent` is a `Matcher` interceptor at **Priority 20** — above bbs (10) — so an
`@[BotName]` mention beats an active BBS session; the mention prefix is cut
from the prompt before handling.

Sessions: channel messages share one agent session per channel; DMs get one per
sender. Not configured → `agent not configured`; connection error →
`agent connection error`; all network slots busy → the `busy` message.

## Config

YAML key `Agent`. The `.agent` trigger is always routed (so it can reply
"not configured") unless `enabled: false` hides it entirely.

```yaml
Agent:
  enabled: false
  type: openai                  # openai (default) | picoclaw
  url: "http://127.0.0.1:8900"  # openai base URL; picoclaw: ws://host:18790/pico/ws
  token: ""                     # bearer token; empty = none (keyless local server)
  model: ""                     # openai only; empty = server default
  timeout_sec: 180
  bot_name: "Yorick 💀"         # mesh display name; @[bot_name] messages are routed to the agent
  # mesh-fit variant for .agent replies is set in Shrink.overrides (default: split)
```

The client is built at startup: an unknown `type` or a missing `url` on an
enabled section fails the bot loudly instead of failing at first use.
