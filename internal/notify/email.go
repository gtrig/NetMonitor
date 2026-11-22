package notify

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	"github.com/gtrig/netmon/internal/config"
)

// SMTPSendFunc delivers a raw message.
type SMTPSendFunc func(addr, from string, to []string, msg []byte, ch config.Channel) error

type email struct {
	ch   config.Channel
	send SMTPSendFunc
}

// NewEmail returns an email notifier; send nil means a real SMTP connection.
func NewEmail(c config.Channel, send SMTPSendFunc) Notifier {
	if send == nil {
		send = smtpSend
	}
	return &email{ch: c, send: send}
}

func (e *email) Name() string { return e.ch.Name }

func (e *email) Send(ctx context.Context, m Message) error {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", e.ch.From)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(e.ch.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", strings.NewReplacer("\r", "", "\n", " ").Replace(m.Title))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n")
	for _, l := range m.Lines {
		b.WriteString("- " + l + "\r\n")
	}
	addr := net.JoinHostPort(e.ch.SMTPHost, strconv.Itoa(e.ch.SMTPPort))
	done := make(chan error, 1)
	go func() { done <- e.send(addr, e.ch.From, e.ch.To, []byte(b.String()), e.ch) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// smtpSend connects with implicit TLS on port 465, otherwise plain with optional STARTTLS.
func smtpSend(addr, from string, to []string, msg []byte, ch config.Channel) error {
	tlsCfg := &tls.Config{ServerName: ch.SMTPHost}
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	if ch.SMTPPort == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	c, err := smtp.NewClient(conn, ch.SMTPHost)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	if ch.StartTLS && ch.SMTPPort != 465 {
		if err := c.StartTLS(tlsCfg); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if ch.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", ch.Username, ch.Password, ch.SMTPHost)); err != nil {
			return fmt.Errorf("auth: %w", err)
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, r := range to {
		if err := c.Rcpt(r); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}
