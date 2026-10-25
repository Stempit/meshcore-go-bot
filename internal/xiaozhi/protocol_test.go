package xiaozhi

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"

	"meshbot/internal/companion"
	"meshbot/internal/meshcore"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeMesh records calls and serves canned data.
type fakeMesh struct {
	msgs        []companion.Message
	dmErr       error
	chErr       error
	queries     []int64
	dmCalls     [][3]string // to, text, ack("0"/"1")
	chCalls     [][3]string
	searches    []string
	searchNames []string // canned SearchContacts result
}

func (f *fakeMesh) SearchContacts(query string) []string {
	f.searches = append(f.searches, query)
	return f.searchNames
}

func (f *fakeMesh) MessagesSince(after int64) ([]companion.Message, int64) {
	f.queries = append(f.queries, after)
	var out []companion.Message
	for _, m := range f.msgs {
		if m.ID > after {
			out = append(out, m)
		}
	}
	if out == nil {
		out = []companion.Message{}
	}
	return out, f.lastAssigned()
}

func (f *fakeMesh) lastAssigned() int64 {
	if len(f.msgs) == 0 {
		return 0
	}
	return f.msgs[len(f.msgs)-1].ID
}

func (f *fakeMesh) SendDM(to, text string, ack bool) error {
	f.dmCalls = append(f.dmCalls, [3]string{to, text, ackFlag(ack)})
	return f.dmErr
}

func (f *fakeMesh) SendChannel(to, text string, ack bool) error {
	f.chCalls = append(f.chCalls, [3]string{to, text, ackFlag(ack)})
	return f.chErr
}

func ackFlag(ack bool) string {
	if ack {
		return "1"
	}
	return "0"
}

func (f *fakeMesh) Contacts() []string { return []string{"Короед", "Wire"} }

func (f *fakeMesh) Channels() []ChannelRef {
	return []ChannelRef{{Index: 0, Name: "public"}, {Index: 1, Name: "private"}}
}

func TestProtocolInitializeEchoesVersion(t *testing.T) {
	s := NewServer(&fakeMesh{}, 0, testLogger())
	out := s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`))

	var resp struct {
		ID     int `json:"id"`
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v (%s)", err, out)
	}
	if resp.ID != 1 || resp.Result.ProtocolVersion != "2025-06-18" {
		t.Errorf("unexpected initialize response: %s", out)
	}
}

func TestProtocolNotificationGetsNoResponse(t *testing.T) {
	s := NewServer(&fakeMesh{}, 0, testLogger())
	if out := s.HandleMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)); out != nil {
		t.Errorf("notification produced a response: %s", out)
	}
}

func TestToolsListAdvertisesAllTools(t *testing.T) {
	s := NewServer(&fakeMesh{}, 0, testLogger())
	out := s.HandleMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`))

	var resp struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Result.Tools) != 5 {
		t.Fatalf("tools/list returned %d tools, want 5", len(resp.Result.Tools))
	}
	for _, want := range []string{"mesh_get_new_messages", "mesh_send_message", "mesh_search_contacts", "mesh_list_contacts", "mesh_list_channels"} {
		if !hasTool(resp.Result.Tools, want) {
			t.Errorf("tool %q missing from tools/list", want)
		}
	}
}

func hasTool(tools []map[string]any, name string) bool {
	for _, tl := range tools {
		if tl["name"] == name {
			return true
		}
	}
	return false
}

func twelveMessages() []companion.Message {
	var msgs []companion.Message
	for i := 1; i <= 12; i++ {
		msgs = append(msgs, companion.Message{ID: int64(i), Type: "dm", Sender: "S", Text: strings.Repeat("x", i) + "|" + string(rune('0'+i))})
	}
	return msgs
}

func TestToolGetMessagesPagesThroughCursor(t *testing.T) {
	mesh := &fakeMesh{msgs: twelveMessages()}
	s := NewServer(mesh, 0, testLogger())

	// First call: capped at 10, cursor advances to the last SHOWN message.
	first := resultText(t, callTool(t, s, "mesh_get_new_messages", nil))
	if !contains(first, "10 new mesh") || !contains(first, "2 more") {
		t.Errorf("first call should show 10 and announce 2 more:\n%s", first)
	}
	// Second call: pages through the rest.
	second := resultText(t, callTool(t, s, "mesh_get_new_messages", nil))
	if !contains(second, "2 new mesh") {
		t.Errorf("second call should show the remaining 2:\n%s", second)
	}
	// Third: everything read.
	third := resultText(t, callTool(t, s, "mesh_get_new_messages", nil))
	if third != "No new mesh messages." {
		t.Errorf("third call = %q, want no new messages", third)
	}
	wantQueries := []int64{0, 10, 12}
	if len(mesh.queries) != len(wantQueries) {
		t.Fatalf("gateway queries = %v, want %v", mesh.queries, wantQueries)
	}
	for i, want := range wantQueries {
		if mesh.queries[i] != want {
			t.Errorf("query %d = %d, want %d", i, mesh.queries[i], want)
		}
	}
}

func TestToolGetMessagesFormatsDMAndChannel(t *testing.T) {
	mesh := &fakeMesh{msgs: []companion.Message{
		{ID: 1, Type: "dm", Sender: "Короед", Text: "hello"},
		{ID: 2, Type: "channel", Channel: "public", Sender: "Wire", Text: "hi all"},
	}}
	s := NewServer(mesh, 0, testLogger())

	text := resultText(t, callTool(t, s, "mesh_get_new_messages", nil))
	for _, want := range []string{`Private from Короед: "hello"`, `Channel public from Wire: "hi all"`} {
		if !contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestToolSendMapsOutcomes(t *testing.T) {
	t.Run("sent", func(t *testing.T) {
		mesh := &fakeMesh{}
		s := NewServer(mesh, 0, testLogger())
		out := callTool(t, s, "mesh_send_message", map[string]any{"type": "dm", "to": "Короед", "text": "hi"})
		if tr := resultTool(t, out); tr.IsError || !contains(resultText(t, out), "sent to Короед") {
			t.Errorf("unexpected result: %+v", out)
		}
		if len(mesh.dmCalls) != 1 || mesh.dmCalls[0][0] != "Короед" || mesh.dmCalls[0][1] != "hi" || mesh.dmCalls[0][2] != "0" {
			t.Errorf("unexpected dm call: %v", mesh.dmCalls)
		}
	})

	t.Run("unknown recipient hints at directories", func(t *testing.T) {
		mesh := &fakeMesh{dmErr: ErrUnknownRecipient}
		s := NewServer(mesh, 0, testLogger())
		out := callTool(t, s, "mesh_send_message", map[string]any{"type": "dm", "to": "ghost", "text": "hi"})
		if tr := resultTool(t, out); !tr.IsError || !contains(resultText(t, out), "not found") || !contains(resultText(t, out), "mesh_list_contacts") {
			t.Errorf("unexpected result: %+v", out)
		}
	})

	t.Run("ambiguous name asks the user to disambiguate", func(t *testing.T) {
		mesh := &fakeMesh{dmErr: &meshcore.AmbiguousMatchError{Names: []string{"Yorick Hamlet", "Yorick 💀"}}}
		s := NewServer(mesh, 0, testLogger())
		out := callTool(t, s, "mesh_send_message", map[string]any{"type": "dm", "to": "Yorick", "text": "hi"})
		text := resultText(t, out)
		if tr := resultTool(t, out); !tr.IsError || !contains(text, "Several recipients match") ||
			!contains(text, "Yorick Hamlet") || !contains(text, "full name") {
			t.Errorf("unexpected result: %+v", out)
		}
	})

	t.Run("no confirmation is not a hard failure", func(t *testing.T) {
		mesh := &fakeMesh{chErr: meshcore.ErrNoConfirmation}
		s := NewServer(mesh, 0, testLogger())
		out := callTool(t, s, "mesh_send_message", map[string]any{"type": "channel", "to": "public", "text": "hi"})
		if tr := resultTool(t, out); !tr.IsError || !contains(resultText(t, out), "may still arrive") {
			t.Errorf("unexpected result: %+v", out)
		}
	})

	t.Run("device rejection", func(t *testing.T) {
		mesh := &fakeMesh{dmErr: meshcore.ErrDeviceRejected}
		s := NewServer(mesh, 0, testLogger())
		out := callTool(t, s, "mesh_send_message", map[string]any{"type": "dm", "to": "Короед", "text": "hi"})
		if tr := resultTool(t, out); !tr.IsError || !contains(resultText(t, out), "rejected") {
			t.Errorf("unexpected result: %+v", out)
		}
	})

	t.Run("validation errors", func(t *testing.T) {
		mesh := &fakeMesh{}
		s := NewServer(mesh, 0, testLogger())
		for _, args := range []map[string]any{
			{"type": "dm", "to": "", "text": "hi"},
			{"type": "dm", "to": "x", "text": " "},
			{"type": "sms", "to": "x", "text": "hi"},
		} {
			out := callTool(t, s, "mesh_send_message", args)
			if tr := resultTool(t, out); !tr.IsError {
				t.Errorf("args %v should fail validation: %+v", args, out)
			}
		}
		if len(mesh.dmCalls)+len(mesh.chCalls) != 0 {
			t.Errorf("invalid args reached the radio: %v %v", mesh.dmCalls, mesh.chCalls)
		}
	})
}

func TestToolListContactsAndChannels(t *testing.T) {
	s := NewServer(&fakeMesh{}, 0, testLogger())

	if text := resultText(t, callTool(t, s, "mesh_list_contacts", nil)); !contains(text, "Короед") || !contains(text, "Wire") {
		t.Errorf("mesh_list_contacts = %q", text)
	}
	if text := resultText(t, callTool(t, s, "mesh_list_channels", nil)); !contains(text, "public (index 0)") {
		t.Errorf("mesh_list_channels = %q", text)
	}
}

func TestToolSearchContacts(t *testing.T) {
	t.Run("matches are listed with the query", func(t *testing.T) {
		mesh := &fakeMesh{searchNames: []string{"Yorick Hamlet", "Yorick 💀"}}
		s := NewServer(mesh, 0, testLogger())
		text := resultText(t, callTool(t, s, "mesh_search_contacts", map[string]any{"query": "yorick"}))
		if !contains(text, `2 contacts match "yorick"`) || !contains(text, "Yorick Hamlet") || !contains(text, "Yorick 💀") {
			t.Errorf("unexpected result: %q", text)
		}
		if len(mesh.searches) != 1 || mesh.searches[0] != "yorick" {
			t.Errorf("unexpected searches: %v", mesh.searches)
		}
	})

	t.Run("singular wording for one match", func(t *testing.T) {
		mesh := &fakeMesh{searchNames: []string{"Yorick 💀"}}
		s := NewServer(mesh, 0, testLogger())
		text := resultText(t, callTool(t, s, "mesh_search_contacts", map[string]any{"query": "yorick"}))
		if !contains(text, `1 contact matches "yorick"`) {
			t.Errorf("unexpected result: %q", text)
		}
	})

	t.Run("no matches", func(t *testing.T) {
		s := NewServer(&fakeMesh{}, 0, testLogger())
		text := resultText(t, callTool(t, s, "mesh_search_contacts", map[string]any{"query": "ghost"}))
		if !contains(text, `No contacts match "ghost"`) {
			t.Errorf("unexpected result: %q", text)
		}
	})

	t.Run("empty query is an error result", func(t *testing.T) {
		mesh := &fakeMesh{}
		s := NewServer(mesh, 0, testLogger())
		out := callTool(t, s, "mesh_search_contacts", map[string]any{"query": "   "})
		if tr := resultTool(t, out); !tr.IsError {
			t.Errorf("empty query should be isError=true: %+v", out)
		}
		if len(mesh.searches) != 0 {
			t.Errorf("empty query reached the mesh: %v", mesh.searches)
		}
	})
}

func TestUnknownToolIsErrorResult(t *testing.T) {
	s := NewServer(&fakeMesh{}, 0, testLogger())
	out := callTool(t, s, "no_such_tool", nil)
	if tr := resultTool(t, out); !tr.IsError {
		t.Errorf("unknown tool should be isError=true: %+v", out)
	}
}

// callTool runs a tools/call through HandleMessage and returns its raw result JSON.
func callTool(t *testing.T, s *Server, name string, args map[string]any) []byte {
	t.Helper()
	req := map[string]any{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	out := s.HandleMessage(raw)
	if out == nil {
		t.Fatal("tools/call produced no response")
	}
	var resp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal response: %v (%s)", err, out)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %s", resp.Error.Message)
	}
	return resp.Result
}

func resultText(t *testing.T, result json.RawMessage) string {
	t.Helper()
	return resultTool(t, result).Content[0].Text
}

func resultTool(t *testing.T, result json.RawMessage) toolResult {
	t.Helper()
	var tr toolResult
	if err := json.Unmarshal(result, &tr); err != nil {
		t.Fatalf("unmarshal tool result: %v (%s)", err, result)
	}
	return tr
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
