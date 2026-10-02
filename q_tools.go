package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	qMaxLookups     = 8
	qToolOutput     = 8 << 10
	qReadLines      = 200
	qListEntries    = 200
	qToolRunTimeout = 10 * time.Second
)

// qLookupTools let the model look before it proposes. All are read-only;
// hi runs them, never the provider.
var qLookupTools = []qToolSpec{
	{
		name:        "list",
		description: "List files and folders with sizes, two levels deep at most. Use it to find names before proposing a command.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Folder to list; default the current folder."},
				"pattern": map[string]any{"type": "string", "description": "Optional glob on names, such as *.md."},
				"depth":   map[string]any{"type": "integer", "description": "1 or 2; default 1."},
			},
		},
	},
	{
		name:        "read",
		description: "Read up to 200 lines of a text file, such as a config, a log, or a Makefile.",
		schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"path":  map[string]any{"type": "string"},
				"from":  map[string]any{"type": "integer", "description": "First line, 1-based; negative counts from the end, so -50 reads the last 50 lines."},
				"lines": map[string]any{"type": "integer", "description": "How many lines; at most 200."},
			},
			"required": []string{"path"},
		},
	},
	{
		name:        "help",
		description: "Show a command's --help, or its man page, for the version installed here. Use it before relying on a flag you're unsure of.",
		schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"command": map[string]any{"type": "string", "description": "A command name such as tar, or a subcommand such as git worktree."}},
			"required":   []string{"command"},
		},
	},
	{
		name:        "which",
		description: "Check whether commands are installed, and where.",
		schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"commands": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
			"required":   []string{"commands"},
		},
	},
	{
		name:        "run",
		description: "Run a read-only command, such as git status, du -sh *, or wc -l, and see its output. Commands that change anything are refused here; propose those instead.",
		schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"command": map[string]any{"type": "string"}},
			"required":   []string{"command"},
		},
	},
}

// qSensitiveNames are never read by the read tool: hi doesn't send them.
var qSensitiveNames = []string{
	".ssh/", "id_rsa", "id_ed25519", "id_ecdsa", ".env", ".netrc", ".pgpass", ".git-credentials",
	".aws/credentials", ".config/hi/", ".kube/config", ".docker/config.json", ".gnupg/", "secrets/",
	".npmrc", ".pypirc", "credentials.json", "token.json", ".vault-token",
}

// runTool runs one lookup tool and returns its result as text for the
// model, showing a one-line note to the user.
func (s *qSession) runTool(ctx context.Context, call qToolCall) string {
	var args struct {
		Path     string   `json:"path"`
		Pattern  string   `json:"pattern"`
		Depth    int      `json:"depth"`
		From     int      `json:"from"`
		Lines    int      `json:"lines"`
		Command  string   `json:"command"`
		Commands []string `json:"commands"`
	}
	if err := json.Unmarshal(call.Args, &args); err != nil {
		return "error: the arguments are not valid JSON: " + err.Error()
	}
	switch call.Name {
	case "list":
		s.note("list " + firstNonEmpty(args.Path, ".") + qIf(args.Pattern != "", " "+args.Pattern))
		return qListTool(firstNonEmpty(args.Path, "."), args.Pattern, args.Depth)
	case "read":
		s.note("read " + args.Path)
		if !s.mayRead(args.Path) {
			return "not read: the user did not allow it, or it holds credentials."
		}
		return qReadTool(args.Path, args.From, args.Lines)
	case "help":
		s.note("help " + args.Command)
		return qHelpTool(ctx, args.Command)
	case "which":
		s.note("which " + strings.Join(args.Commands, " "))
		var b strings.Builder
		for _, name := range args.Commands {
			if path, err := exec.LookPath(name); err == nil {
				fmt.Fprintf(&b, "%s: %s\n", name, path)
			} else {
				fmt.Fprintf(&b, "%s: not installed\n", name)
			}
		}
		return b.String()
	case "run":
		home, _ := os.UserHomeDir()
		assessment := assessCommand(args.Command, home)
		if assessment.risk != qReadOnly {
			s.note("run " + args.Command + " (refused: " + assessment.risk.String() + ")")
			return "not run: only read-only commands run while looking. This one is classed " + assessment.risk.String() + "; propose it instead if it is what the user wants."
		}
		s.note("run " + args.Command)
		return qRunTool(ctx, args.Command)
	}
	return "error: unknown tool " + call.Name
}

// mayRead allows files in and below the current folder, asks for others,
// and refuses credential files.
func (s *qSession) mayRead(path string) bool {
	absolute, err := filepath.Abs(qExpandHome(path))
	if err != nil {
		return false
	}
	for _, name := range qSensitiveNames {
		if strings.Contains(absolute+"/", "/"+name) || strings.HasSuffix(absolute, "/"+strings.TrimSuffix(name, "/")) {
			return false
		}
	}
	folder, _ := os.Getwd()
	if absolute == folder || strings.HasPrefix(absolute, folder+string(filepath.Separator)) {
		return true
	}
	if s.keys == nil {
		return false
	}
	fmt.Fprint(s.stdout, s.style(colorAmber, false).Render(fmt.Sprintf("  Let the model read %s? [y/N] ", absolute)))
	answer, _ := readLine(s.keys)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes"
}

func qExpandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

func qListTool(root, pattern string, depth int) string {
	root = qExpandHome(root)
	if depth < 1 {
		depth = 1
	}
	if depth > 2 {
		depth = 2
	}
	var lines []string
	more := 0
	var walk func(folder, prefix string, level int)
	walk = func(folder, prefix string, level int) {
		entries, err := os.ReadDir(folder)
		if err != nil {
			lines = append(lines, prefix+"error: "+err.Error())
			return
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			name := entry.Name()
			if entry.IsDir() && (name == ".git" || name == "node_modules" || name == ".venv" || name == "__pycache__") && level > 0 {
				continue
			}
			matched := pattern == ""
			if !matched {
				matched, _ = filepath.Match(pattern, name)
			}
			if matched || entry.IsDir() {
				if len(lines) >= qListEntries {
					more++
					continue
				}
				if entry.IsDir() {
					if matched || level+1 < depth {
						lines = append(lines, prefix+name+"/")
					}
				} else if info, err := entry.Info(); err == nil {
					lines = append(lines, fmt.Sprintf("%s%s  %s", prefix, name, qSize(info.Size())))
				}
			}
			if entry.IsDir() && level+1 < depth && name != ".git" {
				walk(filepath.Join(folder, name), prefix+name+"/", level+1)
			}
		}
	}
	walk(root, "", 0)
	if len(lines) == 0 {
		return "nothing matches"
	}
	if more > 0 {
		lines = append(lines, fmt.Sprintf("… and %d more", more))
	}
	return strings.Join(lines, "\n")
}

func qSize(size int64) string {
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(size)/(1<<30))
	case size >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
	case size >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(size)/(1<<10))
	}
	return fmt.Sprintf("%d B", size)
}

func qReadTool(path string, from, count int) string {
	file, err := os.Open(qExpandHome(path))
	if err != nil {
		return "error: " + err.Error()
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.IsDir() {
		return "error: it is a folder; use list"
	}
	if count <= 0 || count > qReadLines {
		count = qReadLines
	}
	var all []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.ContainsRune(line, 0) {
			return "error: it looks like a binary file"
		}
		all = append(all, line)
		if from >= 0 && len(all) > 100000 {
			break
		}
	}
	start := 0
	switch {
	case from < 0:
		start = max(0, len(all)+from)
	case from > 0:
		start = min(from-1, len(all))
	}
	end := min(start+count, len(all))
	var b strings.Builder
	fmt.Fprintf(&b, "lines %d-%d of %d\n", start+1, end, len(all))
	for i := start; i < end; i++ {
		fmt.Fprintf(&b, "%d: %s\n", i+1, all[i])
	}
	return qClip(redactQText(b.String()))
}

func qHelpTool(ctx context.Context, command string) string {
	words := strings.Fields(command)
	if len(words) == 0 {
		return "error: no command"
	}
	for _, word := range words {
		if strings.ContainsAny(word, "/;&|$`<>(){}\\'\"") {
			return "error: give a command name, not a path or a script"
		}
	}
	if _, err := exec.LookPath(words[0]); err != nil {
		return words[0] + " is not installed"
	}
	run := func(name string, args ...string) string {
		ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		process := exec.CommandContext(ctx, name, args...)
		process.Env = append(os.Environ(), "PAGER=cat", "MANPAGER=cat", "MANWIDTH=100", "NO_COLOR=1")
		process.Stdin = nil
		output, _ := process.CombinedOutput()
		return strings.TrimSpace(string(output))
	}
	text := run(words[0], append(words[1:], "--help")...)
	if len(text) < 80 || strings.Contains(strings.ToLower(qFirstLine(text, "")), "unrecognized option") {
		if manual := run("man", "-P", "cat", strings.Join(words, "-")); len(manual) > len(text) {
			text = manual
		}
	}
	if text == "" {
		return "no help found"
	}
	return qClip(text)
}

func qRunTool(ctx context.Context, command string) string {
	ctx, cancel := context.WithTimeout(ctx, qToolRunTimeout)
	defer cancel()
	process := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	if shell := os.Getenv("SHELL"); filepath.Base(shell) == "bash" || filepath.Base(shell) == "zsh" {
		process = exec.CommandContext(ctx, shell, "-c", command)
	}
	var output bytes.Buffer
	process.Stdout = &output
	process.Stderr = &output
	process.Env = append(os.Environ(), "PAGER=cat", "GIT_PAGER=cat", "NO_COLOR=1")
	err := process.Run()
	text := qClip(redactQText(output.String()))
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return text + "\n(stopped after 10 seconds)"
	case err != nil:
		return fmt.Sprintf("%s\n(%v)", text, err)
	case text == "":
		return "(no output)"
	}
	return text
}

func qClip(text string) string {
	if len(text) <= qToolOutput {
		return text
	}
	half := qToolOutput / 2
	return text[:half] + "\n[... cut ...]\n" + text[len(text)-half:]
}

func qIf(condition bool, text string) string {
	if condition {
		return text
	}
	return ""
}
