package features

// Email action tests: email.send + email.broadcast over a minimal in-process
// SMTP server. Covers silent-disable (no SMTP_HOST), delivery, variable
// resolution, {{email}} templating, unsubscribed filtering, List-Unsubscribe
// headers, and header-injection stripping.

import (
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/AmritRai1234/spine/tests/testhelpers"
)


// ---- Silent disable: no SMTP_HOST ⇒ no-op, route never fails ----

func TestEmailDisabledWithoutHost(t *testing.T) {
	os.Unsetenv("SMTP_HOST")
	engine, _ := testhelpers.SetupEmailEngine(t, "")

	engine.Bus.Emit("SEND_MAIL", map[string]interface{}{
		"email": "x@example.com",
		"name":  "X",
	})
	time.Sleep(150 * time.Millisecond)
	// Reaching here means the route completed without erroring out.
}

// ---- email.send delivers with resolved variables ----

func TestEmailSendDelivers(t *testing.T) {
	server := &testhelpers.FakeSMTPServer{}
	hostPort := server.Start(t)
	host, port, _ := net.SplitHostPort(hostPort)
	t.Setenv("SMTP_HOST", host)
	t.Setenv("SMTP_PORT", port)
	t.Setenv("SMTP_FROM", "store@example.com")

	engine, _ := testhelpers.SetupEmailEngine(t, "")
	engine.Bus.Emit("SEND_MAIL", map[string]interface{}{
		"email": "jane@example.com",
		"name":  "Jane",
	})

	msgs := server.WaitCount(t, 1)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 delivered mail, got %d", len(msgs))
	}
	data := msgs[0].Data
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
	server := &testhelpers.FakeSMTPServer{}
	hostPort := server.Start(t)
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
	engine, _ := testhelpers.SetupEmailEngine(t, injectManifest)
	engine.Bus.Emit("INJECT_SUBJECT", map[string]interface{}{})

	msgs := server.WaitCount(t, 1)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 delivered mail, got %d", len(msgs))
	}
	data := msgs[0].Data
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
	msgs = server.WaitCount(t, 2)
	subjectLine := ""
	for _, line := range strings.Split(msgs[1].Data, "\r\n") {
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
	server := &testhelpers.FakeSMTPServer{}
	hostPort := server.Start(t)
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
	engine, _ := testhelpers.SetupEmailEngine(t, unsubManifest)
	engine.Bus.Emit("SEND_UNSUB", map[string]interface{}{})

	msgs := server.WaitCount(t, 1)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 delivered mail, got %d", len(msgs))
	}
	want := "List-Unsubscribe: <https://shop.example.com/unsub?e=a+b%40example.com>"
	if !strings.Contains(msgs[0].Data, want) {
		t.Errorf("missing List-Unsubscribe %q\nfull:\n%s", want, msgs[0].Data)
	}
}

// ---- email.broadcast: {{email}} templating + unsubscribed filter ----

func TestEmailBroadcastFiltersAndTemplates(t *testing.T) {
	server := &testhelpers.FakeSMTPServer{}
	hostPort := server.Start(t)
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

	engine, dbPath := testhelpers.SetupEmailEngine(t, broadcastManifest)

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
	msgs := server.WaitCount(t, 2)
	if len(msgs) != 2 {
		t.Fatalf("broadcast should deliver exactly 2 mails (unsubscribed + empty filtered), got %d", len(msgs))
	}
	seen := map[string]bool{}
	for _, m := range msgs {
		lf := strings.ReplaceAll(m.Data, "\r\n", "\n")
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
		if addr == "" || !strings.Contains(m.Data, "Hello "+addr) {
			t.Errorf("{{email}} not substituted for %q:\n%s", addr, m.Data)
		}
		seen[addr] = true
	}
	if seen["a@example.com"] != true || seen["b@example.com"] != true || seen["gone@example.com"] {
		t.Errorf("wrong recipients delivered: %v", seen)
	}

	_ = dbPath
}
