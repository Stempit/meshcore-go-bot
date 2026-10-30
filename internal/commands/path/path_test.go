package path

import "testing"

// Regression: the route builder slices pk[:2] off every key. A stats source
// returning a truncated public_key_hex used to panic the handler; validateStats
// must drop malformed nodes (and edges referencing them) at the boundary.
func TestValidateStatsDropsMalformedNodes(t *testing.T) {
	resp := &statsResponse{
		Nodes: []statsNode{
			{PublicKeyHex: "aabbcc", Name: "good"},
			{PublicKeyHex: "a", Name: "too short"},  // pk[:2] would panic
			{PublicKeyHex: "zzzz", Name: "not hex"}, // hash list would corrupt
			{PublicKeyHex: "abc", Name: "odd length"},
			{PublicKeyHex: "", Name: "empty"},
		},
		Edges: []statsEdge{
			{NodeA: "aabbcc", NodeB: "a"},      // references dropped node
			{NodeA: "aabbcc", NodeB: "AABBCC"}, // self-edge, valid case-insensitively
		},
	}
	validateStats(resp)

	if len(resp.Nodes) != 1 || resp.Nodes[0].Name != "good" {
		t.Fatalf("nodes = %+v, want only the well-formed one", resp.Nodes)
	}
	// The dropped-node edge is gone; the case-insensitive match survives.
	if len(resp.Edges) != 1 || resp.Edges[0].NodeB != "AABBCC" {
		t.Fatalf("edges = %+v, want only the valid edge", resp.Edges)
	}
}

func TestValidateStatsKeepsEmptyGraph(t *testing.T) {
	resp := &statsResponse{}
	validateStats(resp)
	if len(resp.Nodes) != 0 || len(resp.Edges) != 0 {
		t.Fatalf("empty stats must stay empty, got %+v", resp)
	}
}

func TestIsHexKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"aabb", true},
		{"AABB", true},
		{"a", false},    // below the 2-char minimum
		{"", false},     // empty
		{"abc", false},  // odd length
		{"zzzz", false}, // not hex
		{"aabbccdd", true},
	}
	for _, tc := range cases {
		if got := isHexKey(tc.in); got != tc.want {
			t.Errorf("isHexKey(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
