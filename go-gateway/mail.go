package main

import (
	"encoding/base64"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// ReplySender — всё, что умеет отправить ответ на письмо.
// Телеграм-бот знает только про этот интерфейс, поэтому SMTP и Gmail API подменяются через .env
type ReplySender interface {
	SendReply(e StoredEmail) error
}

// Mailer отправляет письма через SMTP с ящика бота (тот же app password, что и для IMAP)
type Mailer struct {
	Host     string
	Port     string
	User     string
	Password string
}

// SendReply отправляет черновик ответом отправителю письма
func (m Mailer) SendReply(e StoredEmail) error {
	if m.User == "" || m.Password == "" {
		return fmt.Errorf("не заданы BOT_EMAIL / BOT_APP_PASSWORD")
	}

	to, msg, err := composeReply(m.User, e)
	if err != nil {
		return err
	}

	// PlainAuth сам откажется слать пароль по незашифрованному каналу (кроме localhost),
	// а SendMail включает STARTTLS, если сервер его поддерживает (Gmail на 587 — поддерживает)
	auth := smtp.PlainAuth("", m.User, m.Password, m.Host)

	return smtp.SendMail(net.JoinHostPort(m.Host, m.Port), auth, m.User, []string{to}, msg)
}

// composeReply проверяет черновик и собирает готовое письмо-ответ. Общее для SMTP и Gmail API.
func composeReply(from string, e StoredEmail) (to string, msg []byte, err error) {
	if strings.TrimSpace(e.Draft) == "" {
		return "", nil, fmt.Errorf("черновик пустой")
	}

	// ParseAddress заодно отсекает мусор вместо адреса
	addr, err := mail.ParseAddress(e.From)
	if err != nil {
		return "", nil, fmt.Errorf("кривой адрес получателя %q: %w", e.From, err)
	}

	subject := e.Subject
	if !strings.HasPrefix(strings.ToLower(subject), "re:") {
		subject = "Re: " + subject
	}

	return addr.Address, buildMessage(from, addr.Address, subject, e.Draft), nil
}

// buildMessage собирает письмо руками: заголовки + пустая строка + тело в base64
func buildMessage(from, to, subject, body string) []byte {
	// Перенос строки в заголовке = возможность дописать свои заголовки (header injection). Вырезаем.
	noNewlines := strings.NewReplacer("\r", " ", "\n", " ")

	var b strings.Builder
	header := func(k, v string) {
		b.WriteString(k + ": " + noNewlines.Replace(v) + "\r\n")
	}

	header("From", from)
	header("To", to)
	// Кириллицу в заголовке нельзя слать как есть — кодируем в =?utf-8?q?...?=
	header("Subject", mime.QEncoding.Encode("utf-8", noNewlines.Replace(subject)))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")

	// Тело в base64, строки по 76 символов — так требует стандарт
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")

	return []byte(b.String())
}
