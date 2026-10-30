package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stempit/meshcore-go-bot/internal/config"
)

// Configure must decode the Agent section through the embedded
// agentclient.Config (yaml inline) — the type/url/token/model keys live there.
func TestConfigureDecodesAgentSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := `
LLM:
  enabled: false
Agent:
  enabled: true
  url: "http://127.0.0.1:9999"
  token: "t0k"
  model: "m1"
  timeout_sec: 42
  bot_name: "Yorick 💀"
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Load(path); err != nil {
		t.Fatalf("Load: %v", err)
	}

	var p plugin
	if err := p.Configure(config.Sections()); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if !conf.Enabled {
		t.Error("enabled must decode")
	}
	if conf.URL != "http://127.0.0.1:9999" || conf.Token != "t0k" || conf.Model != "m1" {
		t.Errorf("inline adapter fields = %q/%q/%q, want url/token/model decoded", conf.URL, conf.Token, conf.Model)
	}
	if conf.TimeoutSec != 42 || conf.BotName != "Yorick 💀" {
		t.Errorf("plugin fields = %d/%q, want 42/Yorick 💀", conf.TimeoutSec, conf.BotName)
	}
	if conf.Type != "openai" {
		t.Errorf("type = %q, want defaulted to openai", conf.Type)
	}
	if client == nil {
		t.Error("client must be built for an enabled, well-formed section")
	}
}
