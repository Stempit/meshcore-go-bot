package mqttbridge

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// MeshCore packet header: bits [1:0] route, bits [5:2] payload type.
func TestHeaderByteParsing(t *testing.T) {
	cases := []struct {
		name   string
		header byte
		ptype  int
		route  string
	}{
		{"group text, flood", 0x15, 0x05, "F"},
		{"txt dm, direct", 0x0A, 0x02, "D"},
		{"transport direct", 0x0B, 0x02, "T"},
		{"advert, flood", 0x11, 0x04, "F"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getPayloadType([]byte{tc.header}); got != tc.ptype {
				t.Errorf("getPayloadType = %#x, want %#x", got, tc.ptype)
			}
			if got := getRouteType([]byte{tc.header}); got != tc.route {
				t.Errorf("getRouteType = %q, want %q", got, tc.route)
			}
		})
	}
	if got := getPayloadType(nil); got != 0 {
		t.Errorf("empty packet type = %d, want 0", got)
	}
	if got := getRouteType(nil); got != "U" {
		t.Errorf("empty packet route = %q, want U", got)
	}
}

// Flood/direct packets carry [header(1)][path_len(1)][path…][payload…];
// transport routes carry 4 extra transport-code bytes before path_len.
// path_len encodes hash_count in bits 0-5 and bytes-per-hop in bits 6-7 (+1):
// 0x41 = 1 hash × 2 bytes, 0x40 = 0 hashes × 1 byte.
func TestPathAndPayloadOffsets(t *testing.T) {
	flood := []byte{0x15, 0x41, 0xAA, 0xBB, 'h', 'i'}
	if got := pathLenOffset(flood); got != 1 {
		t.Fatalf("flood pathLenOffset = %d, want 1", got)
	}
	if got := payloadOffset(flood); got != 4 {
		t.Fatalf("flood payloadOffset = %d, want 4 (1 hash × 2 bytes)", got)
	}
	if got := string(extractPayload(flood)); got != "hi" {
		t.Fatalf("flood payload = %q, want hi", got)
	}
	if got := getPayloadLen(flood); got != 2 {
		t.Fatalf("flood payloadLen = %d, want 2", got)
	}

	transport := []byte{0x0B, 1, 2, 3, 4, 0x40, 'x'}
	if got := pathLenOffset(transport); got != 5 {
		t.Fatalf("transport pathLenOffset = %d, want 5", got)
	}
	if got := payloadOffset(transport); got != 6 {
		t.Fatalf("transport payloadOffset = %d, want 6 (0 hashes)", got)
	}
	if got := string(extractPayload(transport)); got != "x" {
		t.Fatalf("transport payload = %q, want x", got)
	}

	if got := pathLenOffset([]byte{0x15}); got != -1 {
		t.Fatalf("short packet pathLenOffset = %d, want -1", got)
	}
	// path_len byte claiming more path bytes than the packet holds.
	if got := payloadOffset([]byte{0x15, 0x41, 0xAA}); got != -1 {
		t.Fatalf("overlong path payloadOffset = %d, want -1", got)
	}
}

// calculatePacketHash mirrors Packet::calculatePacketHash: SHA-256 over
// [payload_type | path_len (TRACE only) | payload], truncated to 8 bytes.
func TestCalculatePacketHash(t *testing.T) {
	txt := []byte{0x15, 0x41, 0xAA, 0xBB, 'h', 'i'}
	h := sha256.New()
	h.Write([]byte{0x05}) // payload type from the header
	h.Write([]byte{'h', 'i'})
	want := hex.EncodeToString(h.Sum(nil)[:8])
	if got := calculatePacketHash(txt); got != want {
		t.Fatalf("txt hash = %s, want %s", got, want)
	}

	// TRACE (payload type 9) includes the path_len byte in the digest.
	trace := []byte{0x26, 0x40, 'p'} // type (9<<2)|2 = 0x26, direct, 0 hashes
	h2 := sha256.New()
	h2.Write([]byte{0x09})
	h2.Write([]byte{0x40})
	h2.Write([]byte{'p'})
	want2 := hex.EncodeToString(h2.Sum(nil)[:8])
	if got := calculatePacketHash(trace); got != want2 {
		t.Fatalf("trace hash = %s, want %s", got, want2)
	}

	if got := calculatePacketHash(nil); got == "" {
		t.Error("empty packet must still hash (type 0, empty payload)")
	}
}

func TestDirectionStr(t *testing.T) {
	if directionStr(true) != "tx" || directionStr(false) != "rx" {
		t.Fatal("directionStr wrong")
	}
}
