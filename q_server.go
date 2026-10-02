package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// A connected hi server can pass hi q's requests to a model with the team's
// key (hi server ai). hi q asks the server whether it does at most once an
// hour, and after a failure skips it for five minutes, falling back to the
// user's own key or Claude Code.

const (
	qServerCheckTimeout = 1500 * time.Millisecond
	qServerCacheFor     = time.Hour
	qServerDownFor      = 5 * time.Minute
)

// qNotice receives one-line notes such as a fallback. Tests replace it.
var qNotice io.Writer = os.Stderr

// qServerCache is ~/.local/state/hi/q/server.json.
type qServerCache struct {
	URL       string    `json:"url"`
	Checked   time.Time `json:"checked"`
	Enabled   bool      `json:"enabled"`
	Model     string    `json:"model,omitempty"`
	DownUntil time.Time `json:"down_until,omitempty"`
	Problem   string    `json:"problem,omitempty"`
}

func loadQServerCache() qServerCache {
	var cache qServerCache
	if path, err := qStatePath("server.json"); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			json.Unmarshal(data, &cache)
		}
	}
	return cache
}

func saveQServerCache(cache qServerCache) {
	path, err := qStatePath("server.json")
	if err != nil {
		return
	}
	os.MkdirAll(filepath.Dir(path), 0o700)
	data, _ := json.Marshal(cache)
	os.WriteFile(path, data, 0o600)
}

// qMarkServerDown remembers a failure, so later questions skip the server.
func qMarkServerDown(url, problem string) {
	cache := loadQServerCache()
	if cache.URL != url {
		cache = qServerCache{URL: url}
	}
	cache.DownUntil = time.Now().Add(qServerDownFor)
	cache.Problem = problem
	saveQServerCache(cache)
}

// qServerName is the server's host without port or domain, for messages.
func qServerName(url string) string {
	host := qHost(url)
	host, _, _ = strings.Cut(host, ":")
	if net.ParseIP(host) != nil {
		return host
	}
	if name, _, found := strings.Cut(host, "."); found && name != "" {
		return name
	}
	return host
}

// qServerAI says whether the connected server serves a model. fresh skips
// the cache, for the setup menu. down is set when the server can't be
// reached now.
func qServerAI(connection *serverConnection, key ed25519.PrivateKey, fresh bool) (info apiAI, down string) {
	cache := loadQServerCache()
	if !fresh && cache.URL == connection.URL {
		if time.Now().Before(cache.DownUntil) {
			return apiAI{}, firstNonEmpty(cache.Problem, "can't be reached")
		}
		if time.Since(cache.Checked) < qServerCacheFor && cache.DownUntil.IsZero() {
			return apiAI{Enabled: cache.Enabled, Model: cache.Model}, ""
		}
	}
	client := &http.Client{Timeout: qServerCheckTimeout}
	if fresh {
		client.Timeout = 10 * time.Second
	}
	err := doJSON(client, http.MethodGet, connection.URL+"/v1/ai", nil, &info, key)
	if err != nil {
		var reply *serverReplyError
		problem := "can't be reached"
		if errors.As(err, &reply) {
			problem = "answered: " + reply.message
		}
		qMarkServerDown(connection.URL, problem)
		return apiAI{}, problem
	}
	saveQServerCache(qServerCache{URL: connection.URL, Checked: time.Now(), Enabled: info.Enabled, Model: info.Model})
	return info, ""
}

// qServerProvider is the connected server as an OpenAI-compatible
// endpoint, with requests signed by the device key.
func qServerProvider(connection *serverConnection, key ed25519.PrivateKey, model string) qOpenAI {
	return qOpenAI{
		baseURL: connection.URL + "/v1/ai",
		model:   model,
		sign:    key,
		via:     qServerName(connection.URL),
	}
}

// qConnectedServerChoice picks the connected server when it serves a model.
// When the server can't be reached it returns the user's own provider with
// a note. forced is set when the user chose the server, so it is used even
// when nothing else is there to fall back to.
func qConnectedServerChoice(model string, forced bool) (qChoice, bool) {
	connection, err := loadServerConnection()
	if err != nil || connection == nil {
		return qChoice{}, false
	}
	key, err := loadDeviceKey(false)
	if err != nil {
		return qChoice{}, false
	}
	name := qServerName(connection.URL)
	info, down := qServerAI(connection, key, false)
	if down != "" {
		personal, err := qPersonalProvider(model)
		if err == nil && personal.provider != nil {
			personal.note = fmt.Sprintf("%s %s; using %s", name, down, personal.source)
			return personal, true
		}
		if !forced {
			return qChoice{}, false
		}
		info.Enabled = true // try it anyway and report its error
	}
	if !info.Enabled {
		return qChoice{}, false
	}
	provider := qServerProvider(connection, key, firstNonEmpty(model, info.Model))
	return qChoice{
		provider: &qServerFallback{primary: provider, url: connection.URL, name: name, model: model},
		source:   "connected hi server " + name,
	}, true
}

// qServerFallback uses the server, and switches to the user's own provider
// for the rest of the session when the server can't be used.
type qServerFallback struct {
	primary  qProvider
	url      string
	name     string
	model    string
	switched qProvider
}

func (f *qServerFallback) label() string {
	if f.switched != nil {
		return f.switched.label()
	}
	return f.primary.label()
}

func (f *qServerFallback) step(ctx context.Context, system string, turns []qTurn, lookup bool) (qStep, error) {
	if f.switched != nil {
		return f.switched.step(ctx, system, turns, lookup)
	}
	step, err := f.primary.step(ctx, system, turns, lookup)
	if err == nil || !qServerUnusable(ctx, err) {
		return step, err
	}
	qMarkServerDown(f.url, "can't be used")
	personal, personalErr := qPersonalProvider(f.model)
	if personalErr != nil || personal.provider == nil {
		return step, fmt.Errorf("%w\nNo personal model to fall back to; choose one with hi q --setup", err)
	}
	fmt.Fprintln(qNotice, lipgloss.NewStyle().Foreground(colorDim).Render(fmt.Sprintf("  %s can't be used (%s); using %s", f.name, qFirstLine(err.Error(), "error"), personal.source)))
	f.switched = personal.provider
	return f.switched.step(ctx, system, turns, lookup)
}

// qServerUnusable is true when the hi server itself failed or refused,
// not when it passed on an error from the upstream, such as an unknown
// model, which a personal key would get too.
func qServerUnusable(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var httpErr *qHTTPError
	if errors.As(err, &httpErr) {
		// The server's own refusals, a device it no longer knows (401, 403),
		// and failures (5xx) fall back; the upstream's 4xx answers don't.
		return httpErr.fromServer || httpErr.code == http.StatusUnauthorized || httpErr.code == http.StatusForbidden || httpErr.code >= 500
	}
	return true
}
