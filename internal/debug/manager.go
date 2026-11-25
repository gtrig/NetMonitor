package debug

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gtrig/netmon/internal/config"
)

// ErrTokenGiven is returned by NewToken when the token is set by the environment.
var ErrTokenGiven = errors.New("the token is set by NETMON_DEBUG_TOKEN")

// Options say how the server runs, besides its config.
type Options struct {
	Forced    bool   // on whatever the config says
	Token     string // the token; empty: the one kept in TokenPath
	TokenPath string
}

// Manager starts, moves and stops the server as the config asks.
type Manager struct {
	opts Options
	deps Deps

	mu    sync.Mutex // serialises Apply, NewToken and Close
	state sync.Mutex // guards the rest
	srv   *http.Server
	asked string // address srv was asked to listen on
	addr  string // address it listens on
	err   error
	token string
}

// Status is the state of the server.
type Status struct {
	Running    bool   `json:"running"`
	Addr       string `json:"addr,omitempty"`
	Error      string `json:"error,omitempty"`
	Forced     bool   `json:"forced"`
	TokenGiven bool   `json:"token_given"` // set by the environment
}

// NewManager returns a manager whose server is not running. d.Token is not used.
func NewManager(o Options, d Deps) *Manager {
	m := &Manager{opts: o}
	d.Token = m.currentToken
	m.deps = d
	return m
}

// SetUI sets the handler of the "get" tool. Call it before Apply.
func (m *Manager) SetUI(h http.Handler) { m.deps.UI = h }

func (m *Manager) currentToken() string {
	m.state.Lock()
	defer m.state.Unlock()
	return m.token
}

// Status returns the state of the server.
func (m *Manager) Status() Status {
	m.state.Lock()
	defer m.state.Unlock()
	st := Status{Running: m.srv != nil, Addr: m.addr, Forced: m.opts.Forced, TokenGiven: m.opts.Token != ""}
	if m.err != nil {
		st.Error = m.err.Error()
	}
	return st
}

// Apply makes the server match c. A failure to start shows in Status; it does not stop netmon.
func (m *Manager) Apply(c config.Debug) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state.Lock()
	running, asked := m.srv != nil, m.asked
	m.state.Unlock()
	on := c.Enabled || m.opts.Forced
	if running && on && asked == c.Addr() {
		return // the allowed networks are read per request
	}
	m.stop(nil)
	if !on {
		m.deps.Logs.Stop()
		return
	}
	if err := m.start(c); err != nil {
		slog.Error("debug server did not start", "err", err)
		m.stop(err)
	}
}

func (m *Manager) start(c config.Debug) error {
	token, where, err := LoadToken(m.opts.Token, m.opts.TokenPath)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", c.Addr())
	if err != nil {
		return err
	}
	m.deps.Logs.Start()
	srv := &http.Server{Handler: New(m.deps), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second}
	m.state.Lock()
	m.srv, m.asked, m.addr, m.err, m.token = srv, c.Addr(), ln.Addr().String(), nil, token
	m.state.Unlock()
	slog.Warn("debug mode: MCP server listening", "url", "http://"+ln.Addr().String()+Path, "allow", c.Networks(), "token", where)
	go func() {
		err := srv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			return
		}
		slog.Error("debug server stopped", "err", err)
		m.state.Lock()
		defer m.state.Unlock()
		if m.srv == srv {
			m.srv, m.addr, m.asked, m.err = nil, "", "", err
		}
	}()
	return nil
}

func (m *Manager) stop(err error) {
	m.state.Lock()
	srv := m.srv
	m.srv, m.addr, m.asked, m.err, m.token = nil, "", "", err, ""
	m.state.Unlock()
	if srv != nil {
		srv.Close()
		slog.Info("debug server stopped")
	}
}

// Close stops the server.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stop(nil)
}

// NewToken replaces the token and returns the new one. The old one stops working at once.
func (m *Manager) NewToken() (string, error) {
	if m.opts.Token != "" {
		return "", ErrTokenGiven
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	token := rand.Text()
	if err := writeToken(m.opts.TokenPath, token); err != nil {
		return "", err
	}
	m.state.Lock()
	m.token = token
	m.state.Unlock()
	slog.Warn("debug server: new token")
	return token, nil
}

// LoadToken returns the access token and where it comes from: given, or kept in the file at path,
// which is created if needed.
func LoadToken(given, path string) (token, where string, err error) {
	where = "NETMON_DEBUG_TOKEN"
	if given == "" {
		where = path
		b, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			b = []byte(rand.Text())
			err = writeToken(path, string(b))
		}
		if err != nil {
			return "", "", fmt.Errorf("debug token: %w", err)
		}
		given = strings.TrimSpace(string(b))
	}
	if len(given) < minToken {
		return "", "", fmt.Errorf("debug token in %s: must have at least %d characters", where, minToken)
	}
	return given, where, nil
}

// writeToken replaces the file at path, readable only by its owner.
func writeToken(path, token string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".debug-token-*") // mode 0600
	if err != nil {
		return err
	}
	defer os.Remove(f.Name()) // gone after the rename
	_, err = f.WriteString(token + "\n")
	if err := errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
