package notify

import (
	"context"
	"net/http"

	"github.com/gtrig/netmon/internal/config"
)

const discordLimit = 2000

type discord struct {
	ch     config.Channel
	client *http.Client
}

// NewDiscord returns a Discord webhook notifier.
func NewDiscord(c config.Channel, client *http.Client) Notifier {
	if client == nil {
		client = http.DefaultClient
	}
	return &discord{ch: c, client: client}
}

func (d *discord) Name() string { return d.ch.Name }

func (d *discord) Send(ctx context.Context, m Message) error {
	err := postJSON(ctx, d.client, d.ch.WebhookURL, map[string]any{"content": Format(m, discordLimit)})
	if err != nil {
		return &redactedError{err: err, secret: d.ch.WebhookURL}
	}
	return nil
}
