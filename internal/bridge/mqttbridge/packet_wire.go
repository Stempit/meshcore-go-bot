// Packet wire-format parsing: MeshCore's on-air packet layout (header byte,
// transport codes, path field) and the helpers derived from it. Pure functions
// over []byte so the offsets and the packet hash can be unit-tested against
// real frames.
package mqttbridge

import (
	"crypto/sha256"
	"encoding/hex"
)

func calculatePacketHash(data []byte) string {
	payloadType := uint8(getPayloadType(data))
	payload := extractPayload(data)

	h := sha256.New()
	h.Write([]byte{payloadType})
	if payloadType == 9 { // PAYLOAD_TYPE_TRACE
		h.Write([]byte{getPathLenByte(data)})
	}
	h.Write(payload)

	sum := h.Sum(nil)
	return hex.EncodeToString(sum[:8]) // MAX_HASH_SIZE = 8
}

// pathLenOffset returns the index of the path_len byte in a raw packet, or -1 if
// the packet is too short to contain a header. TRANSPORT routes (route type 0x00
// or 0x03) carry an extra 4-byte transport_codes field before path_len.
func pathLenOffset(data []byte) int {
	if len(data) < 2 {
		return -1
	}
	headerLen := 2
	if routeType := data[0] & 0x03; routeType == 0x00 || routeType == 0x03 {
		headerLen = 6
	}
	if len(data) < headerLen {
		return -1
	}
	return headerLen - 1
}

// payloadOffset returns the index at which the payload begins (after the header
// and the variable-length path), or -1 if the packet is malformed or carries no
// payload. The path_len byte encodes hash_count (bits 0-5) and bytes-per-hop
// (bits 6-7, +1 → 1/2/3); the path occupies hash_count * bytes-per-hop bytes.
func payloadOffset(data []byte) int {
	i := pathLenOffset(data)
	if i < 0 {
		return -1
	}
	pathLenByte := data[i]
	hashCount := int(pathLenByte & 0x3F)
	hashSize := int((pathLenByte>>6)&0x03) + 1
	offset := i + 1 + hashCount*hashSize
	if offset >= len(data) {
		return -1
	}
	return offset
}

// extractPayload returns the payload slice from a raw packet.
func extractPayload(data []byte) []byte {
	off := payloadOffset(data)
	if off < 0 {
		return nil
	}
	return data[off:]
}

// getPathLenByte returns the path_len byte from a raw packet.
func getPathLenByte(data []byte) uint8 {
	i := pathLenOffset(data)
	if i < 0 {
		return 0
	}
	return data[i]
}
func directionStr(isTX bool) string {
	if isTX {
		return "tx"
	}
	return "rx"
}

// MeshCore packet header byte layout (from Packet.h):
//
//	bits [1:0] = route type  (PH_ROUTE_MASK = 0x03)
//	bits [5:2] = payload type (PH_TYPE_MASK = 0x0F, shifted by PH_TYPE_SHIFT = 2)
//	bits [7:6] = payload version
//
// Route type constants:
//
//	0x00 ROUTE_TYPE_TRANSPORT_FLOOD
//	0x01 ROUTE_TYPE_FLOOD
//	0x02 ROUTE_TYPE_DIRECT
//	0x03 ROUTE_TYPE_TRANSPORT_DIRECT
//
// Payload type constants (relevant subset):
//
//	0x02 PAYLOAD_TYPE_TXT_MSG   – plain text DM
//	0x04 PAYLOAD_TYPE_ADVERT    – node advertisement
//	0x05 PAYLOAD_TYPE_GRP_TXT   – group/channel text message

// getPayloadType extracts the payload type from the header byte.
func getPayloadType(data []byte) int {
	if len(data) == 0 {
		return 0
	}
	return int((data[0] >> 2) & 0x0F)
}

// getRouteType returns a single-character route label from the header byte.
func getRouteType(data []byte) string {
	if len(data) == 0 {
		return "U"
	}
	switch data[0] & 0x03 {
	case 0x01:
		return "F" // ROUTE_TYPE_FLOOD
	case 0x02:
		return "D" // ROUTE_TYPE_DIRECT
	case 0x00, 0x03:
		return "T" // ROUTE_TYPE_TRANSPORT_FLOOD / ROUTE_TYPE_TRANSPORT_DIRECT
	default:
		return "U"
	}
}

// getPayloadLen returns the payload byte count from the raw packet.
//
// Wire format (from Packet.cpp writeTo()):
//
//	[header(1)] [transport_codes(4) — only for TRANSPORT routes] [path_len(1)] [path...] [payload...]
func getPayloadLen(data []byte) int {
	off := payloadOffset(data)
	if off < 0 {
		return 0
	}
	return len(data) - off
}
