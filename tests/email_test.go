package tests

// Email action tests: email.send + email.broadcast over a minimal in-process
// SMTP server. Covers silent-disable (no SMTP_HOST), delivery, variable
// resolution, {{email}} templating, unsubscribed filtering, List-Unsubscribe
// headers, and header-injection stripping.

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

// fakeSMTPServer accepts one-shot SMTP conversations and records messages.
type fakeSMTPServer struct {
	mu       sync.Mutex
	messages []capturedMail
}

type capturedMail struct {
	from string
	to   string
	data string // raw DATA payload, CRLF line endings
}

// start launches the server on an ephemeral loopback port and returns its
// address (host:port) ready for SMTP_HOST/SMTP_PORT.
func (s *fakeSMTPServer) start(t *testing.T) string {
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

func (s *fakeSMTPServer) serve(conn net.Conn) {
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
			s.mu.Lock()
			s.messages = append(s.messages, capturedMail{data: body.String()})
			s.mu.Unlock()
		case cmd == "QUIT":
			writeLine("221 bye")
			return
		default:
			writeLine("250 ok")
		}
	}
}

func (s *fakeSMTPServer) waitCount(t *testing.T, n int) []capturedMail {
	t.Helper()
	// Generous deadline: the full test suite keeps batched-writer and
	// worker-pool goroutines busy, so route steps can lag well behind the
	// emit that scheduled them.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		got := len(s.messages)
		s.mu.Unlock()
		if got >= n {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]capturedMail, len(s.messages))
	copy(out, s.messages)
	return out
}

const emailManifest = `spine_version: 2
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

func setupEmailEngine(t *testing.T, extraManifest string) (*spine.Engine, string) {
	t.Helper()
	tempDir := t.TempDir()
	dbPath := filepath.Join(tempDir, "email_test.db")
	spinesFile := filepath.Join(tempDir, "email_test.spine")
	os.WriteFile(spinesFile, []byte(emailManifest+extraManifest), 0644)
	engine, err := spine.NewFromFile(spinesFile, dbPath)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	t.Cleanup(func() { engine.Close() })
	return engine, dbPath
}

// ---- Silent disable: no SMTP_HOST ⇒ no-op, route never fails ----

func TestEmailDisabledWithoutHost(t *testing.T) {
	os.Unsetenv("SMTP_HOST")
	engine, _ := setupEmailEngine(t, "")

	engine.Bus.Emit("SEND_MAIL", map[string]interface{}{
		"email": "x@example.com",
		"name":  "X",
	})
	time.Sleep(150 * time.Millisecond)
	// Reaching here means the route completed without erroring out.
}

// ---- email.send delivers with resolved variables ----

func TestEmailSendDelivers(t *testing.T) {
	server := &fakeSMTPServer{}
	hostPort := server.start(t)
	host, port, _ := net.SplitHostPort(hostPort)
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)
	t.Setenv("SMTP_FROM", "store@example.com")

	engine, _ := setupEmailEngine(t, "")
	engine.Bus.Emit("SEND_MAIL", map[string]interface{}{
		"email": "jane@example.com",
		"name":  "Jane",
	})

	msgs := server.waitCount(t, 1)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 delivered mail, got %d", len(msgs))
	}
	data := msgs[0].data
	lfData := strings.ReplaceAll(data, "\r\n", "\n")

	for _, want := range []string{
		"From: store@example.com",
		"To: jane@example.com",
		"Subject: Welcome Jane",
		"MIME-Version: 1.0",
		`Content-Type: text/plain`,
		"Hi Jane,",
		"thanks for joining.",
	} {
		if !strings.Contains(lfData, want) {
			t.Errorf("message missing %q\nfull:\n%s", want, lfData)
		}
	}
}

// ---- Header injection is stripped; UTF-8 subjects are B-encoded ----

func TestEmailHeaderSanitisation(t *testing.T) {
	server := &fakeSMTPServer{}
	hostPort := server.start(t)
	host, port, _ := net.SplitHostPort(hostPort)
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)
	t.Setenv("SMTP_FROM", "store@example.com")
	// A real newline must arrive via variable interpolation ($env) — manifest
	// strings are single-line, so injected headers come from dynamic data.
	t.Setenv("EVIL_SUBJECT", "hello\nBcc: hacker@example.com")

	injectManifest := `
routes:
  - on: INJECT_SUBJECT
    steps:
      - action: email.send
        to: victim@example.com
        subject: $env.EVIL_SUBJECT
        body: hi
`
	engine, _ := setupEmailEngine(t, injectManifest)
	engine.Bus.Emit("INJECT_SUBJECT", map[string]interface{}{})

	msgs := server.waitCount(t, 1)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 delivered mail, got %d", len(msgs))
	}
	data := msgs[0].data
	// Injection must not create a new header line — stripped content may
	// remain as inert subject text ("helloBcc: ..."), which is harmless.
	for _, line := range strings.Split(data, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "bcc:") ||
			strings.HasPrefix(strings.ToLower(line), "cc:") {
			t.Errorf("header injection survived:\n%s", data)
		}
	}

	// Non-ASCII subject must be RFC 2047 encoded, never raw UTF-8 bytes.
	engine.Bus.Emit("SEND_MAIL", map[string]interface{}{
		"email": "jane@example.com",
		"name":  "Jürgen",
	})
	msgs = server.waitCount(t, 2)
	subjectLine := ""
	for _, line := range strings.Split(msgs[1].data, "\r\n") {
		if strings.HasPrefix(line, "Subject:") {
			subjectLine = line
		}
	}
	if !strings.HasPrefix(subjectLine, "Subject: =?utf-8?B?") {
		t.Errorf("non-ASCII subject not B-encoded: %q", subjectLine)
	}
}

// ---- List-Unsubscribe header with per-recipient URL encoding ----

func TestEmailUnsubscribeHeader(t *testing.T) {
	server := &fakeSMTPServer{}
	hostPort := server.start(t)
	host, port, _ := net.SplitHostPort(hostPort)
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)
	t.Setenv("SMTP_FROM", "store@example.com")

	unsubManifest := `
routes:
  - on: SEND_UNSUB
    steps:
      - action: email.send
        to: a b@example.com
        subject: hi
        body: yo
        unsubscribe_url: "https://shop.example.com/unsub?e={{email}}"
`
	engine, _ := setupEmailEngine(t, unsubManifest)
	engine.Bus.Emit("SEND_UNSUB", map[string]interface{}{})

	msgs := server.waitCount(t, 1)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 delivered mail, got %d", len(msgs))
	}
	want := "List-Unsubscribe: <https://shop.example.com/unsub?e=a+b%40example.com>"
	if !strings.Contains(msgs[0].data, want) {
		t.Errorf("missing List-Unsubscribe %q\nfull:\n%s", want, msgs[0].data)
	}
}

// ---- email.broadcast: {{email}} templating + unsubscribed filter ----

func TestEmailBroadcastFiltersAndTemplates(t *testing.T) {
	server := &fakeSMTPServer{}
	hostPort := server.start(t)
	host, port, _ := net.SplitHostPort(hostPort)
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)
	t.Setenv("SMTP_FROM", "store@example.com")

	broadcastManifest := `
routes:
  - on: SEND_CAMPAIGN
    steps:
      - action: email.broadcast
        table: subscribers
        where: "unsubscribed = 0"
        subject: "Sale for {{email}}"
        body: "Hello {{email}}, 20% off today"
`

	engine, dbPath := setupEmailEngine(t, broadcastManifest)

	// Seed subscribers directly through the subscribe route.
	for _, e := range []string{"a@example.com", "b@example.com"} {
		engine.Bus.Emit("SUBSCRIBE_EMAIL", map[string]interface{}{"email": e})
	}
	engine.Bus.Emit("SUBSCRIBE_EMAIL", map[string]interface{}{"email": "gone@example.com"})
	time.Sleep(200 * time.Millisecond)

	db := engine.Bus.DB()
	if _, err := db.Exec(`UPDATE subscribers SET unsubscribed = 1 WHERE email = 'gone@example.com'`); err != nil {
		t.Fatalf("mark unsubscribed failed: %v", err)
	}

	engine.Bus.Emit("SEND_CAMPAIGN", map[string]interface{}{})
	msgs := server.waitCount(t, 2)
	if len(msgs) != 2 {
		t.Fatalf("broadcast should deliver exactly 2 mails (unsubscribed + empty filtered), got %d", len(msgs))
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		lf := strings.ReplaceAll(m.data, "\r\n", "\n")
		if !strings.Contains(lf, "Subject: Sale for ") {
			t.Errorf("subject template not applied:\n%s", lf)
		}
		var addr string
		for _, line := range strings.Split(lf, "\n") {
			if strings.HasPrefix(line, "To: ") {
				addr = strings.TrimPrefix(line, "To: ")
			}
		}
		if !strings.Contains(lf, "20% off") {
			t.Errorf("body percent literal lost:\n%s", lf)
		}
		if addr == "" || !strings.Contains(m.data, "Hello "+addr) {
			t.Errorf("{{email}} not substituted for %q:\n%s", addr, m.data)
		}
		seen[addr] = true
	}
	if seen["a@example.com"] != true || seen["b@example.com"] != true || seen["gone@example.com"] {
		t.Errorf("wrong recipients delivered: %v", seen)
	}

	_ = dbPath
}
