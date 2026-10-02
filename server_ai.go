package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// hi server ai passes hi q's model requests through to an OpenAI-compatible
// upstream, OpenRouter by default, with the team's key. Devices sign their
// requests as for everything else; any model may be asked for, and the
// server records who used what and what it cost, never the messages.

const (
	serverAIKeyName      = "ai" // in keys.json, next to the compute keys
	serverAIDefaultURL   = "https://openrouter.ai/api/v1"
	serverAIDefaultModel = "anthropic/claude-haiku-4.5"
	serverAIMaxBody      = 4 << 20
	serverAITimeout      = 90 * time.Second
	// serverAIErrorHeader marks errors that come from the hi server itself,
	// rather than the upstream, so hi q knows to fall back.
	serverAIErrorHeader = "Hi-Server-Error"
)

// serverAISettings is ai.json. The key is kept in keys.json.
type serverAISettings struct {
	URL   string `json:"url"`
	Model string `json:"model"`
	Off   bool   `json:"off,omitempty"`
	// NoKey is set for an endpoint that takes no key, such as a model
	// served on the team's own machine.
	NoKey bool `json:"no_key,omitempty"`
}

// serverAIUsage is one line of ai_usage.jsonl: who, which model, and the
// cost. Never the messages or the answer.
type serverAIUsage struct {
	Time             time.Time `json:"time"`
	User             string    `json:"user"`
	Owner            string    `json:"owner,omitempty"`
	Device           string    `json:"device"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	CompletionTokens int       `json:"completion_tokens,omitempty"`
	Cost             float64   `json:"cost,omitempty"`
	Status           int       `json:"status"`
}

// apiAI is GET /v1/ai.
type apiAI struct {
	Enabled bool   `json:"enabled"`
	Model   string `json:"model,omitempty"`
	Host    string `json:"host,omitempty"`
}

var serverAIModels struct {
	sync.Mutex
	url     string
	fetched time.Time
	data    []byte
}

func (s *hiServer) aiSettingsPath() string { return filepath.Join(s.dir, "ai.json") }

func readServerAISettings(dir string) serverAISettings {
	settings := serverAISettings{URL: serverAIDefaultURL, Model: serverAIDefaultModel}
	if data, err := os.ReadFile(filepath.Join(dir, "ai.json")); err == nil {
		json.Unmarshal(data, &settings)
	}
	if settings.URL == "" {
		settings.URL = serverAIDefaultURL
	}
	if settings.Model == "" {
		settings.Model = serverAIDefaultModel
	}
	return settings
}

func writeServerAISettings(dir string, settings serverAISettings) error {
	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "ai.json")
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// aiConfig returns the settings and key, and whether the server serves a
// model.
func (s *hiServer) aiConfig() (serverAISettings, string, bool) {
	settings := readServerAISettings(s.dir)
	s.mu.Lock()
	key := s.keys[serverAIKeyName]
	s.mu.Unlock()
	return settings, key, (key != "" || settings.NoKey) && !settings.Off
}

// setAuthorization adds the team's key, unless the endpoint takes none.
func setAIAuthorization(request *http.Request, key string) {
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
}

func writeAIError(w http.ResponseWriter, status int, message string) {
	w.Header().Set(serverAIErrorHeader, "1")
	writeJSON(w, status, map[string]any{"error": map[string]string{"message": message}})
}

func (s *hiServer) aiRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/ai", s.device(s.handleAIStatus))
	mux.HandleFunc("GET /v1/ai/models", s.device(s.handleAIModels))
	mux.HandleFunc("POST /v1/ai/chat/completions", s.deviceLimited(serverAIMaxBody, s.handleAIChat))
}

func (s *hiServer) handleAIStatus(w http.ResponseWriter, _ *http.Request, device serverDevice, _ []byte) {
	settings, _, on := s.aiConfig()
	if !on || s.isViewer(device.User) {
		writeJSON(w, http.StatusOK, apiAI{})
		return
	}
	writeJSON(w, http.StatusOK, apiAI{Enabled: true, Model: settings.Model, Host: qHost(settings.URL)})
}

// handleAIModels passes the upstream's model list on, cached for an hour.
func (s *hiServer) handleAIModels(w http.ResponseWriter, r *http.Request, device serverDevice, _ []byte) {
	settings, key, on := s.aiConfig()
	if !on || s.isViewer(device.User) {
		writeAIError(w, http.StatusServiceUnavailable, "this hi server doesn't serve a model")
		return
	}
	serverAIModels.Lock()
	defer serverAIModels.Unlock()
	if serverAIModels.url != settings.URL || time.Since(serverAIModels.fetched) > time.Hour {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		request, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(settings.URL, "/")+"/models", nil)
		setAIAuthorization(request, key)
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			writeAIError(w, http.StatusBadGateway, "the model list could not be fetched: "+err.Error())
			return
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
		response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK {
			writeAIError(w, http.StatusBadGateway, fmt.Sprintf("the model list could not be fetched: %s", response.Status))
			return
		}
		serverAIModels.url, serverAIModels.fetched, serverAIModels.data = settings.URL, time.Now(), data
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(serverAIModels.data)
}

// handleAIChat forwards one chat completion to the upstream with the team's
// key and returns its answer and status as they are.
func (s *hiServer) handleAIChat(w http.ResponseWriter, r *http.Request, device serverDevice, body []byte) {
	settings, key, on := s.aiConfig()
	if !on {
		writeAIError(w, http.StatusServiceUnavailable, "this hi server doesn't serve a model; an admin can turn it on with `hi server ai set`")
		return
	}
	if s.isViewer(device.User) {
		writeAIError(w, http.StatusForbidden, "a dashboard viewer can't use the model")
		return
	}
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		writeAIError(w, http.StatusBadRequest, "the request is not JSON")
		return
	}
	model, _ := request["model"].(string)
	if strings.TrimSpace(model) == "" {
		model = settings.Model
		request["model"] = model
	}
	request["stream"] = false
	payload, _ := json.Marshal(request)

	ctx, cancel := context.WithTimeout(r.Context(), serverAITimeout)
	defer cancel()
	upstream, _ := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(settings.URL, "/")+"/chat/completions", bytes.NewReader(payload))
	upstream.Header.Set("Content-Type", "application/json")
	setAIAuthorization(upstream, key)
	if qHost(settings.URL) == "openrouter.ai" {
		upstream.Header.Set("HTTP-Referer", "https://hifin.sh")
		upstream.Header.Set("X-Title", "hi q via hi server")
	}
	response, err := http.DefaultClient.Do(upstream)
	if err != nil {
		s.recordAIUsage(device, model, nil, http.StatusBadGateway)
		writeAIError(w, http.StatusBadGateway, "the upstream could not be reached: "+err.Error())
		return
	}
	defer response.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(response.Body, 16<<20))
	if err != nil {
		writeAIError(w, http.StatusBadGateway, "the upstream's answer could not be read")
		return
	}
	s.recordAIUsage(device, model, answer, response.StatusCode)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(response.StatusCode)
	w.Write(answer)
}

func (s *hiServer) recordAIUsage(device serverDevice, model string, answer []byte, status int) {
	entry := serverAIUsage{Time: computeNow().UTC(), User: device.User, Device: device.Hostname, Model: model, Status: status}
	var parsed struct {
		Model string `json:"model"`
		Usage struct {
			PromptTokens     int     `json:"prompt_tokens"`
			CompletionTokens int     `json:"completion_tokens"`
			Cost             float64 `json:"cost"`
		} `json:"usage"`
	}
	if answer != nil && json.Unmarshal(answer, &parsed) == nil {
		entry.PromptTokens, entry.CompletionTokens, entry.Cost = parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, parsed.Usage.Cost
		if parsed.Model != "" {
			entry.Model = parsed.Model
		}
	}
	s.mu.Lock()
	if user, ok := s.state.Users[device.User]; ok && user.Kind == "agent" {
		entry.Owner = user.Owner
	}
	s.mu.Unlock()
	data, _ := json.Marshal(entry)
	file, err := os.OpenFile(filepath.Join(s.dir, "ai_usage.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	file.Write(append(data, '\n'))
}

// aiUsageRow sums one user's use.
type aiUsageRow struct {
	User     string
	Owner    string
	Requests int
	Tokens   int
	Cost     float64
}

func readAIUsage(dir string, from, to time.Time) []aiUsageRow {
	file, err := os.Open(filepath.Join(dir, "ai_usage.jsonl"))
	if err != nil {
		return nil
	}
	defer file.Close()
	rows := map[string]*aiUsageRow{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		var entry serverAIUsage
		if json.Unmarshal(scanner.Bytes(), &entry) != nil || entry.Time.Before(from) || entry.Time.After(to) {
			continue
		}
		row := rows[entry.User]
		if row == nil {
			row = &aiUsageRow{User: entry.User, Owner: entry.Owner}
			rows[entry.User] = row
		}
		row.Requests++
		row.Tokens += entry.PromptTokens + entry.CompletionTokens
		row.Cost += entry.Cost
	}
	out := make([]aiUsageRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Cost != out[j].Cost {
			return out[i].Cost > out[j].Cost
		}
		return out[i].User < out[j].User
	})
	return out
}

// describeAIUsage is the AI part of hi server ai and hi server spend.
func describeAIUsage(rows []aiUsageRow) string {
	if len(rows) == 0 {
		return "No model requests."
	}
	var total float64
	var lines []string
	for _, row := range rows {
		total += row.Cost
		name := row.User
		if row.Owner != "" {
			name += " (agent of " + row.Owner + ")"
		}
		lines = append(lines, fmt.Sprintf("• %s: %s, %d requests, %s tokens", name, formatDollars(row.Cost), row.Requests, formatTokens(row.Tokens)))
	}
	return fmt.Sprintf("%s in total\n%s", formatDollars(total), strings.Join(lines, "\n"))
}

func formatTokens(tokens int) string {
	switch {
	case tokens >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(tokens)/1e6)
	case tokens >= 1_000:
		return fmt.Sprintf("%.1fk", float64(tokens)/1e3)
	}
	return fmt.Sprint(tokens)
}

// ---------------------------------------------------------------------------
// admin

func (s *hiServer) aiAdminRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/ai", func(w http.ResponseWriter, _ *http.Request) {
		settings, key, on := s.aiConfig()
		now := computeNow()
		writeJSON(w, http.StatusOK, map[string]any{
			"on": on, "has_key": key != "" || settings.NoKey, "url": settings.URL, "model": settings.Model,
			"usage": describeAIUsage(readAIUsage(s.dir, monthStart(now), now)),
		})
	})
	mux.HandleFunc("POST /admin/ai", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			As    string `json:"as"`
			Key   string `json:"key"`
			URL   string `json:"url"`
			Model string `json:"model"`
			Off   *bool  `json:"off"`
			NoKey *bool  `json:"no_key"`
		}
		json.NewDecoder(io.LimitReader(r.Body, serverMaxBody)).Decode(&input)
		settings := readServerAISettings(s.dir)
		if input.URL != "" {
			settings.URL = strings.TrimRight(input.URL, "/")
		}
		if input.Model != "" {
			settings.Model = input.Model
		}
		if input.Off != nil {
			settings.Off = *input.Off
		}
		if input.NoKey != nil {
			settings.NoKey = *input.NoKey
		}
		s.mu.Lock()
		if input.Key != "" {
			s.keys[serverAIKeyName] = strings.TrimSpace(input.Key)
		}
		hasKey := s.keys[serverAIKeyName] != "" || settings.NoKey
		err := s.saveKeysLocked()
		s.mu.Unlock()
		if err == nil {
			err = writeServerAISettings(s.dir, settings)
		}
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		switch {
		case input.Off != nil && *input.Off:
			s.audit(input.As, "ai off", settings.Model, "")
		default:
			s.audit(input.As, "ai set", settings.Model, qHost(settings.URL))
		}
		writeJSON(w, http.StatusOK, map[string]any{"on": hasKey && !settings.Off, "model": settings.Model, "url": settings.URL})
	})
	mux.HandleFunc("DELETE /admin/ai", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		delete(s.keys, serverAIKeyName)
		err := s.saveKeysLocked()
		s.mu.Unlock()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.audit(r.URL.Query().Get("as"), "ai remove", "", "")
		writeJSON(w, http.StatusOK, map[string]any{"on": false})
	})
}

// checkServerAIKey makes one small request, so a wrong key or model shows
// up now rather than on a teammate's first question.
func checkServerAIKey(url, key, model string) error {
	provider := qOpenAI{baseURL: url, key: key, model: model}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := qAskOnce(ctx, provider, "Reply with the single word ok.", "Are you there?")
	return err
}

func serverAICommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("ai", stderr)
	as := flags.String("as", currentUserName(), "who is acting")
	urlFlag := flags.String("url", "", "an OpenAI-compatible base URL (default OpenRouter)")
	modelFlag := flags.String("model", "", "the default model (default "+serverAIDefaultModel+")")
	noKey := flags.Bool("no-key", false, "the endpoint takes no key, such as a model on the team's own machine")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	usage := usageError{"usage: hi server ai [set [--url URL] [--model M] [--no-key] | off | remove]"}
	dir, err := serverDirectory(*dirFlag)
	if err != nil {
		return err
	}
	running := false
	if _, err := os.Stat(filepath.Join(dir, "admin.sock")); err == nil {
		running = true
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); err != nil {
		return fmt.Errorf("no server in %s; run `hi server init` first", dir)
	}
	action := ""
	if len(positional) > 0 {
		action = positional[0]
	}
	if len(positional) > 1 {
		return usage
	}
	switch action {
	case "":
		if !running {
			settings := readServerAISettings(dir)
			fmt.Fprintf(stdout, "Server not running. Settings: %s at %s.\n", settings.Model, settings.URL)
			return nil
		}
		var status struct {
			On     bool   `json:"on"`
			HasKey bool   `json:"has_key"`
			URL    string `json:"url"`
			Model  string `json:"model"`
			Usage  string `json:"usage"`
		}
		if err := adminCall(*dirFlag, http.MethodGet, "/admin/ai", nil, &status); err != nil {
			return err
		}
		switch {
		case status.On:
			fmt.Fprintf(stdout, "Serving %s's models to connected devices; the default is %s.\n", qHost(status.URL), status.Model)
		case status.HasKey:
			fmt.Fprintln(stdout, "Off. Turn it on with `hi server ai set`.")
		default:
			fmt.Fprintln(stdout, "Not set up. Store an OpenRouter key with `hi server ai set`.")
			return nil
		}
		fmt.Fprintf(stdout, "\nThis month\n%s\n", strings.ReplaceAll(status.Usage, "•", "-"))
		return nil
	case "set":
		settings := readServerAISettings(dir)
		if *urlFlag != "" {
			if err := qValidBaseURL(*urlFlag); err != nil {
				return err
			}
			settings.URL = strings.TrimRight(*urlFlag, "/")
		}
		if *modelFlag != "" {
			settings.Model = *modelFlag
		}
		key := ""
		keys := map[string]string{}
		if data, err := os.ReadFile(filepath.Join(dir, "keys.json")); err == nil {
			json.Unmarshal(data, &keys)
		}
		settings.NoKey = *noKey
		if *noKey && *urlFlag == "" {
			return usageError{"--no-key needs --url, the endpoint that takes no key"}
		}
		if !*noKey && (keys[serverAIKeyName] == "" || *urlFlag != "" || isTerminal(stdin)) {
			prompt := "the upstream"
			if qHost(settings.URL) == "openrouter.ai" {
				prompt = "OpenRouter"
			}
			if keys[serverAIKeyName] != "" && isTerminal(stdin) {
				fmt.Fprintln(stdout, "A key is stored; press Enter to keep it.")
			}
			key, err = readOptionalKey(prompt, stdin, stdout, keys[serverAIKeyName] != "")
			if err != nil {
				return err
			}
		}
		checkKey := firstNonEmpty(key, keys[serverAIKeyName])
		if *noKey {
			checkKey = ""
		}
		fmt.Fprintf(stdout, "Checking %s with %s...\n", settings.Model, qHost(settings.URL))
		if err := checkServerAIKey(settings.URL, checkKey, settings.Model); err != nil {
			return fmt.Errorf("the key or model doesn't work: %w", err)
		}
		off := false
		if running {
			body := map[string]any{"as": *as, "key": key, "url": settings.URL, "model": settings.Model, "off": &off, "no_key": noKey}
			if err := adminCall(*dirFlag, http.MethodPost, "/admin/ai", body, nil); err != nil {
				return err
			}
		} else {
			settings.Off = false
			if key != "" {
				keys[serverAIKeyName] = key
				data, _ := json.MarshalIndent(keys, "", "  ")
				if err := os.WriteFile(filepath.Join(dir, "keys.json"), append(data, '\n'), 0o600); err != nil {
					return err
				}
			}
			if err := writeServerAISettings(dir, settings); err != nil {
				return err
			}
		}
		fmt.Fprintf(stdout, "Connected devices can now use %s's models through this server; hi q's default is %s.\n", qHost(settings.URL), settings.Model)
		if !*noKey {
			fmt.Fprintln(stdout, "Tip: set a spending limit on the key with the upstream, too.")
		}
		return nil
	case "off":
		off := true
		if !running {
			settings := readServerAISettings(dir)
			settings.Off = true
			return writeServerAISettings(dir, settings)
		}
		if err := adminCall(*dirFlag, http.MethodPost, "/admin/ai", map[string]any{"as": *as, "off": &off}, nil); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "Off. Devices use their own keys again; the key is kept for `hi server ai set`.")
		return nil
	case "remove":
		if !running {
			return errors.New("start the server first (`hi server run`), so the key is removed from the running server too")
		}
		if err := adminCall(*dirFlag, http.MethodDelete, "/admin/ai?as="+*as, nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(stdout, "The key is deleted. Devices use their own keys again.")
		return nil
	}
	return usage
}

// readOptionalKey reads a key without echo; with keep, an empty answer
// keeps the stored one and returns "".
func readOptionalKey(name string, stdin io.Reader, stdout io.Writer, keep bool) (string, error) {
	if file, ok := stdin.(*os.File); ok && isTerminal(stdin) {
		fmt.Fprintf(stdout, "%s API key (paste it; it shows as *): ", name)
		key, err := readSecret(file, stdout)
		fmt.Fprintln(stdout)
		if err != nil && !(keep && len(key) == 0) {
			return "", err
		}
		if strings.TrimSpace(string(key)) == "" && !keep {
			return "", errors.New("no key was entered")
		}
		return strings.TrimSpace(string(key)), nil
	}
	line, err := readLine(stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if strings.TrimSpace(line) == "" && !keep {
		return "", errors.New("no key given; run this in a terminal, or pipe the key on stdin")
	}
	return strings.TrimSpace(line), nil
}
