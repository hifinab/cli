package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// agentBilling says how an agent's use is paid for on this machine, read
// from its own sign-in: a subscription plan, an API key, or OpenRouter. A
// metered agent's dollars are billed; a subscription's are what the same
// tokens would cost at API prices, a measure of usage.
func agentBilling(kind string) (plan string, metered bool) {
	home, _ := os.UserHomeDir()
	switch kind {
	case "claude":
		if fileExists(boxTokenPath()) {
			return "Claude subscription (setup token)", false
		}
		var credentials struct {
			OAuth struct {
				SubscriptionType string `json:"subscriptionType"`
				RateLimitTier    string `json:"rateLimitTier"`
			} `json:"claudeAiOauth"`
		}
		path := filepath.Join(firstNonEmpty(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude")), ".credentials.json")
		if data, err := os.ReadFile(path); err == nil && json.Unmarshal(data, &credentials) == nil && credentials.OAuth.SubscriptionType != "" {
			plan := "Claude " + titleWord(credentials.OAuth.SubscriptionType)
			// A tier such as default_claude_max_20x names the usage level.
			if tier := credentials.OAuth.RateLimitTier; strings.Contains(tier, "_") {
				if last := tier[strings.LastIndex(tier, "_")+1:]; strings.HasSuffix(last, "x") {
					plan += " " + last
				}
			}
			return plan + " subscription", false
		}
		return "Claude subscription", false
	case "codex":
		var auth struct {
			Mode   string `json:"auth_mode"`
			APIKey string `json:"OPENAI_API_KEY"`
			Tokens struct {
				ID string `json:"id_token"`
			} `json:"tokens"`
		}
		data, err := os.ReadFile(codexAuthPath())
		if err != nil || json.Unmarshal(data, &auth) != nil {
			return "", false
		}
		if auth.Mode == "apikey" || auth.Mode == "" && auth.APIKey != "" {
			return "OpenAI API key", true
		}
		if plan := chatGPTPlan(auth.Tokens.ID); plan != "" {
			return "ChatGPT " + titleWord(plan) + " plan", false
		}
		return "ChatGPT plan", false
	case "hermes":
		return "OpenRouter, billed per token", true
	}
	return "", false
}

// chatGPTPlan is the plan claim in Codex's ID token; only that claim is read.
func chatGPTPlan(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return ""
	}
	var claims struct {
		Auth struct {
			Plan string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Auth.Plan
}

func titleWord(word string) string {
	if word == "" {
		return ""
	}
	return strings.ToUpper(word[:1]) + word[1:]
}
