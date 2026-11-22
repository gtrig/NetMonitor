package notify

import (
	"context"
	"net/http"
	"strings"

	"github.com/gtrig/netmon/internal/config"
)

const telegramLimit = 4096

type telegram struct {
	ch      config.Channel
	client  *http.Client
	baseURL string
}

// NewTelegram returns a Telegram bot notifier. baseURL "" means https://api.telegram.org.
func NewTelegram(c config.Channel, client *http.Client, baseURL string) Notifier {
	if baseURL == "" {
		baseURL = "https://api.telegram.org"
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &telegram{ch: c, client: client, baseURL: strings.TrimSuffix(baseURL, "/")}
}

func (t *telegram) Name() string { return t.ch.Name }

func (t *telegram) Send(ctx context.Context, m Message) error {
	err := postJSON(ctx, t.client, t.baseURL+"/bot"+t.ch.BotToken+"/sendMessage", map[string]any{
		"chat_id": t.ch.ChatID, "text": Format(m, telegramLimit), "disable_web_page_preview": true,
	})
	if err != nil && t.ch.BotToken != "" {
		// Never leak the bot token in logs/UI.
		return &redactedError{err: err, secret: t.ch.BotToken}
	}
	return err
}

type redactedError struct {
	err    error
	secret string
}

func (e *redactedError) Error() string { return strings.ReplaceAll(e.err.Error(), e.secret, "***") }
func (e *redactedError) Unwrap() error { return e.err }
