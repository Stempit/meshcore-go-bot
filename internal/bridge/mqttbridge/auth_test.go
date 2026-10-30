package mqttbridge

import (
	"bytes"
	"testing"
)

func TestSignWithOrlpKeyRejectsBadKeySizes(t *testing.T) {
	b := &MQTTBridge{}
	msg := []byte("m")
	if _, err := b.signWithOrlpKey(msg, make([]byte, 63), make([]byte, 32)); err == nil {
		t.Error("63-byte expanded key must be rejected")
	}
	if _, err := b.signWithOrlpKey(msg, make([]byte, 64), make([]byte, 31)); err == nil {
		t.Error("31-byte public key must be rejected")
	}
}

// The orlp format is deterministic given the same expanded key: same input →
// identical signature, different input → different signature.
func TestSignWithOrlpKeyDeterministic(t *testing.T) {
	b := &MQTTBridge{}
	expanded := bytes.Repeat([]byte{0x42}, 64)
	pub := bytes.Repeat([]byte{0x07}, 32)

	sig1, err := b.signWithOrlpKey([]byte("message"), expanded, pub)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig2, err := b.signWithOrlpKey([]byte("message"), expanded, pub)
	if err != nil {
		t.Fatalf("sign again: %v", err)
	}
	if !bytes.Equal(sig1, sig2) {
		t.Fatal("same message and key must produce an identical signature")
	}
	if len(sig1) != 64 {
		t.Fatalf("signature = %d bytes, want 64 (R‖s)", len(sig1))
	}

	sig3, err := b.signWithOrlpKey([]byte("other message"), expanded, pub)
	if err != nil {
		t.Fatalf("sign other: %v", err)
	}
	if bytes.Equal(sig1, sig3) {
		t.Fatal("a different message must produce a different signature")
	}
}
