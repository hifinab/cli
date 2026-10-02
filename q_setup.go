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

func qSetupCommand(stdin io.Reader, stdout io.Writer) error {
	_, err := runQSetup(newMenuUI(stdin, stdout))
	if errors.Is(err, errMenuBack) {
		return nil
	}
	return err
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
	options = append(options,
		option{"An OpenAI-compatible endpoint: OpenAI, OpenRouter, Ollama, vLLM, …", func() (qProvider, qConfig, string, error) {
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
			_, testErr = provider.ask(ctx, "Reply with the single word ok.", "Are you there?")
		})
		if testErr != nil {
			ui.failure(testErr)
			continue
		}
		if err := saveQConfig(config, key); err != nil {
			return qChoice{}, err
		}
		ui.note(fmt.Sprintf("hi q uses %s. Change it with hi q setup.", provider.label()))
		return qChoice{provider: provider, source: "saved by hi q setup", saved: true}, nil
	}
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
	if strings.TrimRight(base, "/") == qOpenAIURL {
		for i, model := range models {
			if model == qOpenAIModel {
				models[0], models[i] = models[i], models[0]
			}
		}
	}
	index, err := ui.choose("Model", models, len(models) > 10)
	if err != nil {
		return "", err
	}
	return models[index], nil
}

func qListModels(base, key string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&listing) != nil {
		return nil
	}
	var models []string
	for _, model := range listing.Data {
		if model.ID != "" {
			models = append(models, model.ID)
		}
	}
	sort.Strings(models)
	if len(models) > 300 {
		models = models[:300]
	}
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
