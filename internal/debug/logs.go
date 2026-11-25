package debug

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// maxLogs is how many log records are kept.
const maxLogs = 5000

// Entry is one log record.
type Entry struct {
	Seq   int64     `json:"seq"`
	At    time.Time `json:"at"`
	Level string    `json:"level"`
	Line  string    `json:"line"`

	level slog.Level
}

// Logs keeps the latest log records, including debug ones, in memory.
type Logs struct {
	mu      sync.Mutex
	seq     int64
	entries []Entry
	cur     Entry // being written
}

// Handler returns a handler that keeps every record and passes on to next what it wants.
func (l *Logs) Handler(next slog.Handler) slog.Handler {
	text := slog.NewTextHandler((*logWriter)(l), &slog.HandlerOptions{Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey) {
				return slog.Attr{} // separate fields of Entry
			}
			return a
		}})
	return &tee{logs: l, text: text, next: next}
}

// Select returns the latest records after a sequence number, oldest first.
func (l *Logs) Select(after int64, level slog.Level, contains string, limit int) []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	contains = strings.ToLower(contains)
	out := []Entry{}
	for i := len(l.entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := l.entries[i]
		if e.Seq <= after {
			break
		}
		if e.level >= level && strings.Contains(strings.ToLower(e.Line), contains) {
			out = append(out, e)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// logWriter receives one formatted record per Write, with Logs.mu held.
type logWriter Logs

func (w *logWriter) Write(b []byte) (int, error) {
	w.seq++
	e := w.cur
	e.Seq, e.Line = w.seq, strings.TrimSuffix(string(b), "\n")
	w.entries = append(w.entries, e)
	if len(w.entries) >= 2*maxLogs {
		w.entries = append([]Entry(nil), w.entries[len(w.entries)-maxLogs:]...)
	}
	return len(b), nil
}

type tee struct {
	logs       *Logs
	text, next slog.Handler
}

func (t *tee) Enabled(context.Context, slog.Level) bool { return true }

func (t *tee) Handle(ctx context.Context, r slog.Record) error {
	t.logs.mu.Lock()
	t.logs.cur = Entry{At: r.Time, Level: r.Level.String(), level: r.Level}
	err := t.text.Handle(ctx, r)
	t.logs.mu.Unlock()
	if t.next.Enabled(ctx, r.Level) {
		return t.next.Handle(ctx, r)
	}
	return err
}

func (t *tee) WithAttrs(as []slog.Attr) slog.Handler {
	return &tee{t.logs, t.text.WithAttrs(as), t.next.WithAttrs(as)}
}

func (t *tee) WithGroup(name string) slog.Handler {
	return &tee{t.logs, t.text.WithGroup(name), t.next.WithGroup(name)}
}
