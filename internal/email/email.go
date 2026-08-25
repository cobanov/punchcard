package email

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/smtp"
	"strings"

	"github.com/cobanov/punchcard/internal/config"
)

// Message is a plain-text email.
type Message struct {
	To      string
	Subject string
	Text    string
}

// Sender delivers email. Implementations must not log secrets in production.
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// New selects the adapter from config. In Phase 1, sends are synchronous; they
// are moved onto River jobs in Phase 3.
func New(cfg *config.Config, log *slog.Logger) Sender {
	switch cfg.EmailProvider {
	case "smtp":
		return &smtpSender{
			host: cfg.SMTPHost, port: cfg.SMTPPort,
			user: cfg.SMTPUsername, pass: cfg.SMTPPassword, from: cfg.EmailFrom,
		}
	default:
		// dev: logs the message. It logs the body (which can contain
		// verification/reset links) ONLY outside production, so no tokens land
		// in production logs.
		return &devSender{from: cfg.EmailFrom, log: log, logBody: !cfg.IsProduction()}
	}
}

type devSender struct {
	from    string
	log     *slog.Logger
	logBody bool
}

func (d *devSender) Send(ctx context.Context, m Message) error {
	attrs := []any{"from", d.from, "to", m.To, "subject", m.Subject}
	if d.logBody {
		attrs = append(attrs, "body", m.Text)
	}
	d.log.InfoContext(ctx, "email (dev adapter)", attrs...)
	return nil
}

type smtpSender struct {
	host, port, user, pass, from string
}

func (s *smtpSender) Send(_ context.Context, m Message) error {
	addr := net.JoinHostPort(s.host, s.port)
	var auth smtp.Auth
	if s.user != "" {
		auth = smtp.PlainAuth("", s.user, s.pass, s.host)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", s.from)
	fmt.Fprintf(&b, "To: %s\r\n", m.To)
	fmt.Fprintf(&b, "Subject: %s\r\n", m.Subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n\r\n")
	b.WriteString(m.Text)
	if err := smtp.SendMail(addr, auth, s.from, []string{m.To}, []byte(b.String())); err != nil {
		return fmt.Errorf("send mail: %w", err)
	}
	return nil
}
