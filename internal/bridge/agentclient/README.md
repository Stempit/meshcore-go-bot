# agentclient — universal AI agent client

Package `agentclient` connects the bot to a remote AI agent. The wire protocol
is selected by `Agent.type` in `config.yaml`; history is managed by the agent
server, so the client only carries a stable per-scope session ID (derived by
the [`.agent`](../../commands/agent/README.md) command: one session per
channel, one per DM sender).

```go
client, err := agentclient.New(agentclient.Config{
    Type:  "openai",
    URL:   "http://127.0.0.1:8900",
    Token: "…",
})
parts, err := client.Ask(ctx, "mesh-ch-general", "what's the weather?")
```

## Adapters

| `type` | Speaks | Works with |
|---|---|---|
| `openai` (default) | `POST <url>/v1/chat/completions`, bearer token, `session_id` in the body, non-streaming | [nanobot](https://github.com/HKUDS/nanobot), LiteLLM, vLLM, llama.cpp server, OpenRouter, most agent gateways |
| `picoclaw` | legacy Pico Protocol WebSocket (`message.send` / `message.create` / `typing.*`) | picoclaw gateways (`ws://host:18790/pico/ws`, **not** the port-18800 dashboard) |

### Connecting your own agent (nanobot example)

1. Enable and start the API plugin on the agent side:

   ```bash
   nanobot plugins enable api
   nanobot serve   # serves 127.0.0.1:8900 by default; NANOBOT_API_KEY enables bearer auth
   ```

2. Point the bot at it (`config.yaml`):

   ```yaml
   Agent:
     enabled: true
     type: openai
     url: "http://127.0.0.1:8900"
     token: "<NANOBOT_API_KEY>"   # omit for a keyless local server
     timeout_sec: 180             # agent runs can be slow (tools); default 180
     bot_name: "Yorick 💀"        # @[bot_name] mentions route here
   ```

3. `.agent <question>` or `@[BotName] <question>` on the mesh.

The adapter sends `{"messages":[{"role":"user","content":"…"}],"session_id":"…"}`;
`model` is forwarded only when set (nanobot takes the model from its own
config). Responses are parsed defensively: `choices[0].message.content` is
required, error bodies are truncated, and the read is capped at 1 MiB.

### Adding an adapter

Implement `Client` (one `Ask(ctx, sessionID, text) ([]string, error)` method
returning the agent's visible reply parts in order) in a new file, and add a
case to `New`. Keep request construction and response parsing defensive — the
server is untrusted third-party input.
