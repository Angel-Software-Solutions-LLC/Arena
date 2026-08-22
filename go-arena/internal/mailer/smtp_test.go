package mailer

import (
	"bytes"
	"net/mail"
	"strings"
	"testing"
	"time"
)

func TestBuildMagicLinkMessageIsMultipartAndEscapesCustomerName(t *testing.T) {
	from, _ := mail.ParseAddress("Arena <hello@angel-serv.com>")
	to, _ := mail.ParseAddress("pilot@example.com")
	message, err := buildMagicLinkMessage(
		from,
		to,
		`Pilot <script>alert(1)</script>`,
		"https://arena.angel-serv.com/dashboard/#email_token=secret-token",
		15*time.Minute,
		time.Date(2026, 7, 11, 20, 0, 0, 0, time.UTC),
		"<message-id@angel-serv.com>",
	)
	if err != nil {
		t.Fatal(err)
	}
	text := string(message)
	for _, required := range []string{
		"From: \"Arena\" <hello@angel-serv.com>",
		"Reply-To: \"Arena\" <hello@angel-serv.com>",
		"To: <pilot@example.com>",
		"Subject: Your Arena sign-in link",
		"Date: Sat, 11 Jul 2026 20:00:00 +0000",
		"Message-ID: <message-id@angel-serv.com>",
		"Auto-Submitted: auto-generated",
		"multipart/alternative",
		"text/plain; charset=utf-8",
		"text/html; charset=utf-8",
		"expires in 15 minutes",
		"ARENA ACCOUNT ACCESS",
		"Sign in to your Arena Dashboard",
		"Copy and paste this address",
		"Never share this link",
		"https://arena.angel-serv.com/dashboard/#email_token=secret-token",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("message missing %q:\n%s", required, text)
		}
	}
	htmlPart := text[strings.Index(text, "Content-Type: text/html; charset=utf-8"):]
	if strings.Contains(htmlPart, "<script>alert(1)</script>") {
		t.Fatalf("HTML body contains unescaped display name:\n%s", htmlPart)
	}
	if !bytes.Contains([]byte(htmlPart), []byte("Pilot &lt;script&gt;alert(1)&lt;/script&gt;")) {
		t.Fatalf("escaped HTML greeting missing:\n%s", text)
	}
	if strings.Count(htmlPart, `<a href=`) != 1 {
		t.Fatalf("HTML email must contain one primary CTA link:\n%s", htmlPart)
	}
}

func TestNewSMTPMagicLinkSenderFailsClosedWithoutVerifiedTLSAndExactSender(t *testing.T) {
	base := SMTPConfig{
		Host: "100.71.171.28", Port: 465, TLSMode: "implicit", TLSServerName: "mail.angel-serv.com",
		Username: "hello@angel-serv.com", Password: "app-password", From: "Arena <hello@angel-serv.com>",
	}
	for _, test := range []struct {
		name   string
		mutate func(*SMTPConfig)
	}{
		{name: "plaintext mode", mutate: func(cfg *SMTPConfig) { cfg.TLSMode = "none" }},
		{name: "TLS name missing", mutate: func(cfg *SMTPConfig) { cfg.TLSServerName = "" }},
		{name: "password missing", mutate: func(cfg *SMTPConfig) { cfg.Password = "" }},
		{name: "mismatched envelope sender", mutate: func(cfg *SMTPConfig) { cfg.From = "Arena <other@angel-serv.com>" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.mutate(&cfg)
			if sender, err := NewSMTPMagicLinkSender(cfg); err == nil || sender != nil {
				t.Fatalf("NewSMTPMagicLinkSender() = (%+v, %v), want fail-closed error", sender, err)
			}
		})
	}
}

func TestValidMagicLinkURLAllowsHTTPSAndLoopbackDevelopmentOnly(t *testing.T) {
	for _, raw := range []string{
		"https://arena.angel-serv.com/dashboard/#email_token=value",
		"http://localhost:8725/dashboard/#email_token=value",
		"http://127.0.0.1:8725/dashboard/#email_token=value",
	} {
		if !validMagicLinkURL(raw) {
			t.Fatalf("validMagicLinkURL(%q) = false", raw)
		}
	}
	for _, raw := range []string{
		"http://arena.angel-serv.com/dashboard/#email_token=value",
		"javascript:alert(1)",
		"/dashboard/#email_token=value",
	} {
		if validMagicLinkURL(raw) {
			t.Fatalf("validMagicLinkURL(%q) = true", raw)
		}
	}
}
