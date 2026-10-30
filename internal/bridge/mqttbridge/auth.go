// Analyzer authentication: per-region JWT tokens signed with the device's
// Ed25519 key in the orlp wire format.
package mqttbridge

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"filippo.io/edwards25519"
)

// GTS Root R4 Certificate for Let's Mesh Analyzer (Google Trust Services)
const gtsRootR4Cert = `-----BEGIN CERTIFICATE-----
MIIDejCCAmKgAwIBAgIQf+UwvzMTQ77dghYQST2KGzANBgkqhkiG9w0BAQsFADBX
MQswCQYDVQQGEwJCRTEZMBcGA1UEChMQR2xvYmFsU2lnbiBudi1zYTEQMA4GA1UE
CxMHUm9vdCBDQTEbMBkGA1UEAxMSR2xvYmFsU2lnbiBSb290IENBMB4XDTIzMTEx
NTAzNDMyMVoXDTI4MDEyODAwMDA0MlowRzELMAkGA1UEBhMCVVMxIjAgBgNVBAoT
GUdvb2dsZSBUcnVzdCBTZXJ2aWNlcyBMTEMxFDASBgNVBAMTC0dUUyBSb290IFI0
MHYwEAYHKoZIzj0CAQYFK4EEACIDYgAE83Rzp2iLYK5DuDXFgTB7S0md+8Fhzube
Rr1r1WEYNa5A3XP3iZEwWus87oV8okB2O6nGuEfYKueSkWpz6bFyOZ8pn6KY019e
WIZlD6GEZQbR3IvJx3PIjGov5cSr0R2Ko4H/MIH8MA4GA1UdDwEB/wQEAwIBhjAd
BgNVHSUEFjAUBggrBgEFBQcDAQYIKwYBBQUHAwIwDwYDVR0TAQH/BAUwAwEB/zAd
BgNVHQ4EFgQUgEzW63T/STaj1dj8tT7FavCUHYwwHwYDVR0jBBgwFoAUYHtmGkUN
l8qJUC99BM00qP/8/UswNgYIKwYBBQUHAQEEKjAoMCYGCCsGAQUFBzAChhpodHRw
Oi8vaS5wa2kuZ29vZy9nc3IxLmNydDAtBgNVHR8EJjAkMCKgIKAehhxodHRwOi8v
Yy5wa2kuZ29vZy9yL2dzcjEuY3JsMBMGA1UdIAQMMAowCAYGZ4EMAQIBMA0GCSqG
SIb3DQEBCwUAA4IBAQAYQrsPBtYDh5bjP2OBDwmkoWhIDDkic574y04tfzHpn+cJ
odI2D4SseesQ6bDrarZ7C30ddLibZatoKiws3UL9xnELz4ct92vID24FfVbiI1hY
+SW6FoVHkNeWIP0GCbaM4C6uVdF5dTUsMVs/ZbzNnIdCp5Gxmx5ejvEau8otR/Cs
kGN+hr/W5GvT1tMBjgWKZ1i4//emhA1JG1BbPzoLJQvyEotc03lXjTaCzv8mEbep
8RqZ7a2CPsgRbuvTPBwcOMBBmuFeU88+FSBX6+7iP0il8b4Z0QFqIwwMHfs/L6K1
vKAIiFDg0A4HnVWX+mYAMMLo1VKQaFfYwcfMFjPrPB37y0PBjBsHqWQ=
-----END CERTIFICATE-----`

// ============================================================================
// Authentication
// ============================================================================

// getAuthToken returns the current auth token for analyzer servers
func (b *MQTTBridge) getAuthToken(region string) string {
	br := b.brokers[region]
	if br == nil {
		return ""
	}

	// Renew token if expiring soon
	if time.Until(br.tokenExpires) < 5*time.Minute {
		b.renewAuthToken(region)
	}

	return br.authToken
}

// renewAuthToken renews the JWT token for analyzer servers
func (b *MQTTBridge) renewAuthToken(region string) {
	b.mu.RLock()
	deviceInfo := b.deviceInfo
	ownerEmail := b.config.OwnerEmail
	devicePrivKey := b.config.DevicePrivKey
	b.mu.RUnlock()

	// Require owner email for Let's Mesh Analyzer authentication
	if ownerEmail == "" {
		b.logger.Warn("Cannot renew auth token: owner_email not configured for Let's Mesh Analyzer",
			"region", region, "hint", "Set OwnerEmail in MQTTConfig to enable analyzer servers")
		return
	}

	if deviceInfo == nil || len(deviceInfo.PublicKey) < 32 {
		b.logger.Warn("Cannot renew auth token: no device info")
		return
	}

	// Require device private key for JWT signing
	if devicePrivKey == "" {
		b.logger.Warn("Cannot renew auth token: device_private_key not configured",
			"region", region, "hint", "Set DevicePrivKey in MQTTConfig for Let's Mesh Analyzer")
		return
	}

	// Determine server name based on region
	serverName := "mqtt-us-v1.letsmesh.net"
	if region == "eu" {
		serverName = "mqtt-eu-v1.letsmesh.net"
	}

	// Generate JWT token with Ed25519 signing
	expiresIn := 86400 // 24 hours
	jwtToken, err := b.createSignedJWT(deviceInfo.PublicKey, devicePrivKey, ownerEmail, serverName, expiresIn)
	if err != nil {
		b.logger.Error("Failed to create signed JWT", "region", region, "error", err)
		return
	}

	expiresAt := time.Now().Add(time.Duration(expiresIn) * time.Second)

	if br := b.brokers[region]; br != nil {
		br.authToken = jwtToken
		br.tokenExpires = expiresAt
	}

	b.logger.Info("Auth token generated", "region", region, "expires", expiresAt,
		"username", b.analyzerUser())
}

// createSignedJWT creates a JWT token signed with Ed25519 for Let's Mesh Analyzer auth.
//
// MeshCore devices use the orlp/ed25519 C library, which stores private keys in a
// PRE-EXPANDED format: [clamped_scalar(32) | nonce_prefix(32)] — NOT as a seed.
//
// Go's crypto/ed25519.PrivateKey expects [seed(32) | pubkey(32)] and internally
// runs SHA-512 over the seed to derive (scalar, prefix).  Passing an orlp key
// directly to Go would cause Go to SHA-512-hash the already-clamped scalar,
// yielding a completely wrong signature.
//
// Supported key formats (from MeshCore identity.export()):
//   - 32 bytes  (64 hex):  Standard Ed25519 seed — use Go stdlib
//   - 64 bytes (128 hex):  Orlp expanded key [scalar|prefix] — use orlp-compatible signing
//   - 96 bytes (192 hex):  Full MeshCore export [prv(64)|pub(32)] — extract first 64 bytes
func (b *MQTTBridge) createSignedJWT(publicKey []byte, privateKeyHex, email, serverName string, expiresIn int) (string, error) {
	privKeyBytes, err := hex.DecodeString(privateKeyHex)
	if err != nil {
		return "", fmt.Errorf("invalid private key hex: %w", err)
	}

	now := time.Now()
	iat := now.Unix()
	exp := now.Add(time.Duration(expiresIn) * time.Second).Unix()

	// Build JWT header + payload (identical for all key formats).
	headerJSON, _ := json.Marshal(map[string]string{"alg": "Ed25519", "typ": "JWT"})
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

	hexPubKey := strings.ToUpper(hex.EncodeToString(publicKey))
	claims := map[string]interface{}{
		"aud":       serverName,
		"email":     email,
		"exp":       exp,
		"iat":       iat,
		"publicKey": hexPubKey,
	}
	if ownerPubKey := b.config.OwnerPublicKey; ownerPubKey != "" {
		claims["owner"] = strings.ToUpper(ownerPubKey)
	}
	payloadJSON, _ := json.Marshal(claims)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	signingInput := []byte(headerB64 + "." + payloadB64)

	var sig []byte

	switch len(privKeyBytes) {
	case 32:
		// Standard Ed25519 seed: Go stdlib handles SHA-512 expansion correctly.
		stdKey := ed25519.NewKeyFromSeed(privKeyBytes)
		sig = ed25519.Sign(stdKey, signingInput)

	case 64:
		// Orlp expanded key: [clamped_scalar(32) | nonce_prefix(32)].
		// Must NOT pass to ed25519.PrivateKey — Go would re-expand it incorrectly.
		sig, err = b.signWithOrlpKey(signingInput, privKeyBytes, publicKey)
		if err != nil {
			return "", fmt.Errorf("orlp signing failed: %w", err)
		}

	case 96:
		// Full MeshCore export: [prv_key(64) | pub_key(32)].
		// Verify the embedded public key matches what we expect.
		if !bytes.Equal(privKeyBytes[64:], publicKey) {
			b.logger.Warn("Public key in 96-byte export does not match device public key",
				"exported", hex.EncodeToString(privKeyBytes[64:]),
				"device", hex.EncodeToString(publicKey))
		}
		sig, err = b.signWithOrlpKey(signingInput, privKeyBytes[:64], publicKey)
		if err != nil {
			return "", fmt.Errorf("orlp signing failed: %w", err)
		}

	default:
		return "", fmt.Errorf("invalid private key length: expected 32, 64, or 96 bytes, got %d", len(privKeyBytes))
	}

	// MeshCore JWT format: header.payload.<hex-encoded signature>
	jwt := string(signingInput) + "." + hex.EncodeToString(sig)

	b.logger.Info("Auth token created",
		"public_key", hexPubKey,
		"audience", serverName,
		"expires", time.Unix(exp, 0).Format(time.RFC3339))

	return jwt, nil
}

// signWithOrlpKey signs a message using Ed25519 with a pre-expanded private key in
// MeshCore's orlp/ed25519 format: [clamped_scalar(32) | nonce_prefix(32)].
//
// This replicates what orlp/ed25519's ed25519_sign() does:
//
//	r  = SHA-512(prefix ‖ message) mod l        (deterministic nonce scalar)
//	R  = r·B                                    (nonce point)
//	k  = SHA-512(R ‖ pubKey ‖ message) mod l    (challenge scalar)
//	s  = (r + k·a) mod l                        (response; a is the clamped scalar)
//	sig = R ‖ s                                 (64 bytes)
func (b *MQTTBridge) signWithOrlpKey(message, expandedPrivKey, pubKey []byte) ([]byte, error) {
	if len(expandedPrivKey) != 64 {
		return nil, fmt.Errorf("expanded private key must be 64 bytes, got %d", len(expandedPrivKey))
	}
	if len(pubKey) != 32 {
		return nil, fmt.Errorf("public key must be 32 bytes, got %d", len(pubKey))
	}

	scalarBytes := expandedPrivKey[:32] // pre-clamped scalar 'a'
	prefix := expandedPrivKey[32:64]    // nonce prefix

	h := sha512.New()

	// r = SHA-512(prefix ‖ message) mod l
	h.Write(prefix)
	h.Write(message)
	r, err := new(edwards25519.Scalar).SetUniformBytes(h.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("nonce scalar: %w", err)
	}

	// R = r·B
	R := new(edwards25519.Point).ScalarBaseMult(r)
	RBytes := R.Bytes()

	// k = SHA-512(R ‖ pubKey ‖ message) mod l
	h.Reset()
	h.Write(RBytes)
	h.Write(pubKey)
	h.Write(message)
	k, err := new(edwards25519.Scalar).SetUniformBytes(h.Sum(nil))
	if err != nil {
		return nil, fmt.Errorf("challenge scalar: %w", err)
	}

	// a = clamped scalar (SetBytesWithClamping is idempotent on already-clamped data)
	a, err := new(edwards25519.Scalar).SetBytesWithClamping(scalarBytes)
	if err != nil {
		return nil, fmt.Errorf("private key scalar: %w", err)
	}

	// s = r + k·a (mod l)
	s := new(edwards25519.Scalar).MultiplyAdd(k, a, r)

	sig := make([]byte, 64)
	copy(sig[:32], RBytes)
	copy(sig[32:], s.Bytes())
	return sig, nil
}
