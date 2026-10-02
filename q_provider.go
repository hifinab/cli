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
	"sync"
	"time"
)

const (
	qOpenAIURL       = "https://api.openai.com/v1"
	qOpenAIModel     = "gpt-5-mini"
	qOpenRouterURL   = "https://openrouter.ai/api/v1"
	qOpenRouterModel = "anthropic/claude-haiku-4.5"
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

// qToolCall is a request from the model to run one of hi's tools.
type qToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"args"`
}

// qTurn is one entry of a conversation: the user's words, the model's text
// or tool calls, or the result of one tool call.
type qTurn struct {
	Role   string      `json:"role"` // user, assistant, or tool
	Text   string      `json:"text,omitempty"`
	Calls  []qToolCall `json:"calls,omitempty"`
	CallID string      `json:"call_id,omitempty"`
	Tool   string      `json:"tool,omitempty"`
}

// qStep is what a model did with a conversation: replied, or asked for
// tools. A command arrives as a propose call, kept in proposal.
type qStep struct {
	reply    qReply
	proposal qToolCall
	calls    []qToolCall
}

// qProvider takes one step of a conversation. lookup offers the read-only
// tools as well as propose.
type qProvider interface {
	label() string
	step(ctx context.Context, system string, turns []qTurn, lookup bool) (qStep, error)
}

// qAskOnce sends one message with no lookup tools, for setup checks and
// explanations.
func qAskOnce(ctx context.Context, provider qProvider, system, user string) (qReply, error) {
	step, err := provider.step(ctx, system, []qTurn{{Role: "user", Text: user}}, false)
	return step.reply, err
}

// qToolSpec describes a tool to the model.
type qToolSpec struct {
	name        string
	description string
	schema      map[string]any
}

var qProposeSpec = qToolSpec{
	name:        "propose",
	description: "Propose one shell command (or a short script) for the user to confirm and run. Use this whenever the user wants something done. This ends your turn.",
	schema: map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{"type": "string", "description": "The exact command to run in the user's shell."},
			"reason":  map[string]any{"type": "string", "description": "One short sentence saying what it does."},
			"risk": map[string]any{"type": "string", "enum": []string{"read-only", "changes", "dangerous"},
				"description": "read-only if it changes nothing; dangerous if it deletes data, needs root, or is hard to undo."},
		},
		"required": []string{"command", "reason", "risk"},
	},
}

func qTools(lookup bool) []qToolSpec {
	if !lookup {
		return []qToolSpec{qProposeSpec}
	}
	return append([]qToolSpec{qProposeSpec}, qLookupTools...)
}

// qStepSchema is the structured output asked of agent CLIs, which can't
// take custom tools: one reply or one tool call per step.
func qStepSchema(lookup bool) map[string]any {
	kinds := []string{"command", "answer"}
	properties := map[string]any{
		"kind":    nil,
		"command": map[string]any{"type": "string"},
		"reason":  map[string]any{"type": "string"},
		"risk":    map[string]any{"type": "string", "enum": []string{"read-only", "changes", "dangerous"}},
		"answer":  map[string]any{"type": "string"},
	}
	if lookup {
		kinds = append(kinds, "tool")
		var names []string
		for _, tool := range qLookupTools {
			names = append(names, tool.name)
		}
		properties["tool"] = map[string]any{"type": "string", "enum": names}
		properties["args"] = map[string]any{"type": "object"}
	}
	properties["kind"] = map[string]any{"type": "string", "enum": kinds}
	return map[string]any{"type": "object", "properties": properties, "required": []string{"kind"}}
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

// qStepFromCalls turns tool calls into a step: a propose call wins, since
// it ends the turn.
func qStepFromCalls(calls []qToolCall, text string) (qStep, error) {
	for _, call := range calls {
		if call.Name != "propose" {
			continue
		}
		var reply qReply
		if err := json.Unmarshal(call.Args, &reply); err != nil {
			return qStep{}, fmt.Errorf("the model's command could not be read: %w", err)
		}
		reply.Kind = "command"
		return qStep{reply: reply, proposal: call}, reply.valid()
	}
	if len(calls) > 0 {
		return qStep{calls: calls}, nil
	}
	reply, err := qTextReply(text)
	return qStep{reply: reply}, err
}

var qCallCounter struct {
	sync.Mutex
	n int
}

func qNewCallID() string {
	qCallCounter.Lock()
	defer qCallCounter.Unlock()
	qCallCounter.n++
	return fmt.Sprintf("call_hi_%d_%d", time.Now().UnixNano()%1e6, qCallCounter.n)
}

// qCompleteTurns adds a result for any tool call that has none, which
// APIs require, for example a proposal the user never answered. Results
// follow their call directly.
func qCompleteTurns(turns []qTurn) []qTurn {
	var out []qTurn
	for i := 0; i < len(turns); i++ {
		turn := turns[i]
		out = append(out, turn)
		if turn.Role != "assistant" || len(turn.Calls) == 0 {
			continue
		}
		answered := map[string]bool{}
		for i+1 < len(turns) && turns[i+1].Role == "tool" {
			i++
			out = append(out, turns[i])
			answered[turns[i].CallID] = true
		}
		for _, call := range turn.Calls {
			if !answered[call.ID] {
				out = append(out, qTurn{Role: "tool", CallID: call.ID, Tool: call.Name, Text: "The user did not run it."})
			}
		}
	}
	return out
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
	return fmt.Sprintf("%s at %s", p.model, qHost(p.baseURL))
}

// qNoTools remembers endpoints and models that rejected tools, so later
// steps don't try again.
var qNoTools sync.Map

func (p qOpenAI) step(ctx context.Context, system string, turns []qTurn, lookup bool) (qStep, error) {
	cacheKey := p.baseURL + " " + p.model
	if _, ok := qNoTools.Load(cacheKey); !ok {
		step, err := p.request(ctx, system, turns, qTools(lookup))
		if err == nil || !qToolsUnsupported(err) {
			return step, err
		}
		qNoTools.Store(cacheKey, true)
	}
	// Some models, mostly small local ones, reject tools. They are asked
	// for the reply as JSON instead, which qTextReply reads.
	return p.request(ctx, system+"\n\n"+qJSONInstruction, qFlattenTurns(turns), nil)
}

const qJSONInstruction = `You have no tools here. To propose a command, reply with only this JSON and nothing else: {"kind":"command","command":"...","reason":"...","risk":"read-only|changes|dangerous"}. To answer a question, reply in plain text.`

func (p qOpenAI) request(ctx context.Context, system string, turns []qTurn, tools []qToolSpec) (qStep, error) {
	messages := []map[string]any{{"role": "system", "content": system}}
	for _, turn := range qCompleteTurns(turns) {
		switch turn.Role {
		case "user":
			messages = append(messages, map[string]any{"role": "user", "content": turn.Text})
		case "assistant":
			message := map[string]any{"role": "assistant", "content": turn.Text}
			if len(turn.Calls) > 0 {
				var calls []map[string]any
				for _, call := range turn.Calls {
					calls = append(calls, map[string]any{
						"id": call.ID, "type": "function",
						"function": map[string]any{"name": call.Name, "arguments": string(call.Args)},
					})
				}
				message["tool_calls"] = calls
			}
			messages = append(messages, message)
		case "tool":
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": turn.CallID, "content": turn.Text})
		}
	}
	body := map[string]any{"model": p.model, "messages": messages}
	if len(tools) > 0 {
		var specs []map[string]any
		for _, tool := range tools {
			specs = append(specs, map[string]any{
				"type":     "function",
				"function": map[string]any{"name": tool.name, "description": tool.description, "parameters": tool.schema},
			})
		}
		body["tools"] = specs
		body["tool_choice"] = "auto"
	}
	headers := map[string]string{}
	if p.key != "" {
		headers["Authorization"] = "Bearer " + p.key
	}
	if qHost(p.baseURL) == "openrouter.ai" {
		// OpenRouter's optional attribution headers.
		headers["HTTP-Referer"] = "https://hifin.sh"
		headers["X-Title"] = "hi q"
	}
	var response struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := qPostJSON(ctx, p.client, strings.TrimRight(p.baseURL, "/")+"/chat/completions", headers, body, &response); err != nil {
		return qStep{}, err
	}
	if len(response.Choices) == 0 {
		return qStep{}, errors.New("the model sent no reply")
	}
	message := response.Choices[0].Message
	var calls []qToolCall
	for _, call := range message.ToolCalls {
		id := call.ID
		if id == "" {
			id = qNewCallID()
		}
		arguments := call.Function.Arguments
		if strings.TrimSpace(arguments) == "" {
			arguments = "{}"
		}
		calls = append(calls, qToolCall{ID: id, Name: call.Function.Name, Args: json.RawMessage(arguments)})
	}
	return qStepFromCalls(calls, message.Content)
}

// qFlattenTurns writes tool calls and results as text, for models without
// tools.
func qFlattenTurns(turns []qTurn) []qTurn {
	var out []qTurn
	var pending strings.Builder
	flush := func(role string) {
		if pending.Len() > 0 {
			out = append(out, qTurn{Role: role, Text: strings.TrimSpace(pending.String())})
			pending.Reset()
		}
	}
	for _, turn := range turns {
		switch turn.Role {
		case "user":
			pending.WriteString(turn.Text + "\n")
			flush("user")
		case "assistant":
			text := turn.Text
			for _, call := range turn.Calls {
				text += fmt.Sprintf("\n[%s %s]", call.Name, call.Args)
			}
			out = append(out, qTurn{Role: "assistant", Text: strings.TrimSpace(text)})
		case "tool":
			pending.WriteString(fmt.Sprintf("[result of %s]\n%s\n", turn.Tool, turn.Text))
		}
	}
	flush("user")
	return out
}

// qHTTPError is a non-2xx answer, kept typed so callers can look at it.
type qHTTPError struct {
	host    string
	status  string
	code    int
	message string
}

func (e *qHTTPError) Error() string {
	return fmt.Sprintf("%s answered %s: %s", e.host, e.status, e.message)
}

func qToolsUnsupported(err error) bool {
	var httpErr *qHTTPError
	if !errors.As(err, &httpErr) || httpErr.code < 400 || httpErr.code >= 500 || httpErr.code == 401 || httpErr.code == 403 {
		return false
	}
	return strings.Contains(strings.ToLower(httpErr.message), "tool")
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

func (p qAnthropic) step(ctx context.Context, system string, turns []qTurn, lookup bool) (qStep, error) {
	// Anthropic wants alternating roles, with tool results inside a user
	// message, so consecutive user-side turns are merged.
	var messages []map[string]any
	var userBlocks []map[string]any
	flushUser := func() {
		if len(userBlocks) > 0 {
			messages = append(messages, map[string]any{"role": "user", "content": userBlocks})
			userBlocks = nil
		}
	}
	for _, turn := range qCompleteTurns(turns) {
		switch turn.Role {
		case "user":
			userBlocks = append(userBlocks, map[string]any{"type": "text", "text": turn.Text})
		case "tool":
			userBlocks = append(userBlocks, map[string]any{"type": "tool_result", "tool_use_id": turn.CallID, "content": turn.Text})
		case "assistant":
			flushUser()
			var blocks []map[string]any
			if strings.TrimSpace(turn.Text) != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": turn.Text})
			}
			for _, call := range turn.Calls {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": call.ID, "name": call.Name, "input": call.Args})
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
		}
	}
	flushUser()
	var tools []map[string]any
	for _, tool := range qTools(lookup) {
		tools = append(tools, map[string]any{"name": tool.name, "description": tool.description, "input_schema": tool.schema})
	}
	body := map[string]any{
		"model":      p.model,
		"max_tokens": 2048,
		"system":     system,
		"messages":   messages,
		"tools":      tools,
	}
	headers := map[string]string{
		"x-api-key":         p.key,
		"anthropic-version": "2023-06-01",
	}
	var response struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			ID    string          `json:"id"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	}
	if err := qPostJSON(ctx, p.client, strings.TrimRight(p.baseURL, "/")+"/v1/messages", headers, body, &response); err != nil {
		return qStep{}, err
	}
	var text strings.Builder
	var calls []qToolCall
	for _, block := range response.Content {
		switch block.Type {
		case "tool_use":
			id := block.ID
			if id == "" {
				id = qNewCallID()
			}
			calls = append(calls, qToolCall{ID: id, Name: block.Name, Args: block.Input})
		case "text":
			text.WriteString(block.Text)
		}
	}
	return qStepFromCalls(calls, text.String())
}

// ---------------------------------------------------------------------------
// Claude Code, through its non-interactive mode with its own tools off

type qClaudeCode struct {
	binary string
	model  string
}

func (p qClaudeCode) label() string { return "Claude Code (" + p.model + ")" }

func (p qClaudeCode) step(ctx context.Context, system string, turns []qTurn, lookup bool) (qStep, error) {
	schema, _ := json.Marshal(qStepSchema(lookup))
	if lookup {
		system += "\n\n" + qClaudeCodeToolNote()
	}
	command := exec.CommandContext(ctx, p.binary,
		"-p", "--tools", "", "--json-schema", string(schema),
		"--output-format", "json", "--no-session-persistence",
		"--model", p.model, "--system-prompt", system)
	command.Stdin = strings.NewReader(qTranscript(turns))
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
			return qStep{}, fmt.Errorf("claude failed: %s", qFirstLine(stderr.String(), err.Error()))
		}
		return qStep{}, fmt.Errorf("claude sent output hi could not read: %w", jsonErr)
	}
	if result.IsError {
		return qStep{}, fmt.Errorf("claude: %s", qFirstLine(result.Result, "request failed"))
	}
	var parsed struct {
		qReply
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	}
	raw := result.StructuredOutput
	if len(raw) == 0 || string(raw) == "null" {
		raw = json.RawMessage(result.Result)
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		reply, textErr := qTextReply(result.Result)
		return qStep{reply: reply}, textErr
	}
	if parsed.Kind == "tool" {
		args := parsed.Args
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		return qStep{calls: []qToolCall{{ID: qNewCallID(), Name: parsed.Tool, Args: args}}}, nil
	}
	reply := parsed.qReply
	if reply.Kind == "" && reply.Command != "" {
		reply.Kind = "command"
	}
	step := qStep{reply: reply}
	if reply.Kind == "command" {
		args, _ := json.Marshal(reply)
		step.proposal = qToolCall{ID: qNewCallID(), Name: "propose", Args: args}
	}
	return step, reply.valid()
}

func qClaudeCodeToolNote() string {
	var b strings.Builder
	b.WriteString(`Each reply is one step. Use kind "tool" with "tool" and "args" to call one of these, and you'll get its result in the next message; use kind "command" to propose a command, or kind "answer" to answer.` + "\n")
	for _, tool := range qLookupTools {
		schema, _ := json.Marshal(tool.schema["properties"])
		fmt.Fprintf(&b, "- %s %s: %s\n", tool.name, schema, tool.description)
	}
	return b.String()
}

// qTranscript writes a conversation as one prompt, for backends that take
// a single message.
func qTranscript(turns []qTurn) string {
	if len(turns) == 1 && turns[0].Role == "user" {
		return turns[0].Text
	}
	var b strings.Builder
	b.WriteString("The conversation so far, oldest first. Reply to the end of it.\n\n")
	for _, turn := range qCompleteTurns(turns) {
		switch turn.Role {
		case "user":
			fmt.Fprintf(&b, "<user>\n%s\n</user>\n", turn.Text)
		case "assistant":
			if turn.Text != "" {
				fmt.Fprintf(&b, "<you>\n%s\n</you>\n", turn.Text)
			}
			for _, call := range turn.Calls {
				fmt.Fprintf(&b, "<you called=%q>%s</you>\n", call.Name, call.Args)
			}
		case "tool":
			fmt.Fprintf(&b, "<result of=%q>\n%s\n</result>\n", turn.Tool, turn.Text)
		}
	}
	return b.String()
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
		return &qHTTPError{host: qHost(url), status: response.Status, code: response.StatusCode, message: qErrorMessage(payload)}
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
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		chosen := firstNonEmpty(model, os.Getenv("HI_Q_MODEL"), qOpenRouterModel)
		return qChoice{provider: qOpenAI{baseURL: qOpenRouterURL, key: key, model: chosen}, source: "OPENROUTER_API_KEY"}, nil
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
	case "openrouter":
		if config.Provider != "openrouter" {
			config = qConfig{}
		}
		if config.Provider != "openrouter" || key == "" {
			key = os.Getenv("OPENROUTER_API_KEY")
		}
		if key == "" {
			return qChoice{}, errors.New("no OpenRouter key; run hi q --setup or set OPENROUTER_API_KEY")
		}
		chosen := firstNonEmpty(model, config.Model, qOpenRouterModel)
		return qChoice{provider: qOpenAI{baseURL: qOpenRouterURL, key: key, model: chosen}, source: "--provider openrouter"}, nil
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
	return qChoice{}, fmt.Errorf("unknown provider %q; use openai, openrouter, anthropic, or claude", name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
