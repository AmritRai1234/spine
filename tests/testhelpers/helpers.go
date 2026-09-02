package testhelpers

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	spine "github.com/AmritRai1234/spine"
)

// WaitUntil polls fn until it returns true or timeout occurs.
func WaitUntil(t *testing.T, label string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !fn() {
		t.Fatalf("timeout waiting for condition %q", label)
	}
}

// WaitForTableRows polls until the table row count equals expected.
func WaitForTableRows(t *testing.T, eng *spine.Engine, table string, expected int) {
	t.Helper()
	WaitUntil(t, "table row count: "+table, func() bool {
		var count int
		err := eng.Bus.DB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count)
		return err == nil && count == expected
	})
}

// CapturedMail represents an email received by FakeSMTPServer.
type CapturedMail struct {
	From string
	To   string
	Data string // raw DATA payload, CRLF line endings
}

// FakeSMTPServer accepts one-shot SMTP conversations and records messages.
type FakeSMTPServer struct {
	Mu       sync.Mutex
	Messages []CapturedMail
}

// Start launches the server on an ephemeral loopback port and returns its host:port address.
func (s *FakeSMTPServer) Start(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fake smtp listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(conn)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln.Addr().String()
}

func (s *FakeSMTPServer) serve(conn net.Conn) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)

	writeLine := func(line string) {
		w.WriteString(line + "\r\n")
		w.Flush()
	}

	writeLine("220 spine-test ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		cmd := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			writeLine("250-spine-test")
			writeLine("250 SIZE 10485760")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			writeLine("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			writeLine("250 ok")
		case cmd == "DATA":
			writeLine("354 go ahead")
			var body strings.Builder
			for {
				dl, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dl == ".\r\n" || dl == ".\n" {
					break
				}
				body.WriteString(dl)
			}
			writeLine("250 accepted")
			s.Mu.Lock()
			s.Messages = append(s.Messages, CapturedMail{Data: body.String()})
			s.Mu.Unlock()
		case cmd == "QUIT":
			writeLine("221 bye")
			return
		default:
			writeLine("250 ok")
		}
	}
}

// WaitCount waits until at least n messages are captured.
//
// The deadline is generous by design: this helper asserts mail DELIVERY
// semantics, not SMTP latency. Under a loaded -race CI runner the engine's
// email goroutines (with their retry backoff) can lag several seconds behind
// the emitting test; a tight deadline turns that lag into a false failure of
// the whole suite. 30s is still far below the go test default timeout, so a
// genuinely broken mail path still fails fast.
func (s *FakeSMTPServer) WaitCount(t *testing.T, n int) []CapturedMail {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s.Mu.Lock()
		got := len(s.Messages)
		s.Mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.Mu.Lock()
	defer s.Mu.Unlock()
	out := make([]CapturedMail, len(s.Messages))
	copy(out, s.Messages)
	return out
}

const EmailManifest = `spine_version: 2
database:
  tables:
    - subscribers

nodes:
  Mailer:
    emits:
      - event: SEND_MAIL
        payload:
          email: string
          name: string
      - event: SUBSCRIBE_EMAIL
        payload:
          email: string
      - event: INJECT_SUBJECT
      - event: SEND_UNSUB
      - event: SEND_CAMPAIGN

routes:
  - on: SEND_MAIL
    steps:
      - action: email.send
        to: $event.payload.email
        subject: "Welcome $event.payload.name"
        body: "Hi $event.payload.name,\n\nthanks for joining."
  - on: SUBSCRIBE_EMAIL
    steps:
      - action: set
        id: $uuid
        created_at: $now
        unsubscribed: 0
      - action: db.upsert
        table: subscribers
        key: email
`

// SetupEmailEngine creates a test engine wired for email testing.
func SetupEmailEngine(t *testing.T, extraManifest string) (*spine.Engine, string) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "email_test.db")
	spinesFile := filepath.Join(tempDir, "email_test.spine")
	os.WriteFile(spinesFile, []byte(EmailManifest+extraManifest), 0644)
	engine, err := spine.NewFromFile(spinesFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine, dbPath
}
