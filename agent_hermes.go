package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Hermes Agent (Nous Research) runs from its own install folder with its
// own Python, both mounted read-only into the box. Its model calls go to
// OpenRouter through the proxy's token listener, which puts the API key in
// place of a placeholder, so the key never enters the box. Its sessions
// are in a SQLite file in its home folder, which hi reads with python3.

func hermesHome() string {
	if home := os.Getenv("HERMES_HOME"); home != "" {
		return home
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".hermes")
}

func hermesEnvPath() string { return filepath.Join(hermesHome(), ".env") }

func hermesInstallDir() string {
	return firstNonEmpty(os.Getenv("HERMES_INSTALL_DIR"), filepath.Join(hermesHome(), "hermes-agent"))
}

// hermesModelConfig is the model block of Hermes' config.yaml.
type hermesModelConfig struct {
	Default, Provider, BaseURL, APIMode string
}

// readHermesModelConfig reads the keys under the top-level model: block of
// config.yaml; nothing else in the file is needed.
func readHermesModelConfig(path string) hermesModelConfig {
	var config hermesModelConfig
	data, err := os.ReadFile(path)
	if err != nil {
		return config
	}
	inModel := false
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			inModel = strings.TrimSpace(line) == "model:"
			continue
		}
		if !inModel {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		switch strings.TrimSpace(key) {
		case "default":
			config.Default = value
		case "provider":
			config.Provider = value
		case "base_url":
			config.BaseURL = value
		case "api_mode":
			config.APIMode = value
		}
	}
	return config
}

// hermesReady checks that Hermes is installed and uses OpenRouter with a
// key, the one provider hi can keep the key outside the box for.
func hermesReady() error {
	if !fileExists(filepath.Join(hermesInstallDir(), "venv", "bin", "python")) || !fileExists(filepath.Join(hermesInstallDir(), "hermes")) {
		return errors.New("Hermes is not installed on this machine; install it with hi install hermes")
	}
	config := readHermesModelConfig(filepath.Join(hermesHome(), "config.yaml"))
	if provider := firstNonEmpty(config.Provider, "openrouter"); provider != "openrouter" {
		return fmt.Errorf("hi agent hermes works with OpenRouter, and Hermes here uses %s; run hermes model and pick OpenRouter", provider)
	}
	if _, err := readDotEnvValue(hermesEnvPath(), "OPENROUTER_API_KEY"); err != nil {
		return errors.New("Hermes has no OpenRouter key in ~/.hermes/.env; run hermes setup")
	}
	return nil
}

// hermesBoxSetup mounts Hermes and its Python, writes its config and a
// placeholder key in the box's home folder, and returns its command.
func hermesBoxSetup(prompt string, meta boxMeta, homeDir, results string, run *[]string) ([]string, error) {
	install := hermesInstallDir()
	*run = append(*run, "-v", install+":"+install+":ro")
	// The venv's python is a link to a Python uv installed in the home
	// folder; that comes along read-only at the same path.
	if python, err := filepath.EvalSymlinks(filepath.Join(install, "venv", "bin", "python")); err == nil {
		home, _ := os.UserHomeDir()
		if root := filepath.Dir(filepath.Dir(filepath.Dir(python))); strings.HasPrefix(root, home+"/") {
			*run = append(*run, "-v", root+":"+root+":ro")
		}
	}
	hermesDir := filepath.Join(homeDir, ".hermes")
	if err := os.MkdirAll(hermesDir, 0o700); err != nil {
		return nil, err
	}
	host := readHermesModelConfig(filepath.Join(hermesHome(), "config.yaml"))
	model := firstNonEmpty(meta.Model, host.Default)
	// Hermes sends the OpenRouter key only to openrouter.ai, so the box
	// names the proxy as a custom endpoint with the placeholder as its key.
	config := fmt.Sprintf("model:\n  provider: custom\n  base_url: http://%s:%d/api/v1\n  api_key: %s\n  api_mode: %s\n", meta.ProxyIP, boxInjectPort, boxClaudePlacehold, firstNonEmpty(host.APIMode, "chat_completions"))
	if model != "" {
		config = "model:\n  default: " + model + "\n" + strings.TrimPrefix(config, "model:\n")
	}
	config += "terminal:\n  backend: local\n  cwd: .\n"
	if err := os.WriteFile(filepath.Join(hermesDir, "config.yaml"), []byte(config), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(hermesDir, ".env"), []byte("OPENROUTER_API_KEY="+boxClaudePlacehold+"\n"), 0o600); err != nil {
		return nil, err
	}
	hermes := []string{filepath.Join(install, "venv", "bin", "python"), filepath.Join(install, "hermes")}
	if prompt == "" {
		return append(hermes, "chat", "--yolo"), nil
	}
	// The final answer goes to a file and to the log; the session ID goes
	// to the log on stderr.
	script := shellQuote(hermes[0]) + " " + shellQuote(hermes[1]) + " chat --query-file " + results + "/task.md -Q --yolo > " + results + "/last.txt; status=$?; cat " + results + "/last.txt; exit $status"
	return []string{"sh", "-c", script}, nil
}

// hermesSessionScript prints, as JSON, the newest session in a Hermes
// state.db: tokens, tool calls, model, cost, and the latest tool call.
const hermesSessionScript = `
import json, sqlite3, sys
try:
    db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True, timeout=1)
    row = db.execute("select id, model, input_tokens, output_tokens, cache_read_tokens, tool_call_count, started_at, ended_at from sessions order by started_at desc limit 1").fetchone()
    if not row:
        print("{}"); sys.exit()
    out = dict(zip(["id", "model", "input", "output", "cached", "tools", "started", "ended"], row))
    cost = db.execute("select sum(coalesce(actual_cost_usd, estimated_cost_usd)), sum(input_tokens), sum(output_tokens), sum(cache_read_tokens), max(model) from session_model_usage where session_id = ?", (row[0],)).fetchone()
    if cost and cost[1]:
        out["cost"], out["input"], out["output"], out["cached"] = cost[0], cost[1], cost[2], cost[3]
        out["model"] = out["model"] or cost[4]
    last = db.execute("select tool_calls from messages where session_id = ? and role = 'assistant' and tool_calls is not null order by id desc limit 1", (row[0],)).fetchone()
    if last:
        out["last_calls"] = last[0]
    out["steps"] = db.execute("select count(*) from messages where session_id = ? and role = 'tool'", (row[0],)).fetchone()[0]
    print(json.dumps(out, default=str))
except Exception as e:
    print(json.dumps({"error": str(e)}))
`

type hermesSession struct {
	ID        string   `json:"id"`
	Model     string   `json:"model"`
	Input     int      `json:"input"`
	Output    int      `json:"output"`
	Cached    int      `json:"cached"`
	Steps     int      `json:"steps"`
	Cost      *float64 `json:"cost"`
	LastCalls *string  `json:"last_calls"`
	Error     string   `json:"error"`
}

// readHermesSession reads the newest session from a box's Hermes home.
func readHermesSession(home string) (hermesSession, bool) {
	var session hermesSession
	db := filepath.Join(home, ".hermes", "state.db")
	if !fileExists(db) {
		return session, false
	}
	out, err := exec.Command("python3", "-c", hermesSessionScript, db).Output()
	if err != nil || json.Unmarshal(out, &session) != nil || session.Error != "" || session.ID == "" {
		return session, false
	}
	return session, true
}

// hermesProgress turns a session into the status line's progress.
func hermesProgress(session hermesSession) agentProgress {
	// Hermes counts input without the cached part; hi's "in" includes it.
	progress := agentProgress{in: session.Input + session.Cached, out: session.Output, cached: session.Cached, steps: session.Steps, model: session.Model}
	if session.LastCalls != nil && *session.LastCalls != "" {
		var calls []struct {
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		}
		if json.Unmarshal([]byte(*session.LastCalls), &calls) == nil && len(calls) > 0 {
			var arguments map[string]any
			json.Unmarshal([]byte(calls[len(calls)-1].Function.Arguments), &arguments)
			progress.step = claudeStep(calls[len(calls)-1].Function.Name, arguments)
		}
	}
	return progress
}
