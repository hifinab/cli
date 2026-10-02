package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	qListLimit    = 50
	qHistoryLines = 20
	qPipeLimit    = 32 << 10
	qProbeTimeout = 200 * time.Millisecond
)

// qContextTools are the installed commands worth telling the model about, so
// it proposes rg over grep -r when rg is there.
var qContextTools = []string{
	"rg", "fd", "fdfind", "jq", "yq", "git", "gh", "docker", "podman", "uv", "python3",
	"node", "npm", "go", "cargo", "make", "tmux", "rsync", "curl", "wget", "ffmpeg",
	"convert", "magick", "trash", "trash-put", "zip", "unzip", "7z", "tar", "fzf",
	"bat", "eza", "sqlite3", "psql", "kubectl", "hi",
}

// qContext gathers what the model sees besides the prompt. Every part is
// small, local, and redacted; nothing here contacts the network.
type qContext struct {
	system  string
	shell   string
	gnu     bool
	folder  string
	git     string
	listing []string
	total   int
	history []string
	tools   []string
	piped   string
}

func gatherQContext(piped string) qContext {
	c := qContext{
		system: qSystemName(),
		shell:  qShellName(),
		piped:  redactQText(piped),
	}
	c.gnu = qProbe("ls", "--version") != ""
	c.folder, _ = os.Getwd()
	c.git = qGitSummary()
	c.listing, c.total = qListing(".")
	c.history = qRecentHistory(c.shell, qHistoryLines)
	for _, tool := range qContextTools {
		if _, err := exec.LookPath(tool); err == nil {
			c.tools = append(c.tools, tool)
		}
	}
	return c
}

func (c qContext) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "system: %s\n", c.system)
	core := "BSD"
	if c.gnu {
		core = "GNU"
	}
	fmt.Fprintf(&b, "shell: %s (%s core tools)\n", c.shell, core)
	fmt.Fprintf(&b, "current folder: %s\n", c.folder)
	if c.git != "" {
		fmt.Fprintf(&b, "git: %s\n", c.git)
	}
	if c.total == 0 {
		b.WriteString("files here: none\n")
	} else {
		more := ""
		if c.total > len(c.listing) {
			more = fmt.Sprintf(" (first %d of %d)", len(c.listing), c.total)
		}
		fmt.Fprintf(&b, "files here%s: %s\n", more, strings.Join(c.listing, "  "))
	}
	if len(c.tools) > 0 {
		fmt.Fprintf(&b, "installed: %s\n", strings.Join(c.tools, " "))
	}
	if len(c.history) > 0 {
		b.WriteString("recent commands, oldest first:\n")
		for _, line := range c.history {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	if c.piped != "" {
		b.WriteString("piped input:\n")
		b.WriteString(c.piped)
		if !strings.HasSuffix(c.piped, "\n") {
			b.WriteString("\n")
		}
	}
	return b.String()
}

func qSystemName() string {
	name := runtime.GOOS
	if data, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if value, ok := strings.CutPrefix(line, "PRETTY_NAME="); ok {
				name = strings.Trim(value, `"`)
				break
			}
		}
	} else if runtime.GOOS == "darwin" {
		if version := qProbe("sw_vers", "-productVersion"); version != "" {
			name = "macOS " + version
		}
	}
	return fmt.Sprintf("%s (%s/%s)", name, runtime.GOOS, runtime.GOARCH)
}

func qShellName() string {
	shell := filepath.Base(os.Getenv("SHELL"))
	if shell == "" || shell == "." {
		return "sh"
	}
	return shell
}

// qProbe runs a quick local command and returns its first line, or "" when
// it fails or takes too long.
func qProbe(name string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), qProbeTimeout)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(output)), "\n")
	return line
}

func qGitSummary() string {
	root := qProbe("git", "rev-parse", "--show-toplevel")
	if root == "" {
		return ""
	}
	branch := qProbe("git", "branch", "--show-current")
	if branch == "" {
		branch = "detached HEAD"
	}
	ctx, cancel := context.WithTimeout(context.Background(), qProbeTimeout)
	defer cancel()
	state := "state unknown"
	if output, err := exec.CommandContext(ctx, "git", "status", "--porcelain", "--untracked-files=normal").Output(); err == nil {
		if changed := strings.Count(string(output), "\n"); changed == 0 {
			state = "clean"
		} else {
			state = fmt.Sprintf("%d changed or untracked files", changed)
		}
	}
	return fmt.Sprintf("branch %s, %s, repository root %s", branch, state, root)
}

// qListing returns names in a folder, folders marked with /, hidden files
// last, and the total count.
func qListing(folder string) ([]string, int) {
	entries, err := os.ReadDir(folder)
	if err != nil {
		return nil, 0
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		} else if entry.Type()&os.ModeSymlink != 0 {
			name += "@"
		}
		names = append(names, name)
	}
	sort.SliceStable(names, func(i, j int) bool {
		hiddenI, hiddenJ := strings.HasPrefix(names[i], "."), strings.HasPrefix(names[j], ".")
		if hiddenI != hiddenJ {
			return hiddenJ
		}
		return false
	})
	if len(names) > qListLimit {
		return names[:qListLimit], len(names)
	}
	return names, len(names)
}

// qRecentHistory reads the end of the shell's history file. A separate
// process can't see the history the shell holds in memory, so this can lag
// until shell integration (v0.20.0) writes it on each prompt.
func qRecentHistory(shell string, count int) []string {
	path := os.Getenv("HISTFILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil
		}
		switch shell {
		case "zsh":
			path = filepath.Join(home, ".zsh_history")
		case "fish":
			path = filepath.Join(home, ".local", "share", "fish", "fish_history")
		default:
			path = filepath.Join(home, ".bash_history")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	if info, err := file.Stat(); err == nil && info.Size() > 64<<10 {
		file.Seek(-64<<10, io.SeekEnd)
	}
	var lines []string
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 64<<10)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, ": ") && strings.Contains(line, ";"):
			// zsh extended history: ": 1712345678:0;command"
			_, line, _ = strings.Cut(line, ";")
		case strings.HasPrefix(line, "- cmd: "):
			line = strings.TrimPrefix(line, "- cmd: ")
		case strings.HasPrefix(line, "#") && len(line) > 1 && line[1] >= '0' && line[1] <= '9':
			continue // bash HISTTIMEFORMAT stamps
		case strings.HasPrefix(line, "  when: ") || strings.HasPrefix(line, "  paths:"):
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "hi q") {
			continue
		}
		lower := strings.ToLower(line)
		if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "passwd") {
			continue
		}
		lines = append(lines, redactQText(line))
	}
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return lines
}

// readQPipe keeps the start and end of piped input, where the command and
// its error usually are.
func readQPipe(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 4*qPipeLimit))
	if len(data) <= qPipeLimit {
		return string(data)
	}
	half := qPipeLimit / 2
	return string(data[:half]) + "\n[... cut ...]\n" + string(data[len(data)-half:])
}

var qRedactions = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z ]*PRIVATE KEY-----|$)`), "[private key removed]"},
	{regexp.MustCompile(`(?i)\b(export\s+|set\s+-x\s+|setenv\s+)?([A-Z0-9_]*(KEY|TOKEN|SECRET|PASS|PASSWORD|PWD|AUTH|CREDENTIALS?)[A-Z0-9_]*)=("[^"]*"|'[^']*'|\S+)`), "$1$2=[removed]"},
	{regexp.MustCompile(`(?i)(--password|--pass|--token|--api-key|--apikey|--secret|--key|--auth)(=|\s+)("[^"]*"|'[^']*'|\S+)`), "$1$2[removed]"},
	{regexp.MustCompile(`(?i)(authorization|x-api-key|api-key|cookie)\s*:\s*[^'"\n]+`), "$1: [removed]"},
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s:@]+:[^/\s@]+@`), "$1[removed]@"},
	{regexp.MustCompile(`\b(sk-(ant-|proj-)?[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|hf_[A-Za-z0-9]{20,}|xox[abpors]-[A-Za-z0-9-]{10,}|xapp-[A-Za-z0-9-]{10,}|AKIA[0-9A-Z]{16}|AIza[0-9A-Za-z_-]{30,}|glpat-[A-Za-z0-9_-]{20,}|rpa_[A-Za-z0-9]{20,})\b`), "[removed]"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`), "[removed]"},
}

// redactQText removes the values of things that look like credentials.
func redactQText(text string) string {
	for _, rule := range qRedactions {
		text = rule.pattern.ReplaceAllString(text, rule.replace)
	}
	return text
}
