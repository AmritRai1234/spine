package engine

import (
	"encoding/base64"
	"fmt"
	"log"
	"net/smtp"
	"net/url"
	"os"
	"strings"
	"sync/atomic"

	"github.com/AmritRai1234/spine/pkg/manifest"
)

// Email actions — lightweight marketing & transactional mail over plain SMTP.
//
// Transport is configured entirely through environment variables, so a manifest
// stays portable across dev (no mail) and prod (real relay):
//
//	SMTP_HOST   SMTP server hostname          (unset ⇒ email is disabled)
//	SMTP_PORT   port                          (default 587, STARTTLS)
//	SMTP_USER   username                      (optional)
//	SMTP_PASS   password                      (optional)
//	SMTP_FROM   default From address          (optional)
//
// With SMTP_HOST unset every email action is a silent no-op — routes never
// fail in development, exactly like notify.webhook without ALERT_WEBHOOK_URL.

const emailDisabledLogEvery = 50

var emailDisabledCount atomic.Int64

// resolveEmailFrom returns the sender address for a step: explicit config
// wins, then $SMTP_FROM, else "".
func resolveEmailFrom(step *manifest.RouteStep, eventName string, payload map[string]interface{}) string {
	from := ResolveVariables(step.Config["from"], eventName, payload)
	if from == "" {
		from = os.Getenv("SMTP_FROM")
	}
	return strings.TrimSpace(from)
}

// buildEmailMessage renders RFC 5322 headers + body. html selects the MIME
// type; unsubURL (optional) emits a List-Unsubscribe header with {{email}}
// substituted per recipient. The blank line before body terminates headers.
func buildEmailMessage(from, to, subject, body string, html bool, unsubURL string) []byte {
	var b strings.Builder
	contentType := "text/plain"
	if html {
		contentType = "text/html"
	}
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + mimeEncodeHeader(subject) + "\r\n")
	if unsubURL != "" {
		oneClick := strings.ReplaceAll(unsubURL, "{{email}}", url.QueryEscape(to))
		b.WriteString("List-Unsubscribe: <" + oneClick + ">\r\n")
	}
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: " + contentType + "; charset=\"utf-8\"\r\n")
	b.WriteString("\r\n")
	// Manifest strings are single-line (the .spine parser has no block
	// scalars), so literal "\n" sequences stand in for newlines.
	body = strings.ReplaceAll(body, "\\n", "\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	return []byte(b.String())
}

// mimeEncodeHeader guards against header injection: newlines would smuggle
// extra headers, so they are stripped; non-ASCII subjects get RFC 2047
// B-encoding so UTF-8 survives the wire.
func mimeEncodeHeader(s string) string {
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r", ""), "\n", "")
	for _, r := range s {
		if r > 126 {
			return mimeBEncode(s)
		}
	}
	return s
}

// mimeBEncode produces =?utf-8?B?...?= encoded words (RFC 2047), chunked at
// 45 raw bytes so each line stays well under the 998-byte limit and never
// splits a UTF-8 rune mid-sequence.
func mimeBEncode(s string) string {
	const chunkMax = 45
	var words []string
	for start := 0; start < len(s); {
		end := start + chunkMax
		if end > len(s) {
			end = len(s)
		} else {
			for end > start && s[end]&0xC0 == 0x80 { // don't split runes
				end--
			}
		}
		word := "=?utf-8?B?" + base64.StdEncoding.EncodeToString([]byte(s[start:end])) + "?="
		words = append(words, word)
		start = end
	}
	return strings.Join(words, " ")
}

// sendMail delivers one message over SMTP. Port 587 uses STARTTLS via
// smtp.SendMail; credentials are only offered after TLS or on localhost,
// matching smtp.PlainAuth's safety rules.
func sendMail(host, port, user, pass, from string, to []string, msg []byte) error {
	addr := host + ":" + port
	var auth smtp.Auth
	if user != "" {
		auth = smtp.PlainAuth("", user, pass, host)
	}
	return smtp.SendMail(addr, auth, from, to, msg)
}

// emailSend implements the `email.send` action: one transactional email.
// Required config: to, subject, body. Optional: from, html ("true"),
// unsubscribe_url. Missing SMTP_HOST disables mail silently.
func (b *Bus) emailSend(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	host := os.Getenv("SMTP_HOST")
	if host == "" {
		// Atomic: concurrent Emit goroutines may skip mail simultaneously.
		if n := emailDisabledCount.Add(1); (n-1)%emailDisabledLogEvery == 0 {
			log.Printf("[email] SMTP_HOST not set — email.send skipped (notifications disabled)")
		}
		return nil
	}
	to := ResolveVariables(step.Config["to"], eventName, payload)
	if to == "" {
		return fmt.Errorf("email.send requires 'to' config")
	}
	subject := ResolveVariables(step.Config["subject"], eventName, payload)
	body := ResolveVariables(step.Config["body"], eventName, payload)
	from := resolveEmailFrom(step, eventName, payload)
	if from == "" {
		return fmt.Errorf("email.send requires 'from' config or SMTP_FROM env")
	}
	unsubURL := ResolveVariables(step.Config["unsubscribe_url"], eventName, payload)
	msg := buildEmailMessage(from, to, subject, body, step.Config["html"] == "true", unsubURL)

	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	if err := sendMail(host, port, os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASS"), from, []string{to}, msg); err != nil {
		return fmt.Errorf("email.send to %s failed: %w", to, err)
	}
	payload["email_to"] = to
	return nil
}

// emailBroadcast implements the `email.broadcast` action: a marketing send to
// every row of a recipients table (step.Table). Config:
//
//	email_column     column holding addresses (default "email")
//	where            optional SQL filter, e.g. "unsubscribed = 0"
//	subject / body   templates with {{email}} substituted per recipient
//	from, html, unsubscribe_url   as in email.send
//
// Per-recipient failures are logged and skipped (one bad address must never
// duplicate-send the rest on route retry); the resolved count lands in the
// payload as `email_sent`.
func (b *Bus) emailBroadcast(step *manifest.RouteStep, eventName string, payload map[string]interface{}) error {
	host := os.Getenv("SMTP_HOST")
	if host == "" {
		log.Printf("[email] SMTP_HOST not set — email.broadcast skipped (notifications disabled)")
		return nil
	}
	tableName := sanitizeIdent(step.Table)
	if tableName == "" {
		return fmt.Errorf("email.broadcast requires 'table'")
	}
	col := sanitizeIdent(step.Config["email_column"])
	if col == "" {
		col = "email"
	}

	query := fmt.Sprintf(`SELECT "%s" FROM "%s"`, col, tableName)
	if w := strings.TrimSpace(step.Where); w != "" {
		query += " WHERE " + w
	}

	rows, err := b.db.Query(query)
	if err != nil {
		return fmt.Errorf("email.broadcast query failed: %w", err)
	}
	defer rows.Close()

	subjectTpl := ResolveVariables(step.Config["subject"], eventName, payload)
	bodyTpl := ResolveVariables(step.Config["body"], eventName, payload)
	unsubTpl := ResolveVariables(step.Config["unsubscribe_url"], eventName, payload)
	html := step.Config["html"] == "true"
	from := resolveEmailFrom(step, eventName, payload)
	if from == "" {
		return fmt.Errorf("email.broadcast requires 'from' config or SMTP_FROM env")
	}

	port := os.Getenv("SMTP_PORT")
	if port == "" {
		port = "587"
	}
	user, pass := os.Getenv("SMTP_USER"), os.Getenv("SMTP_PASS")

	sent := 0
	for rows.Next() {
		var addr string
		if err := rows.Scan(&addr); err != nil {
			continue
		}
		addr = strings.TrimSpace(addr)
		if addr == "" || !strings.Contains(addr, "@") {
			continue
		}
		msg := buildEmailMessage(
			from, addr,
			strings.ReplaceAll(subjectTpl, "{{email}}", addr),
			strings.ReplaceAll(bodyTpl, "{{email}}", addr),
			html,
			unsubTpl,
		)
		if err := sendMail(host, port, user, pass, from, []string{addr}, msg); err != nil {
			log.Printf("[email] broadcast to %s failed: %v", addr, err)
			continue
		}
		sent++
	}
	payload["email_sent"] = sent
	log.Printf("[email] broadcast '%s' delivered to %d recipient(s)", subjectTpl, sent)
	return nil
}
