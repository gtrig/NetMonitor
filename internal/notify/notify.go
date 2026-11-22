// Package notify sends change notifications to Telegram, Discord and email.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Message is a notification: a title and one line per change.
type Message struct {
	Title string
	Lines []string
}

// Notifier delivers messages to one channel.
type Notifier interface {
	Name() string
	Send(ctx context.Context, m Message) error
}

// Format renders m as text no longer than limit bytes, replacing lines that don't fit
// with "…and N more".
func Format(m Message, limit int) string {
	var b strings.Builder
	b.WriteString(m.Title)
	for i, l := range m.Lines {
		line := "\n- " + l
		remaining := len(m.Lines) - i - 1
		tail := ""
		if remaining > 0 {
			tail = fmt.Sprintf("\n…and %d more", remaining)
		}
		if b.Len()+len(line)+len(tail) > limit {
			more := fmt.Sprintf("\n…and %d more", len(m.Lines)-i)
			if b.Len()+len(more) > limit {
				return truncate(b.String(), limit)
			}
			b.WriteString(more)
			return b.String()
		}
		b.WriteString(line)
	}
	return truncate(b.String(), limit)
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}

func postJSON(ctx context.Context, client *http.Client, url string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
