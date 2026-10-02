package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	qOpenAIURL       = "https://api.openai.com/v1"
	qOpenAIModel     = "gpt-5-mini"
	qAnthropicURL    = "https://api.anthropic.com"
	qAnthropicModel  = "claude-haiku-4-5"
	qClaudeCodeModel = "haiku"
	qRequestTimeout  = 90 * time.Second
)

// qReply is one answer from a model: a command to confirm, or plain text.
type qReply struct {
	Kind    string `json:"kind"`
	Command string `json:"command,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Risk    string `json:"risk,omitempty"`
	Answer  string `json:"answer,omitempty"`
}

// qProvider sends one request to a model. Release 1 has no tool loop, so a
// request is the system prompt and one user message.
type qProvider interface {
	label() string
	ask(ctx context.Context, system, user string) (qReply, error)
}

// qProposeDescription and qProposeSchema describe the one tool API backends
// get: propose a command. A plain text reply is an answer.
const qProposeDescription = "Propose one shell command (or a short script) for the user to confirm and run. Use this whenever the user wants something done."

var qProposeSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"command": map[string]any{"type": "string", "description": "The exact command to run in the user's shell."},
		"reason":  map[string]any{"type": "string", "description": "One short sentence saying what it does."},
		"risk": map[string]any{"type": "string", "enum": []string{"read-only", "changes", "dangerous"},
			"description": "read-only if it changes nothing; dangerous if it deletes data, needs root, or is hard to undo."},
	},
	"required": []string{"command", "reason", "risk"},
}

// qReplySchema is the structured output asked of agent CLIs, which can't
// take custom tools.
var qReplySchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"kind":    map[string]any{"type": "string", "enum": []string{"command", "answer"}},
		"command": map[string]any{"type": "string"},
		"reason":  map[string]any{"type": "string"},
		"risk":    map[string]any{"type": "string", "enum": []string{"read-only", "changes", "dangerous"}},
		"answer":  map[string]any{"type": "string"},
	},
	"required": []string{"kind"},
}

func (r qReply) valid() error {
	switch r.Kind {
	case "command":
		if strings.TrimSpace(r.Command) == "" {
			return errors.New("the model proposed an empty command")
		}
	case "answer":
		if strings.TrimSpace(r.Answer) == "" {
			return errors.New("the model sent an empty answer")
		}
	default:
		return fmt.Errorf("the model sent an unknown reply kind %q", r.Kind)
	}
	return nil
}

// ---------------------------------------------------------------------------
// OpenAI-compatible chat completions

type qOpenAI struct {
	baseURL string
	key     string
	model   string
	client  *http.Client
}

func (p qOpenAI) label() string {
	host := strings.TrimPrefix(strings.TrimPrefix(p.baseURL, "https://"), "http://")
	host, _, _ = strings.Cut(host, "/")
	return fmt.Sprintf("%s at %s", p.model, host)
}

func (p qOpenAI) ask(ctx context.Context, system, user string) (qReply, error) {
	body := map[string]any{
		"model": p.model,
		"messages": []map[string]string{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name":        "propose",
				"description": qProposeDescription,
				"parameters":  qProposeSchema,
			},
		}},
		"tool_choice": "auto",
	}
	headers := map[string]string{}
	if p.key != "" {
		headers["Authorization"] = "Bearer " + p.key
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := qPostJSON(ctx, p.client, strings.TrimRight(p.baseURL, "/")+"/chat/completions", headers, body, &response); err != nil {
		return qReply{}, err
	}
	if len(response.Choices) == 0 {
		return qReply{}, errors.New("the model sent no reply")
	}
	message := response.Choices[0].Message
	for _, call := range message.ToolCalls {
		if call.Function.Name != "propose" {
			continue
		}
		reply := qReply{Kind: "command"}
		if err := json.Unmarshal([]byte(call.Function.Arguments), &reply); err != nil {
			return qReply{}, fmt.Errorf("the model's command could not be read: %w", err)
		}
		reply.Kind = "command"
		return reply, reply.valid()
	}
	return qTextReply(message.Content)
}

// ---------------------------------------------------------------------------
// Anthropic Messages API

type qAnthropic struct {
	baseURL string
	key     string
	model   string
	client  *http.Client
}

func (p qAnthropic) label() string { return p.model + " (Anthropic API)" }

func (p qAnthropic) ask(ctx context.Context, system, user string) (qReply, error) {
	body := map[string]any{
		"model":      p.model,
		"max_tokens": 1024,
		"system":     system,
		"messages":   []map[string]string{{"role": "user", "content": user}},
		"tools": []map[string]any{{
			"name":         "propose",
			"description":  qProposeDescription,
			"input_schema": qProposeSchema,
		}},
	}
	headers := map[string]string{
		"x-api-key":         p.key,
		"anthropic-version": "2023-06-01",
	}
	var response struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := qPostJSON(ctx, p.client, strings.TrimRight(p.baseURL, "/")+"/v1/messages", headers, body, &response); err != nil {
		return qReply{}, err
	}
	var text strings.Builder
	for _, block := range response.Content {
		switch {
		case block.Type == "tool_use" && block.Name == "propose":
			reply := qReply{}
			if err := json.Unmarshal(block.Input, &reply); err != nil {
				return qReply{}, fmt.Errorf("the model's command could not be read: %w", err)
			}
			reply.Kind = "command"
			return reply, reply.valid()
		case block.Type == "text":
			text.WriteString(block.Text)
		}
	}
	return qTextReply(text.String())
}

// ---------------------------------------------------------------------------
// Claude Code, through its non-interactive mode with its own tools off

type qClaudeCode struct {
	binary string
	model  string
}

func (p qClaudeCode) label() string { return "Claude Code (" + p.model + ")" }

func (p qClaudeCode) ask(ctx context.Context, system, user string) (qReply, error) {
	schema, _ := json.Marshal(qReplySchema)
	command := exec.CommandContext(ctx, p.binary,
		"-p", "--tools", "", "--json-schema", string(schema),
		"--output-format", "json", "--no-session-persistence",
		"--model", p.model, "--system-prompt", system)
	command.Stdin = strings.NewReader(user)
	// An empty folder keeps Claude Code from loading this project's
	// CLAUDE.md, settings, and hooks into a request that doesn't need them.
	command.Dir = os.TempDir()
	var stderr bytes.Buffer
	command.Stderr = &stderr
	output, err := command.Output()
	var result struct {
		IsError          bool            `json:"is_error"`
		Result           string          `json:"result"`
		StructuredOutput json.RawMessage `json:"structured_output"`
	}
	if jsonErr := json.Unmarshal(output, &result); jsonErr != nil {
		if err != nil {
			return qReply{}, fmt.Errorf("claude failed: %s", qFirstLine(stderr.String(), err.Error()))
		}
		return qReply{}, fmt.Errorf("claude sent output hi could not read: %w", jsonErr)
	}
	if result.IsError {
		return qReply{}, fmt.Errorf("claude: %s", qFirstLine(result.Result, "request failed"))
	}
	var reply qReply
	if len(result.StructuredOutput) > 0 && string(result.StructuredOutput) != "null" {
		if err := json.Unmarshal(result.StructuredOutput, &reply); err != nil {
			return qReply{}, fmt.Errorf("claude's reply could not be read: %w", err)
		}
	} else if err := json.Unmarshal([]byte(result.Result), &reply); err != nil {
		return qTextReply(result.Result)
	}
	if reply.Kind == "" && reply.Command != "" {
		reply.Kind = "command"
	}
	return reply, reply.valid()
}

// ---------------------------------------------------------------------------
// shared

func qPostJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	if client == nil {
		client = &http.Client{Timeout: qRequestTimeout}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("%s answered %s: %s", qHost(url), response.Status, qErrorMessage(payload))
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("%s sent a reply hi could not read: %w", qHost(url), err)
	}
	return nil
}

// qTextReply turns plain text into an answer. Models without tool calls
// sometimes write the JSON they were meant to send, so that is read too.
func qTextReply(text string) (qReply, error) {
	text = strings.TrimSpace(text)
	trimmed := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(text, "```json"), "```"), "```")
	var reply qReply
	if strings.HasPrefix(strings.TrimSpace(trimmed), "{") && json.Unmarshal([]byte(trimmed), &reply) == nil && reply.valid() == nil {
		return reply, nil
	}
	reply = qReply{Kind: "answer", Answer: text}
	return reply, reply.valid()
}

func qErrorMessage(payload []byte) string {
	var parsed struct {
		Error any `json:"error"`
	}
	if json.Unmarshal(payload, &parsed) == nil && parsed.Error != nil {
		switch value := parsed.Error.(type) {
		case string:
			return value
		case map[string]any:
			if message, ok := value["message"].(string); ok {
				return message
			}
		}
	}
	return qFirstLine(string(payload), "no details")
}

func qHost(url string) string {
	host := strings.TrimPrefix(strings.TrimPrefix(url, "https://"), "http://")
	host, _, _ = strings.Cut(host, "/")
	return host
}

func qFirstLine(text, fallback string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return fallback
	}
	line, _, _ := strings.Cut(text, "\n")
	if len(line) > 300 {
		line = line[:300] + "…"
	}
	return line
}

// ---------------------------------------------------------------------------
// choosing a provider

// qConfig is what `hi q --setup` saves. The key is kept apart, in q-key.
type qConfig struct {
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model,omitempty"`
}

func qConfigDirectory() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "hi")
}

func loadQConfig() (qConfig, string, error) {
	var config qConfig
	directory := qConfigDirectory()
	data, err := os.ReadFile(filepath.Join(directory, "q.json"))
	if errors.Is(err, os.ErrNotExist) {
		return config, "", nil
	}
	if err != nil {
		return config, "", err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return config, "", fmt.Errorf("%s: %w", filepath.Join(directory, "q.json"), err)
	}
	key, err := os.ReadFile(filepath.Join(directory, "q-key"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return config, "", err
	}
	return config, strings.TrimSpace(string(key)), nil
}

func saveQConfig(config qConfig, key string) error {
	directory := qConfigDirectory()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(directory, "q.json"), append(data, '\n'), 0o600); err != nil {
		return err
	}
	keyPath := filepath.Join(directory, "q-key")
	if key == "" {
		if err := os.Remove(keyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.WriteFile(keyPath, []byte(key+"\n"), 0o600); err != nil {
		return err
	}
	return os.Chmod(keyPath, 0o600)
}

// qChoice is a resolved provider and where it came from, for `hi q --status`.
type qChoice struct {
	provider qProvider
	source   string
	saved    bool
}

// resolveQProvider picks the provider in the spec's order: the flag, saved
// setup, HI_Q_* variables, OpenAI and Anthropic keys, then Claude Code.
func resolveQProvider(name, model string) (qChoice, error) {
	config, key, err := loadQConfig()
	if err != nil {
		return qChoice{}, err
	}
	if name != "" {
		return qProviderByName(name, model, config, key)
	}
	if config.Provider != "" {
		choice, err := qProviderByName(config.Provider, model, config, key)
		choice.source, choice.saved = "saved by hi q --setup", true
		return choice, err
	}
	if base := os.Getenv("HI_Q_BASE_URL"); base != "" {
		chosen := firstNonEmpty(model, os.Getenv("HI_Q_MODEL"))
		if chosen == "" {
			return qChoice{}, errors.New("HI_Q_BASE_URL is set but HI_Q_MODEL is not; set it to the model to use")
		}
		return qChoice{provider: qOpenAI{baseURL: base, key: os.Getenv("HI_Q_API_KEY"), model: chosen}, source: "HI_Q_BASE_URL"}, nil
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		base := firstNonEmpty(os.Getenv("OPENAI_BASE_URL"), qOpenAIURL)
		chosen := firstNonEmpty(model, os.Getenv("HI_Q_MODEL"), qOpenAIModel)
		return qChoice{provider: qOpenAI{baseURL: base, key: key, model: chosen}, source: "OPENAI_API_KEY"}, nil
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		chosen := firstNonEmpty(model, os.Getenv("HI_Q_MODEL"), qAnthropicModel)
		base := firstNonEmpty(os.Getenv("ANTHROPIC_BASE_URL"), qAnthropicURL)
		return qChoice{provider: qAnthropic{baseURL: base, key: key, model: chosen}, source: "ANTHROPIC_API_KEY"}, nil
	}
	if binary, err := exec.LookPath("claude"); err == nil {
		return qChoice{provider: qClaudeCode{binary: binary, model: firstNonEmpty(model, qClaudeCodeModel)}, source: "found Claude Code"}, nil
	}
	return qChoice{}, nil
}

func qProviderByName(name, model string, config qConfig, key string) (qChoice, error) {
	switch name {
	case "openai":
		if config.Provider != "openai" {
			config = qConfig{}
		}
		base := firstNonEmpty(config.BaseURL, os.Getenv("OPENAI_BASE_URL"), qOpenAIURL)
		if config.Provider != "openai" || key == "" {
			key = os.Getenv("OPENAI_API_KEY")
		}
		chosen := firstNonEmpty(model, config.Model, qOpenAIModel)
		return qChoice{provider: qOpenAI{baseURL: base, key: key, model: chosen}, source: "--provider openai"}, nil
	case "anthropic":
		if config.Provider != "anthropic" {
			config = qConfig{}
		}
		base := firstNonEmpty(config.BaseURL, os.Getenv("ANTHROPIC_BASE_URL"), qAnthropicURL)
		if config.Provider != "anthropic" || key == "" {
			key = os.Getenv("ANTHROPIC_API_KEY")
		}
		if key == "" {
			return qChoice{}, errors.New("no Anthropic key; run hi q --setup or set ANTHROPIC_API_KEY")
		}
		return qChoice{provider: qAnthropic{baseURL: base, key: key, model: firstNonEmpty(model, config.Model, qAnthropicModel)}, source: "--provider anthropic"}, nil
	case "claude":
		binary, err := exec.LookPath("claude")
		if err != nil {
			return qChoice{}, errors.New("Claude Code is not installed; run hi install claude")
		}
		chosen := qClaudeCodeModel
		if config.Provider == "claude" && config.Model != "" {
			chosen = config.Model
		}
		return qChoice{provider: qClaudeCode{binary: binary, model: firstNonEmpty(model, chosen)}, source: "--provider claude"}, nil
	}
	return qChoice{}, fmt.Errorf("unknown provider %q; use openai, anthropic, or claude", name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
