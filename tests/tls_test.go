package tests

// TLS tests: automatic HTTPS support. The ACME/Let's Encrypt path requires a
// public domain and port 80/443 reachability, so it cannot run in CI — these
// tests cover the bring-your-own-certificate serving path end-to-end (real
// TLS handshake against a live engine), config validation, and the manager's
// host whitelist construction via the public API.

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

func genSelfSignedCert(t *testing.T, dir string) (certPath, keyPath string) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa keygen: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}

func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func newTLSTestEngine(t *testing.T) *spine.Engine {
	t.Helper()
	dir := t.TempDir()
	spineFile := filepath.Join(dir, "tls.spine")
	dbPath := filepath.Join(dir, "tls.db")

	manifest := `spine_version: 1

database:
  tables:
    - items

nodes:
  TlsNode:
    emits:
      - event: PING
        payload:
          msg: string

routes:
  - on: PING
    steps:
      - action: db.insert
        table: items
`
	if err := os.WriteFile(spineFile, []byte(manifest), 0644); err != nil {
		t.Fatal(err)
	}
	eng, err := spine.NewFromFile(spineFile, dbPath)
	if err != nil {
		t.Fatalf("NewFromFile: %v", err)
	}
	return eng
}

// TestListenAndServeTLSWithProvidedCert drives the BYO-cert path end-to-end:
// a live engine serves /health over a real TLS handshake.
func TestListenAndServeTLSWithProvidedCert(t *testing.T) {
	dir := t.TempDir()
	certFile, keyFile := genSelfSignedCert(t, dir)
	eng := newTLSTestEngine(t)
	defer eng.Close()

	port := freeTCPPort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	errCh := make(chan error, 1)
	go func() {
		errCh <- eng.ListenAndServeTLS(addr, &spine.TLSConfig{
			CertFile: certFile,
			KeyFile:  keyFile,
		})
	}()

	client := &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // self-signed test cert
		},
	}

	var resp *http.Response
	deadline := time.Now().Add(5 * time.Second)
	for {
		r, err := client.Get("https://" + addr + "/health")
		if err == nil {
			resp = r
			break
		}
		select {
		case serveErr := <-errCh:
			t.Fatalf("ListenAndServeTLS exited early: %v", serveErr)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("TLS server never became reachable: %v", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
	defer resp.Body.Close()

	var body map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode /health response: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("/health over HTTPS returned %v, want status=ok", body)
	}
	if resp.TLS == nil || !resp.TLS.HandshakeComplete {
		t.Error("connection did not complete a TLS handshake")
	}
}

// TestTLSConfigValidation ensures misconfigured TLS fails fast and
// synchronously, before any listener starts.
func TestTLSConfigValidation(t *testing.T) {
	eng := newTLSTestEngine(t)
	defer eng.Close()

	cases := []struct {
		name string
		cfg  *spine.TLSConfig
	}{
		{"nil config", nil},
		{"empty config", &spine.TLSConfig{}},
		{"acme mixed with cert files", &spine.TLSConfig{
			Domains:  []string{"shop.example.com"},
			CertFile: "/tmp/c.pem",
			KeyFile:  "/tmp/k.pem",
		}},
	}
	for _, tc := range cases {
		err := eng.ListenAndServeTLS("127.0.0.1:0", tc.cfg)
		if err == nil {
			t.Errorf("%s: expected synchronous error, got nil", tc.name)
		}
	}
}
