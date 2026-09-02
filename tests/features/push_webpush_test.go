package features

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/AmritRai1234/spine/pkg/engine"
)

// Web Push (self-owned) acceptance criteria:
//
//	AC1: VAPID JWT is a valid ES256 JWT — signature verifies against the
//	     public key, claims carry aud/exp/sub.
//	AC2: RFC 8291 encryption roundtrip — a client holding its private key +
//	     auth secret can decrypt the framed ciphertext and recover the
//	     payload (padding delimiter intact).
//	AC3: subscription token parsing rejects malformed values.
//	AC4: endpoint origin extraction (aud) is correct.

func TestVAPIDJWTVerifies(t *testing.T) {
	// Generate a keypair the way LoadOrCreateVAPIDKeys exports it.
	pubB64, privB64, _, err := engine.LoadOrCreateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	// NOTE: LoadOrCreateVAPIDKeys reads env — with both set it returns them.
	t.Setenv("VAPID_PUBLIC_KEY", pubB64)
	t.Setenv("VAPID_PRIVATE_KEY", privB64)

	// Re-derive the public point from the private scalar to verify the pair.
	priv, err := engine.ParseVAPIDPrivateKeyForTest(privB64)
	if err != nil {
		t.Fatalf("parse private: %v", err)
	}
	gotPub := elliptic.Marshal(elliptic.P256(), priv.PublicKey.X, priv.PublicKey.Y)
	pubRaw, err := engine.B64URLDecodeForTest(pubB64)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPub, pubRaw) {
		t.Fatal("VAPID private scalar does not reproduce the exported public point")
	}

	// JWT roundtrip: sign then verify.
	jwtStr, err := engine.VapidJWTForTest("https://fcm.googleapis.com", "mailto:test@spine.local", priv)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(jwtStr, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT must have 3 segments, got %d", len(parts))
	}
	// Verify ES256 signature over the signing input.
	sig, err := engine.B64URLDecodeForTest(parts[2])
	if err != nil || len(sig) != 64 {
		t.Fatalf("bad JOSE signature length %d", len(sig))
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&priv.PublicKey, hash[:], r, s) {
		t.Fatal("ES256 signature does NOT verify")
	}
	// Claims: aud + sub present, exp in the future.
	claimsJSON, err := engine.B64URLDecodeForTest(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims map[string]interface{}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		t.Fatal(err)
	}
	if claims["aud"] != "https://fcm.googleapis.com" {
		t.Fatalf("aud wrong: %v", claims["aud"])
	}
	if claims["sub"] != "mailto:test@spine.local" {
		t.Fatalf("sub wrong: %v", claims["sub"])
	}
	if exp, ok := claims["exp"].(float64); !ok || exp < float64(engine.NowUnixForTest()) {
		t.Fatalf("exp must be in the future: %v", claims["exp"])
	}
}

func TestWebPushEncryptionRoundtrip(t *testing.T) {
	// Simulate the CLIENT: generate keypair + auth secret.
	client, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authSecret := make([]byte, 16)
	if _, err := rand.Read(authSecret); err != nil {
		t.Fatal(err)
	}
	clientPubRaw := client.PublicKey().Bytes()

	payload := []byte(`{"title":"Order shipped","body":"Track it now","data":{"order_id":"o-1"}}`)

	// SERVER side: encrypt (the real path).
	ciphertext, serverPubRaw, err := engine.PushEncryptWebPushForTest(payload, clientPubRaw, authSecret)
	if err != nil {
		t.Fatalf("encrypt failed: %v", err)
	}

	// Parse the RFC 8291 frame: salt(16) | rs(4) | idlen(1) | keyid | ct.
	if len(ciphertext) < 16+4+1+len(serverPubRaw)+16 {
		t.Fatalf("frame too short: %d", len(ciphertext))
	}
	salt := ciphertext[:16]
	rs := binary.BigEndian.Uint32(ciphertext[16:20])
	idlen := int(ciphertext[20])
	if rs != 4096 || idlen != len(serverPubRaw) {
		t.Fatalf("bad frame header rs=%d idlen=%d", rs, idlen)
	}
	keyID := ciphertext[21 : 21+idlen]
	sealed := ciphertext[21+idlen:]
	if !bytes.Equal(keyID, serverPubRaw) {
		t.Fatal("frame keyid != ephemeral server public key")
	}

	// CLIENT side: derive keys and decrypt.
	shared, err := client.ECDH(mustPub(t, serverPubRaw))
	if err != nil {
		t.Fatal(err)
	}
	info := append([]byte("WebPush: info\x00"), append(clientPubRaw, serverPubRaw...)...)
	ikm := engine.HkdfSHA256ForTest(authSecret, shared, info, 32)
	cek := engine.HkdfSHA256ForTest(salt, ikm, []byte("Content-Encoding: aes128gcm\x01"), 16)
	nonce := engine.HkdfSHA256ForTest(salt, ikm, []byte("Content-Encoding: nonce\x01"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		t.Fatalf("client decrypt failed: %v", err)
	}
	// Padding delimiter 0x02 must be the last byte.
	if plain[len(plain)-1] != 0x02 {
		t.Fatal("missing RFC 8291 padding delimiter")
	}
	plain = plain[:len(plain)-1]
	if !bytes.Equal(plain, payload) {
		t.Fatalf("roundtrip mismatch:\n got %s\nwant %s", plain, payload)
	}
}

func TestWebPushTokenParsing(t *testing.T) {
	if _, _, _, err := engine.ParseWebPushTokenForTest("https://push.example/abc|only-two"); err == nil {
		t.Fatal("2-part token must be rejected")
	}
	if _, _, _, err := engine.ParseWebPushTokenForTest("|key|auth"); err == nil {
		t.Fatal("empty endpoint must be rejected")
	}
	ep, p256dh, auth, err := engine.ParseWebPushTokenForTest("https://push.example/abc|QUJD|REVG")
	if err != nil || ep != "https://push.example/abc" || p256dh != "QUJD" || auth != "REVG" {
		t.Fatalf("parse failed: %v %q %q %q", err, ep, p256dh, auth)
	}
}

func TestWebPushAudienceExtraction(t *testing.T) {
	aud, err := engine.ParseEndpointOriginForTest("https://fcm.googleapis.com/fcm/send/xyz123")
	if err != nil || aud != "https://fcm.googleapis.com" {
		t.Fatalf("aud=%q err=%v", aud, err)
	}
	if _, err := engine.ParseEndpointOriginForTest("http://insecure.example/push"); err == nil {
		t.Fatal("http endpoint must be rejected")
	}
}

// ── helpers ────────────────────────────────────────────────────────────────

func mustPub(t *testing.T, raw []byte) *ecdh.PublicKey {
	t.Helper()
	pk, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	return pk
}
