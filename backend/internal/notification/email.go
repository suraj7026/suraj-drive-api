package notification

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/url"
	"strings"
	"time"

	"surajdrive/backend/internal/repository"
)

type Email struct {
	To      string
	Subject string
	Text    string
	HTML    string
}

type Sender interface {
	Send(context.Context, Email) error
}

type SMTPConfig struct {
	Address        string
	Username       string
	Password       string
	FromAddress    string
	FromName       string
	AllowPlaintext bool
}

type SMTPSender struct{ config SMTPConfig }

func NewSMTPSender(config SMTPConfig) *SMTPSender { return &SMTPSender{config: config} }

func (s *SMTPSender) Send(ctx context.Context, message Email) error {
	to, err := mail.ParseAddress(message.To)
	if err != nil {
		return fmt.Errorf("parse notification recipient: %w", err)
	}
	from, err := mail.ParseAddress((&mail.Address{Name: s.config.FromName, Address: s.config.FromAddress}).String())
	if err != nil {
		return fmt.Errorf("parse notification sender: %w", err)
	}
	host, _, err := net.SplitHostPort(s.config.Address)
	if err != nil {
		return fmt.Errorf("parse SMTP address: %w", err)
	}
	connection, err := (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext(ctx, "tcp", s.config.Address)
	if err != nil {
		return fmt.Errorf("connect to SMTP: %w", err)
	}
	defer connection.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(connection, host)
	if err != nil {
		return fmt.Errorf("start SMTP client: %w", err)
	}
	defer client.Close()
	if !s.config.AllowPlaintext {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return fmt.Errorf("SMTP server does not support STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("secure SMTP connection: %w", err)
		}
	}
	if s.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.config.Username, s.config.Password, host)); err != nil {
			return fmt.Errorf("authenticate SMTP: %w", err)
		}
	}
	if err := client.Mail(from.Address); err != nil {
		return fmt.Errorf("set SMTP sender: %w", err)
	}
	if err := client.Rcpt(to.Address); err != nil {
		return fmt.Errorf("set SMTP recipient: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("open SMTP message: %w", err)
	}
	if _, err := io.WriteString(writer, encodeMessage(from, to, message)); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish SMTP message: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("quit SMTP session: %w", err)
	}
	return nil
}

func encodeMessage(from, to *mail.Address, message Email) string {
	const boundary = "suraj-drive-notification-boundary"
	subject := strings.ReplaceAll(strings.ReplaceAll(message.Subject, "\r", ""), "\n", "")
	return "From: " + from.String() + "\r\n" +
		"To: " + to.String() + "\r\n" +
		"Subject: " + mime.QEncoding.Encode("utf-8", subject) + "\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=\"" + boundary + "\"\r\n\r\n" +
		"--" + boundary + "\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + message.Text + "\r\n" +
		"--" + boundary + "\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" + message.HTML + "\r\n" +
		"--" + boundary + "--\r\n"
}

func Render(job repository.NotificationJob, publicURL string) (Email, error) {
	var payload map[string]any
	if err := json.Unmarshal(job.Payload, &payload); err != nil {
		return Email{}, fmt.Errorf("decode notification payload: %w", err)
	}
	baseURL := strings.TrimRight(publicURL, "/")
	link := baseURL + "/shared"
	subject, heading, detail := "Drive notification", "Something changed in your Drive", "Open Drive to view the latest update."
	switch job.Type {
	case "share.invited":
		subject, heading, detail, link = "You were invited to a Drive item", "An item was shared with you", "Sign in using this email address to accept the invitation.", baseURL+"/login"
	case "item.shared":
		subject, heading, detail = "A Drive item was shared with you", "You have new access", "Open Shared with me to view the item."
	case "comment.mentioned":
		subject, heading, detail = "You were mentioned in a Drive comment", "Someone mentioned you", "Open Shared with me to read the comment."
	case "share.accepted":
		subject, heading, detail = "Your Drive invitation was accepted", "Invitation accepted", "The recipient now has access to the shared item."
	}
	if _, err := url.ParseRequestURI(link); err != nil {
		return Email{}, fmt.Errorf("build notification URL: %w", err)
	}
	text := heading + "\n\n" + detail + "\n\n" + link
	htmlBody := `<div style="font-family:Arial,sans-serif;max-width:560px;margin:auto;padding:32px;color:#202124"><h1 style="font-size:24px">` + html.EscapeString(heading) + `</h1><p style="line-height:1.6">` + html.EscapeString(detail) + `</p><p style="margin-top:28px"><a href="` + html.EscapeString(link) + `" style="background:#0b57d0;color:white;padding:12px 20px;border-radius:999px;text-decoration:none">Open Drive</a></p><p style="margin-top:32px;color:#5f6368;font-size:12px">This message was sent because of activity in Suraj Drive.</p></div>`
	return Email{To: job.Recipient, Subject: subject, Text: text, HTML: htmlBody}, nil
}
