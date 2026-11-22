package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gtrig/netmon/internal/config"
)

func TestFormatTruncates(t *testing.T) {
	var lines []string
	for i := 0; i < 100; i++ {
		lines = append(lines, fmt.Sprintf("New device: host-%02d (10.0.0.%d)", i, i))
	}
	out := Format(Message{Title: "netmon: 100 changes on lan", Lines: lines}, 200)
	if len(out) > 200 {
		t.Fatalf("len = %d", len(out))
	}
	if !strings.Contains(out, "…and ") || !strings.HasSuffix(out, " more") {
		t.Fatalf("out = %q", out)
	}
}

func TestFormatFits(t *testing.T) {
	out := Format(Message{Title: "T", Lines: []string{"a", "b"}}, 100)
	if out != "T\n- a\n- b" {
		t.Fatalf("out = %q", out)
	}
}

func TestTelegramPayload(t *testing.T) {
	var path string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		json.NewDecoder(r.Body).Decode(&body)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	n := NewTelegram(config.Channel{Name: "tg", BotToken: "123:abc", ChatID: "42"}, srv.Client(), srv.URL)
	if err := n.Send(context.Background(), Message{Title: "hi", Lines: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if path != "/bot123:abc/sendMessage" || body["chat_id"] != "42" || body["text"] != "hi\n- x" {
		t.Fatalf("path=%q body=%v", path, body)
	}
}

func TestTelegramErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"ok":false,"description":"Unauthorized"}`))
	}))
	defer srv.Close()
	err := NewTelegram(config.Channel{BotToken: "t", ChatID: "1"}, srv.Client(), srv.URL).Send(context.Background(), Message{Title: "x"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v", err)
	}
}

func TestDiscordPayload(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	n := NewDiscord(config.Channel{WebhookURL: srv.URL + "/api/webhooks/1/x"}, srv.Client())
	if err := n.Send(context.Background(), Message{Title: "hi", Lines: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if body["content"] != "hi\n- x" {
		t.Fatalf("body = %v", body)
	}
}

func TestEmailMessage(t *testing.T) {
	var gotTo []string
	var gotBody string
	send := func(addr string, from string, to []string, msg []byte, ch config.Channel) error {
		gotTo, gotBody = to, string(msg)
		return nil
	}
	n := NewEmail(config.Channel{SMTPHost: "smtp.x", SMTPPort: 587, From: "a@x", To: []string{"b@x"}}, send)
	if err := n.Send(context.Background(), Message{Title: "Changes", Lines: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if len(gotTo) != 1 || gotTo[0] != "b@x" || !strings.Contains(gotBody, "Subject: Changes\r\n") || !strings.Contains(gotBody, "- x") {
		t.Fatalf("to=%v body=%q", gotTo, gotBody)
	}
}
