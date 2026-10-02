package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"
)

// qSetupCommand is hi q --setup: the model, then the shell integration.
func qSetupCommand(stdin io.Reader, stdout io.Writer) error {
	ui := newMenuUI(stdin, stdout)
	current, err := resolveQProvider("", "")
	if err != nil {
		return err
	}
	if current.provider != nil {
		_, rc := qShellRC()
		options := []string{"Choose the model (now " + current.provider.label() + ")"}
		if rc != "" && qShellInstalled() == "" {
			options = append(options, "Set up the shell ("+rc+")")
		}
		options = append(options, "Cancel")
		choice, err := ui.choose("What do you want to set up?", options, false)
		if err != nil || options[choice] == "Cancel" {
			return nil
		}
		if choice == 1 {
			return offerQShell(ui)
		}
	}
	if _, err := runQSetup(ui); err != nil {
		if errors.Is(err, errMenuBack) {
			return nil
		}
		return err
	}
	return offerQShell(ui)
}

// runQSetup is the first-run menu: it offers what it found on the machine,
// then an OpenAI-compatible endpoint or an Anthropic key, tests the choice
// with one small request, and saves it.
func runQSetup(ui menuUI) (qChoice, error) {
	type option struct {
		label string
		pick  func() (qProvider, qConfig, string, error)
	}
	var options []option
	if binary, err := exec.LookPath("claude"); err == nil {
		options = append(options, option{"Claude Code: your Claude sign-in, a few seconds per answer", func() (qProvider, qConfig, string, error) {
			return qClaudeCode{binary: binary, model: qClaudeCodeModel}, qConfig{Provider: "claude", Model: qClaudeCodeModel}, "", nil
		}})
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		options = append(options, option{"OpenAI, with OPENAI_API_KEY from the environment", func() (qProvider, qConfig, string, error) {
			base := firstNonEmpty(os.Getenv("OPENAI_BASE_URL"), qOpenAIURL)
			model, err := qChooseModel(ui, base, key)
			if err != nil {
				return nil, qConfig{}, "", err
			}
			return qOpenAI{baseURL: base, key: key, model: model}, qConfig{Provider: "openai", BaseURL: base, Model: model}, "", nil
		}})
	}
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		options = append(options, option{"Anthropic, with ANTHROPIC_API_KEY from the environment", func() (qProvider, qConfig, string, error) {
			return qAnthropic{baseURL: qAnthropicURL, key: key, model: qAnthropicModel}, qConfig{Provider: "anthropic", Model: qAnthropicModel}, "", nil
		}})
	}
	openRouter := func(key string, save bool) func() (qProvider, qConfig, string, error) {
		return func() (qProvider, qConfig, string, error) {
			if key == "" {
				var err error
				if key, err = ui.secret("OpenRouter API key (openrouter.ai/keys)"); err != nil {
					return nil, qConfig{}, "", err
				}
			}
			model, err := qChooseModel(ui, qOpenRouterURL, key)
			if err != nil {
				return nil, qConfig{}, "", err
			}
			saved := ""
			if save {
				saved = key
			}
			return qOpenAI{baseURL: qOpenRouterURL, key: key, model: model}, qConfig{Provider: "openrouter", Model: model}, saved, nil
		}
	}
	if key := os.Getenv("OPENROUTER_API_KEY"); key != "" {
		options = append(options, option{"OpenRouter, with OPENROUTER_API_KEY from the environment", openRouter(key, false)})
	}
	options = append(options,
		option{"OpenRouter: one key for models from Anthropic, OpenAI, Google, DeepSeek, …", openRouter("", true)},
		option{"An OpenAI-compatible endpoint: OpenAI, Ollama, vLLM, llama.cpp, …", func() (qProvider, qConfig, string, error) {
			base, err := ui.input("Base URL", qOpenAIURL, qValidBaseURL)
			if err != nil {
				return nil, qConfig{}, "", err
			}
			base = strings.TrimRight(base, "/")
			key := ""
			if !qLocalURL(base) {
				if key, err = ui.secret("API key"); err != nil {
					return nil, qConfig{}, "", err
				}
			}
			model, err := qChooseModel(ui, base, key)
			if err != nil {
				return nil, qConfig{}, "", err
			}
			return qOpenAI{baseURL: base, key: key, model: model}, qConfig{Provider: "openai", BaseURL: base, Model: model}, key, nil
		}},
		option{"An Anthropic API key", func() (qProvider, qConfig, string, error) {
			key, err := ui.secret("Anthropic API key")
			if err != nil {
				return nil, qConfig{}, "", err
			}
			model, err := ui.input("Model", qAnthropicModel, nil)
			if err != nil {
				return nil, qConfig{}, "", err
			}
			return qAnthropic{baseURL: qAnthropicURL, key: key, model: model}, qConfig{Provider: "anthropic", Model: model}, key, nil
		}},
	)

	labels := make([]string, 0, len(options)+1)
	for _, item := range options {
		labels = append(labels, item.label)
	}
	labels = append(labels, "Cancel")
	for {
		index, err := ui.choose("Which model should hi q use?", labels, false)
		if err != nil || index >= len(options) {
			return qChoice{}, errMenuBack
		}
		provider, config, key, err := options[index].pick()
		if errors.Is(err, errMenuBack) {
			continue
		}
		if err != nil {
			ui.failure(err)
			continue
		}
		var testErr error
		ui.busy("Trying "+provider.label()+"…", func() {
			ctx, cancel := context.WithTimeout(context.Background(), qRequestTimeout)
			defer cancel()
			_, testErr = qAskOnce(ctx, provider, "Reply with the single word ok.", "Are you there?")
		})
		if testErr != nil {
			ui.failure(testErr)
			continue
		}
		if err := saveQConfig(config, key); err != nil {
			return qChoice{}, err
		}
		ui.note(fmt.Sprintf("hi q uses %s. Change it with hi q --setup.", provider.label()))
		return qChoice{provider: provider, source: "saved by hi q --setup", saved: true}, nil
	}
}

// qSuggestedModels are fast, cheap models that call tools well. Those the
// endpoint offers are listed first.
var qSuggestedModels = []string{
	qOpenAIModel,
	qOpenRouterModel,
	"google/gemini-2.5-flash",
	"openai/gpt-5-mini",
	"deepseek/deepseek-v4-flash",
	"openai/gpt-oss-120b",
}

// qChooseModel offers the endpoint's model list when it has one, and asks
// for a name otherwise.
func qChooseModel(ui menuUI, base, key string) (string, error) {
	var models []string
	ui.busy("Listing models…", func() { models = qListModels(base, key) })
	if len(models) == 0 {
		fallback := ""
		if strings.TrimRight(base, "/") == qOpenAIURL {
			fallback = qOpenAIModel
		}
		return ui.input("Model", fallback, func(value string) error {
			if strings.TrimSpace(value) == "" {
				return errors.New("type the model's name")
			}
			return nil
		})
	}
	models = qSuggestedFirst(models)
	index, err := ui.choose("Model", models, len(models) > 10)
	if err != nil {
		return "", err
	}
	return models[index], nil
}

func qSuggestedFirst(models []string) []string {
	present := map[string]bool{}
	for _, model := range models {
		present[model] = true
	}
	var first []string
	for _, model := range qSuggestedModels {
		if present[model] {
			first = append(first, model)
			delete(present, model)
		}
	}
	rest := make([]string, 0, len(models))
	for _, model := range models {
		if present[model] {
			rest = append(rest, model)
		}
	}
	return append(first, rest...)
}

// qListModels reads the endpoint's /models list. When the list says which
// models take tools, as OpenRouter's does, only those are offered.
func qListModels(base, key string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return nil
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil
	}
	var listing struct {
		Data []struct {
			ID                  string   `json:"id"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&listing) != nil {
		return nil
	}
	described := false
	for _, model := range listing.Data {
		if model.SupportedParameters != nil {
			described = true
			break
		}
	}
	var models []string
	for _, model := range listing.Data {
		if model.ID == "" || strings.HasSuffix(model.ID, ":batch") {
			continue
		}
		if described && !qOneOf("tools", model.SupportedParameters...) {
			continue
		}
		models = append(models, model.ID)
	}
	sort.Strings(models)
	return models
}

func qValidBaseURL(value string) error {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("enter a URL such as https://api.openai.com/v1 or http://localhost:11434/v1")
	}
	return nil
}

// qLocalURL is true for endpoints on this machine or a private network,
// which usually take no key.
func qLocalURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".local") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}
