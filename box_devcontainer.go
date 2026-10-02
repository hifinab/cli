package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// boxDevcontainer is the part of a project's devcontainer.json that hi box
// uses. Fields that run on the host or widen the box are ignored.
type boxDevcontainer struct {
	path              string
	Image             string            `json:"image"`
	Build             boxDevBuild       `json:"build"`
	ContainerEnv      map[string]string `json:"containerEnv"`
	PostCreateCommand json.RawMessage   `json:"postCreateCommand"`
	Customizations    struct {
		Hi boxCustomizations `json:"hi"`
	} `json:"customizations"`
	ignored []string
}

type boxDevBuild struct {
	Dockerfile string `json:"dockerfile"`
	Context    string `json:"context"`
}

// boxCustomizations is customizations.hi.
type boxCustomizations struct {
	Domains []string `json:"domains"`
	Network string   `json:"network"`
	GPU     bool     `json:"gpu"`
}

// boxIgnoredFields would run on the host or widen the box.
var boxIgnoredFields = []string{"initializeCommand", "runArgs", "mounts", "privileged", "capAdd", "securityOpt", "appPort", "forwardPorts", "workspaceMount", "dockerComposeFile"}

// readBoxDevcontainer finds .devcontainer/devcontainer.json or
// .devcontainer.json in the project, and returns nil when there is none.
func readBoxDevcontainer(root string) (*boxDevcontainer, error) {
	for _, candidate := range []string{filepath.Join(root, ".devcontainer", "devcontainer.json"), filepath.Join(root, ".devcontainer.json")} {
		data, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		clean := stripJSONC(data)
		var config boxDevcontainer
		if err := json.Unmarshal(clean, &config); err != nil {
			return nil, fmt.Errorf("%s: %w", candidate, err)
		}
		var all map[string]json.RawMessage
		json.Unmarshal(clean, &all)
		for _, field := range boxIgnoredFields {
			if _, ok := all[field]; ok {
				config.ignored = append(config.ignored, field)
			}
		}
		config.path = candidate
		return &config, nil
	}
	return nil, nil
}

// postCreate is postCreateCommand as a command for sh -c, whether the file
// gives a string or a list.
func (d *boxDevcontainer) postCreate() string {
	if d == nil || len(d.PostCreateCommand) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(d.PostCreateCommand, &text) == nil {
		return text
	}
	var list []string
	if json.Unmarshal(d.PostCreateCommand, &list) == nil {
		quoted := make([]string, len(list))
		for i, word := range list {
			quoted[i] = "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
		}
		return strings.Join(quoted, " ")
	}
	return ""
}

// stripJSONC removes comments and trailing commas, which devcontainer.json
// allows.
func stripJSONC(data []byte) []byte {
	var out []byte
	inString, escaped := false, false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(data) && data[i+1] == '/':
			for i < len(data) && data[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(data) && data[i+1] == '*':
			i += 2
			for i+1 < len(data) && !(data[i] == '*' && data[i+1] == '/') {
				i++
			}
			i++
		case c == '}' || c == ']':
			// Drop a comma before this, skipping whitespace.
			j := len(out) - 1
			for j >= 0 && (out[j] == ' ' || out[j] == '\n' || out[j] == '\t' || out[j] == '\r') {
				j--
			}
			if j >= 0 && out[j] == ',' {
				out = append(out[:j], out[j+1:]...)
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// extra domains, allowed once per project and list

// boxDomainDecision returns whether the user allowed this project's extra
// domains, asking once per list when there is a terminal.
func boxDomainDecision(root string, domains []string, keys io.Reader, stdout io.Writer) bool {
	if len(domains) == 0 {
		return true
	}
	sorted := append([]string(nil), domains...)
	sort.Strings(sorted)
	digest := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	sum := hex.EncodeToString(digest[:])
	path := boxStateFile("domains.json")
	decisions := map[string]struct {
		Sum     string `json:"sum"`
		Allowed bool   `json:"allowed"`
	}{}
	if data, err := os.ReadFile(path); err == nil {
		json.Unmarshal(data, &decisions)
	}
	if entry, ok := decisions[root]; ok && entry.Sum == sum {
		return entry.Allowed
	}
	if keys == nil || !isTerminal(keys) {
		fmt.Fprintf(stdout, "The project asks to reach %s; not allowed until you say so in a terminal.\n", strings.Join(sorted, ", "))
		return false
	}
	fmt.Fprintf(stdout, "This project's devcontainer.json asks for boxes to reach:\n  %s\nAllow them for this project? [y/N] ", strings.Join(sorted, "\n  "))
	answer, _ := readLine(keys)
	answer = strings.ToLower(strings.TrimSpace(answer))
	allowed := answer == "y" || answer == "yes"
	decisions[root] = struct {
		Sum     string `json:"sum"`
		Allowed bool   `json:"allowed"`
	}{sum, allowed}
	data, _ := json.MarshalIndent(decisions, "", "  ")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, append(data, '\n'), 0o600)
	return allowed
}
