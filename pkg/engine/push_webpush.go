package engine

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"
)

// Web Push (RFC 8291 + RFC 8292) — self-owned, no third-party SDK account.
//
// A browser (or any push-service client) registers a subscription. The
// subscription is three values from PushManager.subscribe():
//
//	endpoint  https://push-service.example/…   (the push service URL)
//	p256dh    client's ECDH P-256 public key   (base64url, no padding)
//	auth      authentication secret            (base64url, no padding)
//
// Spine stores subscriptions as one string: "endpoint|p256dh|auth" — the
// token shape router already sends https:// tokens to this adapter.
//
// Encryption per message (RFC 8291 "aes128gcm" content coding):
//   - server generates an ephemeral ECDH P-256 keypair
//   - shared secret = ECDH(ephemeral_priv, client_p256dh_pub)
//   - PRK = HKDF(auth=ikm)… see pushWebPushKeys below
//   - AES-128-GCM seal; ciphertext framed per RFC 8291 §3.2
//
// VAPID (RFC 8292): the Authorization header carries an ES256 JWT signing
// the endpoint's origin, with the server's P-256 public key embedded. The
// key pair is generated once and stored — VAPID_PUBLIC_KEY /
// VAPID_PRIVATE_KEY env (base64url raw coordinates / scalar).

const webPushSaltLen = 16

// b64url is the unpadded base64url encoding used throughout Web Push.
func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func b64urlDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// LoadOrCreateVAPIDKeys returns the server's VAPID keypair from env, or
// generates + logs a fresh pair when unset (self-owned: there is no external
// account to provision — you ARE the sender identity).
func LoadOrCreateVAPIDKeys() (publicB64, privateB64 string, generated bool, err error) {
	pub := os.Getenv("VAPID_PUBLIC_KEY")
	priv := os.Getenv("VAPID_PRIVATE_KEY")
	if pub != "" && priv != "" {
		return pub, priv, false, nil
	}
	if priv == "" && pub == "" {
		// Generate P-256 keypair, export as raw coordinates.
		key, kerr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if kerr != nil {
			return "", "", false, kerr
		}
		pubRaw := elliptic.Marshal(elliptic.P256(), key.PublicKey.X, key.PublicKey.Y)
		privRaw := key.D.FillBytes(make([]byte, 32))
		generated = true
		return b64url(pubRaw), b64url(privRaw), true, nil
	}
	return "", "", false, fmt.Errorf("VAPID: set both VAPID_PUBLIC_KEY and VAPID_PRIVATE_KEY (or neither to auto-generate)")
}

// parseVAPIDPrivateKey decodes the base64url scalar into an ecdsa key.
func parseVAPIDPrivateKey(privateB64 string) (*ecdsa.PrivateKey, error) {
	raw, err := b64urlDecode(privateB64)
	if err != nil || len(raw) != 32 {
		return nil, fmt.Errorf("VAPID_PRIVATE_KEY must be 32 raw bytes base64url (got %d bytes)", len(raw))
	}
	d := new(big.Int).SetBytes(raw)
	curve := elliptic.P256()
	priv := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: curve, X: new(big.Int), Y: new(big.Int)},
		D:         d,
	}
	// Recompute the public point.
	x, y := curve.ScalarBaseMult(d.Bytes())
	priv.PublicKey.X, priv.PublicKey.Y = x, y
	return priv, nil
}

// vapidJWT builds the RFC 8292 Authorization header value for one audience
// (the push service origin) with the required claims: aud, exp, sub.
func vapidJWT(audience, subject string, priv *ecdsa.PrivateKey) (string, error) {
	now := time.Now()
	header := map[string]string{"typ": "JWT", "alg": "ES256"}
	claims := map[string]interface{}{
		"aud": audience,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": subject, // mailto: or https: contact — required by push services
	}
	hb, err := json.Marshal(header)
	if err != nil {
		return "", err
	}
	cb, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signingInput := b64url(hb) + "." + b64url(cb)
	hash := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, priv, hash[:])
	if err != nil {
		return "", err
	}
	// JOSE signature: r||s, each 32 bytes.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + b64url(sig), nil
}

// vapidHeaders assembles Authorization + Crypto-Key for a push request.
func vapidHeaders(endpoint, subject string, priv *ecdsa.PrivateKey, pubRaw []byte) (http.Header, error) {
	u, err := parseEndpointOrigin(endpoint)
	if err != nil {
		return nil, err
	}
	token, err := vapidJWT(u, subject, priv)
	if err != nil {
		return nil, err
	}
	h := http.Header{}
	h.Set("Authorization", "vapid t="+token+", k="+b64url(pubRaw))
	h.Set("TTL", "86400") // keep if the device is offline for a day
	return h, nil
}

func parseEndpointOrigin(endpoint string) (string, error) {
	if !strings.HasPrefix(endpoint, "https://") {
		return "", fmt.Errorf("web push endpoint must be https: %q", endpoint[:min(40, len(endpoint))])
	}
	// aud = scheme + host (+ port if non-default)
	rest := endpoint[len("https://"):]
	host := rest
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		host = rest[:i]
	}
	return "https://" + host, nil
}

// pushWebPushKeys derives the AES-128-GCM key + nonce per RFC 8291 §4.2
// (aes128gcm profile): HKDF-SHA256 with the RFC-specified info labels.
func pushWebPushKeys(authSecret, clientPubRaw, serverPubRaw, salt []byte) (key, nonce []byte, err error) {
	// IKM: HKDF(salt=auth_secret, ikm=ecdh_secret, info="WebPush: info\x00"||client_pub||server_pub, len=32)
	shared, err := ecdhSecret(clientPubRaw)
	if err != nil {
		return nil, nil, err
	}
	info := append([]byte("WebPush: info\x00"), append(clientPubRaw, serverPubRaw...)...)
	ikm := hkdfSHA256(authSecret, shared, info, 32)

	// CEK: HKDF(salt, ikm, "Content-Encoding: aes128gcm\x01", 16)
	cek := hkdfSHA256(salt, ikm, []byte("Content-Encoding: aes128gcm\x01"), 16)
	// NONCE: HKDF(salt, ikm, "Content-Encoding: nonce\x01", 12)
	nonceOut := hkdfSHA256(salt, ikm, []byte("Content-Encoding: nonce\x01"), 12)
	return cek, nonceOut, nil
}

// ecdhSecret computes ECDH with the CLIENT's P-256 public key using a fresh
// ephemeral server key (returned as raw point for the header).
func ecdhSecret(clientPubRaw []byte) (shared []byte, err error) {
	// crypto/ecdh's P256 type handles the raw-point unmarshal + ECDH.
	curve := ecdh.P256()
	clientPub, err := curve.NewPublicKey(clientPubRaw)
	if err != nil {
		return nil, fmt.Errorf("web push: bad client p256dh key: %w", err)
	}
	eph, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	shared, err = eph.ECDH(clientPub)
	if err != nil {
		return nil, fmt.Errorf("web push: ECDH failed: %w", err)
	}
	return shared, nil
}

// hkdfSHA256 is the extract-then-expand HKDF (RFC 5869).
func hkdfSHA256(salt, ikm, info []byte, length int) []byte {
	// Extract
	mac := hmac.New(sha256.New, salt)
	mac.Write(ikm)
	prk := mac.Sum(nil)
	// Expand
	var out, t []byte
	for i := byte(1); len(out) < length; i++ {
		mac := hmac.New(sha256.New, prk)
		mac.Write(t)
		mac.Write(info)
		mac.Write([]byte{i})
		t = mac.Sum(nil)
		out = append(out, t...)
	}
	return out[:length]
}

// pushEncryptWebPush seals a plaintext payload for one subscription.
// Returns the aes128gcm-framed ciphertext + the ephemeral server public key.
func pushEncryptWebPush(payload []byte, clientPubRaw, authSecret []byte) (ciphertext, serverPubRaw []byte, err error) {
	salt := make([]byte, webPushSaltLen)
	if _, err = rand.Read(salt); err != nil {
		return nil, nil, err
	}

	curve := ecdh.P256()
	eph, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	clientPub, err := curve.NewPublicKey(clientPubRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("bad p256dh: %w", err)
	}
	shared, err := eph.ECDH(clientPub)
	if err != nil {
		return nil, nil, err
	}
	serverPubRaw = eph.PublicKey().Bytes()

	info := append([]byte("WebPush: info\x00"), append(clientPubRaw, serverPubRaw...)...)
	ikm := hkdfSHA256(authSecret, shared, info, 32)
	cek := hkdfSHA256(salt, ikm, []byte("Content-Encoding: aes128gcm\x01"), 16)
	nonce := hkdfSHA256(salt, ikm, []byte("Content-Encoding: nonce\x01"), 12)

	// AES-128-GCM with RFC 8291 padding delimiter (0x02 appended).
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	plaintext := append(append([]byte{}, payload...), 0x02)
	sealed := gcm.Seal(nil, nonce, plaintext, nil)

	// Frame per RFC 8291 §3.2: salt(16) | rs(4) | idlen(1) | keyid | ciphertext
	frame := new(bytes.Buffer)
	frame.Write(salt)
	binary.Write(frame, binary.BigEndian, uint32(4096)) // rs
	frame.WriteByte(byte(len(serverPubRaw)))
	frame.Write(serverPubRaw)
	frame.Write(sealed)
	return frame.Bytes(), serverPubRaw, nil
}

// pushSendWebPush delivers one encrypted message to a subscription.
// token format: "endpoint|p256dh|auth" (all base64url, no padding).
func pushSendWebPush(token, title, body string, data map[string]interface{}) error {
	endpoint, p256dh, auth, err := parseWebPushToken(token)
	if err != nil {
		return err
	}

	// Payload: JSON with title/body (+data). Empty payload = silent notification.
	msg := map[string]interface{}{"title": title, "body": body}
	if len(data) > 0 {
		msg["data"] = data
	}
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	clientPub, err := b64urlDecode(p256dh)
	if err != nil {
		return fmt.Errorf("web push: bad p256dh encoding: %w", err)
	}
	authSecret, err := b64urlDecode(auth)
	if err != nil {
		return fmt.Errorf("web push: bad auth encoding: %w", err)
	}

	ciphertext, _, err := pushEncryptWebPush(payload, clientPub, authSecret)
	if err != nil {
		return err
	}

	// VAPID: self-owned keys — env or auto-generate.
	pubB64, privB64, generated, err := LoadOrCreateVAPIDKeys()
	if err != nil {
		return err
	}
	if generated {
		log.Printf("[push] VAPID keys auto-generated (store to reuse): PUBLIC=%s PRIVATE=%s", pubB64, privB64)
	}
	priv, err := parseVAPIDPrivateKey(privB64)
	if err != nil {
		return err
	}
	pubRaw, err := b64urlDecode(pubB64)
	if err != nil {
		return fmt.Errorf("bad VAPID_PUBLIC_KEY: %w", err)
	}

	subject := orDefault(os.Getenv("VAPID_SUBJECT"), "mailto:admin@spine.local")
	headers, err := vapidHeaders(endpoint, subject, priv, pubRaw)
	if err != nil {
		return err
	}
	headers.Set("Content-Encoding", "aes128gcm")
	headers.Set("Content-Type", "application/octet-stream")

	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(ciphertext))
	if err != nil {
		return err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Set(k, v)
		}
	}
	resp, err := sharedHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("push endpoint unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	var parsed struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(respBody, &parsed)
	msgText := parsed.Error
	if msgText == "" {
		msgText = parsed.Reason
	}
	if msgText == "" {
		msgText = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	return fmt.Errorf("push provider returned %d: %s", resp.StatusCode, msgText)
}

// parseWebPushToken splits the stored subscription string.
func parseWebPushToken(token string) (endpoint, p256dh, auth string, err error) {
	parts := strings.SplitN(token, "|", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("web push subscription must be endpoint|p256dh|auth (got %d parts)", len(parts))
	}
	return parts[0], parts[1], parts[2], nil
}
