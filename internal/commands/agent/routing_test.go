package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stempit/meshcore-go-bot/internal/bridge/agentclient"
	"github.com/stempit/meshcore-go-bot/internal/support/i18n"
	"github.com/stempit/meshcore-go-bot/internal/support/netgate"
	"github.com/stempit/meshcore-go-bot/internal/support/registry"
)

// newCtx builds a handler input like dispatch does; cfg is nil so the language
// defaults to i18n.Base.
func newCtx(text, channel string) registry.Ctx {
	return registry.NewCtx(text, channel, "01234567", "tester", nil, nil)
}

// resetConf restores the disabled default after a test mutates the
// package-level config/client singletons.
func resetConf(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		conf = cfg{}
		client = nil
	})
}

// The trigger string, the ".agent " prefix rule and the mention matcher are
// this command's contract with dispatch — a rename-class regression here must
// fail loudly.
func TestMatchesRouting(t *testing.T) {
	resetConf(t)
	p := plugin{}

	// Disabled: the mention must not match (config-aware), while the bare
	// trigger check stays ungated here — hiding a disabled plugin from routing
	// is Dispatch's job (registry skips !Enabled before matching).
	conf = cfg{}
	if p.Matches(newCtx("@[Yorick 💀] hi", "")) {
		t.Error("disabled plugin matched a mention")
	}
	if !p.Matches(newCtx(".agent hi", "")) {
		t.Error("trigger matching is ungated at this layer; Dispatch owns the enable gate")
	}
	// The enabled gate matters even with a bot name configured — otherwise a
	// bot_name left in config would hijack mentions while disabled.
	conf = cfg{BotName: "Yorick 💀"}
	if p.Matches(newCtx("@[Yorick 💀] hi", "")) {
		t.Error("mention matched with enabled unset")
	}

	conf = cfg{Enabled: true, BotName: "Yorick 💀"}
	cases := []struct {
		text string
		want bool
	}{
		{".agent", true},
		{".agent hello", true},
		{".AGENT hello", true},  // Lower is precomputed case-insensitively
		{".agentx", false},      // Prefix rule: no mid-word match
		{".agentology", false},  //
		{"hello .agent", false}, // prefix must lead
		{"@[Yorick 💀] hello", true},
		{"@[yorick 💀] hello", true}, // mention matches case-insensitively
		{"@[Yorick] hello", false},  // partial name is not the configured mention
		{"@[y💀] hello", false},      // different bot name
		{"hello", false},
	}
	for _, tc := range cases {
		if got := p.Matches(newCtx(tc.text, "")); got != tc.want {
			t.Errorf("Matches(%q) = %v, want %v", tc.text, got, tc.want)
		}
	}
}

// Both entry points (mention and trigger) must cut their prefix and forward
// the prompt to the agent client, returning its reply.
func TestHandleRoutesToAgent(t *testing.T) {
	resetConf(t)

	var gotSession, gotText string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotSession, _ = body["session_id"].(string)
		if msgs, _ := body["messages"].([]any); len(msgs) > 0 {
			if m, _ := msgs[0].(map[string]any); m != nil {
				gotText, _ = m["content"].(string)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	}))
	defer srv.Close()

	conf = cfg{Enabled: true, Type: "openai", URL: srv.URL, BotName: "Yorick 💀"}
	var err error
	if client, err = agentclient.New(agentclient.Config{Type: "openai", URL: srv.URL}); err != nil {
		t.Fatalf("New: %v", err)
	}
	p := plugin{}

	for _, text := range []string{"@[Yorick 💀] hi there", ".agent hi there", ".AGENT hi there"} {
		replies, err := p.Handle(newCtx(text, ""))
		if err != nil {
			t.Fatalf("Handle(%q): %v", text, err)
		}
		if len(replies) != 1 || replies[0] != "pong" {
			t.Fatalf("Handle(%q) replies = %q, want [pong]", text, replies)
		}
		if gotText != "hi there" {
			t.Errorf("Handle(%q) prompt = %q, want hi there", text, gotText)
		}
		if gotSession != "mesh-dm-3031323334353637" {
			t.Errorf("Handle(%q) session = %q, want DM scope", text, gotSession)
		}
	}
}

// The fallback ladder before the network call: usage → not_configured → busy.
func TestProcessAgentFallbacks(t *testing.T) {
	resetConf(t)

	// Empty prompt → usage, before any gate.
	if replies, _ := processAgent(i18n.Base, "   ", "k", ""); replies[0] != tr.T(i18n.Base, "usage") {
		t.Errorf("empty prompt replies = %q, want usage", replies)
	}

	// Disabled → the defensive not_configured fallback (Dispatch never routes
	// here; see config.go Enabled).
	conf = cfg{}
	if replies, _ := processAgent(i18n.Base, "hi", "k", ""); replies[0] != tr.T(i18n.Base, "not_configured") {
		t.Errorf("disabled replies = %q, want not_configured", replies)
	}

	// All netgate slots held → busy, before the client is touched.
	conf = cfg{Enabled: true}
	var releases []func()
	t.Cleanup(func() {
		for _, r := range releases {
			r()
		}
	})
	for i := 0; i < 4; i++ { // netgate.maxConcurrent
		release, ok := netgate.TryAcquire()
		if !ok {
			t.Fatalf("netgate slot %d must be acquirable", i+1)
		}
		releases = append(releases, release)
	}
	if replies, _ := processAgent(i18n.Base, "hi", "k", ""); replies[0] != tr.T(i18n.Base, "busy") {
		t.Errorf("saturated replies = %q, want busy", replies)
	}
}

// client.Ask failure maps to the localized connection_error, not a raw error.
func TestProcessAgentConnectionError(t *testing.T) {
	resetConf(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	conf = cfg{Enabled: true, Type: "openai", URL: srv.URL}
	var err error
	if client, err = agentclient.New(agentclient.Config{Type: "openai", URL: srv.URL}); err != nil {
		t.Fatalf("New: %v", err)
	}
	replies, _ := processAgent(i18n.Base, "hi", "k", "")
	if len(replies) != 1 || replies[0] != tr.T(i18n.Base, "connection_error") {
		t.Errorf("replies = %q, want connection_error", replies)
	}
}
