package main

import (
	"encoding/base64"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAgentBillingReadsEachSignIn(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	writeSkillTestFile(t, filepath.Join(home, ".claude", ".credentials.json"),
		`{"claudeAiOauth":{"accessToken":"x","subscriptionType":"max","rateLimitTier":"default_claude_max_20x"}}`, 0o600)
	claims := base64.RawURLEncoding.EncodeToString([]byte(`{"https://api.openai.com/auth":{"chatgpt_plan_type":"pro"}}`))
	writeSkillTestFile(t, filepath.Join(home, ".codex", "auth.json"),
		`{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"id_token":"h.`+claims+`.s"}}`, 0o600)
	for kind, want := range map[string]struct {
		plan    string
		metered bool
	}{
		"claude": {"Claude Max 20x subscription", false},
		"codex":  {"ChatGPT Pro plan", false},
		"hermes": {"OpenRouter, billed per token", true},
	} {
		if plan, metered := agentBilling(kind); plan != want.plan || metered != want.metered {
			t.Errorf("%s: %q %v, want %q %v", kind, plan, metered, want.plan, want.metered)
		}
	}
	writeSkillTestFile(t, filepath.Join(home, ".codex", "auth.json"), `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-test"}`, 0o600)
	if plan, metered := agentBilling("codex"); plan != "OpenAI API key" || !metered {
		t.Errorf("codex with a key: %q %v", plan, metered)
	}
}

func TestBestOfAgentsTakeAModelEach(t *testing.T) {
	kinds, models, err := parseBestOfAgents("claude, codex:gpt-6.1-sol,hermes:qwen/qwen3.8-max-0902")
	if err != nil || !reflect.DeepEqual(kinds, []string{"claude", "codex", "hermes"}) ||
		!reflect.DeepEqual(models, []string{"", "gpt-6.1-sol", "qwen/qwen3.8-max-0902"}) {
		t.Fatalf("%v %v %v", kinds, models, err)
	}
	if _, _, err := parseBestOfAgents("hermes:bad model"); err == nil {
		t.Error("a model with a space was taken")
	}
	if _, _, err := parseBestOfAgents("gemini"); err == nil {
		t.Error("an unknown agent was taken")
	}
}
