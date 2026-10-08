package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// agentProgress is what an agent's own session log says so far: tokens
// in (with cached ones) and out, the tool calls it made, and the latest.
type agentProgress struct {
	in, out, cached int
	steps           int
	step            string
	model           string
}

// agentProgressReader follows the newest session log in a box's home
// folder: Claude Code's transcript or Codex's rollout. Both are JSON lines
// that grow while the agent works, so it reads only what was added.
type agentProgressReader struct {
	kind   string
	home   string
	path   string
	offset int64
	carry  []byte
	// Claude Code writes one line per content block, each with its
	// message's usage, so usage is kept per message.
	usage map[string][3]int
	agentProgress
}

func newAgentProgressReader(kind, home string) *agentProgressReader {
	return &agentProgressReader{kind: kind, home: home, usage: map[string][3]int{}}
}

func (r *agentProgressReader) pattern() string {
	if r.kind == "claude" {
		return filepath.Join(r.home, ".claude", "projects", "*", "*.jsonl")
	}
	return filepath.Join(r.home, ".codex", "sessions", "*", "*", "*", "rollout-*.jsonl")
}

// poll reads what the log gained since the last call.
func (r *agentProgressReader) poll() agentProgress {
	if r.kind == "hermes" {
		if session, ok := readHermesSession(r.home); ok {
			r.agentProgress = hermesProgress(session)
		}
		return r.agentProgress
	}
	newest, newestTime := "", time.Time{}
	matches, _ := filepath.Glob(r.pattern())
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && info.ModTime().After(newestTime) {
			newest, newestTime = match, info.ModTime()
		}
	}
	if newest == "" {
		return r.agentProgress
	}
	if newest != r.path {
		*r = agentProgressReader{kind: r.kind, home: r.home, path: newest, usage: map[string][3]int{}}
	}
	file, err := os.Open(r.path)
	if err != nil {
		return r.agentProgress
	}
	defer file.Close()
	if _, err := file.Seek(r.offset, io.SeekStart); err != nil {
		return r.agentProgress
	}
	data, _ := io.ReadAll(file)
	r.offset += int64(len(data))
	data = append(r.carry, data...)
	last := strings.LastIndexByte(string(data), '\n')
	if last < 0 {
		r.carry = data
		return r.agentProgress
	}
	r.carry = append([]byte(nil), data[last+1:]...)
	scanner := bufio.NewScanner(strings.NewReader(string(data[:last])))
	scanner.Buffer(make([]byte, 1<<20), 64<<20)
	for scanner.Scan() {
		if r.kind == "claude" {
			r.claudeLine(scanner.Bytes())
		} else {
			r.codexLine(scanner.Bytes())
		}
	}
	if r.kind == "claude" {
		r.in, r.out, r.cached = 0, 0, 0
		for _, usage := range r.usage {
			r.in += usage[0]
			r.out += usage[1]
			r.cached += usage[2]
		}
	}
	return r.agentProgress
}

func (r *agentProgressReader) claudeLine(line []byte) {
	var entry struct {
		Type    string `json:"type"`
		Message struct {
			ID    string `json:"id"`
			Model string `json:"model"`
			Usage *struct {
				Input       int `json:"input_tokens"`
				CacheRead   int `json:"cache_read_input_tokens"`
				CacheCreate int `json:"cache_creation_input_tokens"`
				Output      int `json:"output_tokens"`
			} `json:"usage"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(line, &entry) != nil || entry.Type != "assistant" {
		return
	}
	if usage := entry.Message.Usage; usage != nil && entry.Message.ID != "" {
		r.usage[entry.Message.ID] = [3]int{usage.Input + usage.CacheRead + usage.CacheCreate, usage.Output, usage.CacheRead}
	}
	if model := entry.Message.Model; model != "" && !strings.HasPrefix(model, "<") {
		r.model = model
	}
	var blocks []struct {
		Type  string         `json:"type"`
		Name  string         `json:"name"`
		Input map[string]any `json:"input"`
	}
	json.Unmarshal(entry.Message.Content, &blocks)
	for _, block := range blocks {
		switch block.Type {
		case "tool_use":
			r.steps++
			r.step = claudeStep(block.Name, block.Input)
		case "thinking":
			r.step = "thinking"
		case "text":
			r.step = "writing"
		}
	}
}

// claudeStep names a tool call with the part of its input that says most.
func claudeStep(name string, input map[string]any) string {
	for _, key := range []string{"command", "query", "url", "file_path", "pattern", "description", "prompt", "path"} {
		if value, ok := input[key].(string); ok && value != "" {
			if key == "file_path" || key == "path" {
				value = filepath.Base(value)
			}
			return name + ": " + value
		}
	}
	return name
}

var codexCmd = regexp.MustCompile(`\bcmd\s*:\s*"((?:[^"\\]|\\.)*)"`)

func (r *agentProgressReader) codexLine(line []byte) {
	var entry struct {
		Type    string `json:"type"`
		Payload struct {
			Type      string `json:"type"`
			Model     string `json:"model"`
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
			Input     string `json:"input"`
			Action    struct {
				Command []string `json:"command"`
				Query   string   `json:"query"`
			} `json:"action"`
			Info *struct {
				Total struct {
					Input  int `json:"input_tokens"`
					Cached int `json:"cached_input_tokens"`
					Output int `json:"output_tokens"`
				} `json:"total_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &entry) != nil {
		return
	}
	payload := entry.Payload
	switch {
	case entry.Type == "turn_context" && payload.Model != "":
		r.model = payload.Model
	case entry.Type == "event_msg" && payload.Type == "token_count" && payload.Info != nil:
		r.in, r.out, r.cached = payload.Info.Total.Input, payload.Info.Total.Output, payload.Info.Total.Cached
	case entry.Type == "response_item" && payload.Type == "function_call":
		r.steps++
		var arguments map[string]any
		json.Unmarshal([]byte(payload.Arguments), &arguments)
		r.step = claudeStep(payload.Name, arguments)
		if command, ok := arguments["command"].([]any); ok && len(command) > 0 {
			r.step = payload.Name + ": " + fmt.Sprint(command[len(command)-1])
		}
	case entry.Type == "response_item" && payload.Type == "custom_tool_call":
		r.steps++
		r.step = payload.Name
		if match := codexCmd.FindStringSubmatch(payload.Input); match != nil {
			var command string
			if json.Unmarshal([]byte(`"`+match[1]+`"`), &command) != nil {
				command = match[1]
			}
			r.step = payload.Name + ": " + command
		}
	case entry.Type == "response_item" && payload.Type == "local_shell_call":
		r.steps++
		r.step = "shell: " + strings.Join(payload.Action.Command, " ")
	case entry.Type == "response_item" && payload.Type == "web_search_call":
		r.steps++
		r.step = "web search: " + payload.Action.Query
	case entry.Type == "response_item" && payload.Type == "reasoning":
		r.step = "thinking"
	}
}

// agentStatusLine is one line for a terminal of the given width: a scanner,
// the time, tokens, steps, and the latest step.
func agentStatusLine(frame int, elapsed time.Duration, progress agentProgress, width int) string {
	dim := lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#6b7280", Dark: "#8b8f98"})
	parts := []string{formatAgentElapsed(elapsed)}
	if progress.in+progress.out > 0 {
		parts = append(parts, formatTokenCount(progress.in)+" in", formatTokenCount(progress.out)+" out")
	}
	if progress.steps > 0 {
		parts = append(parts, plural(progress.steps, "step"))
	}
	text := strings.Join(parts, " · ")
	step := strings.Join(strings.Fields(progress.step), " ")
	if step == "" {
		step = "starting"
	}
	const scannerWidth = 12
	room := width - scannerWidth - 2 - len([]rune(text)) - 3
	if room >= 8 {
		if runes := []rune(step); len(runes) > room {
			step = string(runes[:room-1]) + "…"
		}
		text += " · " + step
	}
	return knightRider(frame, scannerWidth) + "  " + dim.Render(text)
}

// scannerLevels is how bright each cell of the scanner is on a frame: 0 is
// the head, larger numbers fade behind it, and -1 is dark.
func scannerLevels(frame, width int) []int {
	levels := make([]int, width)
	period := 2 * (width - 1)
	position := frame % period
	head, step := position, -1 // moving right, the trail is to the left
	if position >= width {
		head, step = period-position, 1
	}
	for i := range levels {
		levels[i] = -1
	}
	for distance := 0; distance < 4; distance++ {
		if cell := head + distance*step; cell >= 0 && cell < width {
			levels[cell] = distance
		}
	}
	return levels
}

var scannerColors = []lipgloss.Color{"#ff2b2b", "#c21717", "#7d0e0e", "#4d0909"}

func knightRider(frame, width int) string {
	var b strings.Builder
	off := lipgloss.NewStyle().Foreground(lipgloss.Color("#2e0b0b"))
	for _, level := range scannerLevels(frame, width) {
		if level < 0 {
			b.WriteString(off.Render("▬"))
		} else {
			b.WriteString(lipgloss.NewStyle().Foreground(scannerColors[level]).Render("▬"))
		}
	}
	return b.String()
}

func formatTokenCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 10_000:
		return fmt.Sprintf("%dk", n/1000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

func formatAgentElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// showAgentStatus draws the status line on w until stop is closed, then
// clears it. It draws nothing unless w is a terminal.
func showAgentStatus(w io.Writer, meta boxMeta, stop <-chan struct{}) (done <-chan struct{}) {
	finished := make(chan struct{})
	file, ok := w.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		close(finished)
		return finished
	}
	reader := newAgentProgressReader(meta.Agent, boxStateFile(meta.Name, "home"))
	started := meta.Created
	if started.IsZero() {
		started = time.Now()
	}
	go func() {
		defer close(finished)
		fmt.Fprint(file, "\x1b[?25l")
		defer fmt.Fprint(file, "\r\x1b[2K\x1b[?25h")
		progress := reader.poll()
		lastPoll := time.Now()
		ticker := time.NewTicker(90 * time.Millisecond)
		defer ticker.Stop()
		for frame := 0; ; frame++ {
			if time.Since(lastPoll) >= time.Second {
				progress, lastPoll = reader.poll(), time.Now()
			}
			width, _, err := term.GetSize(int(file.Fd()))
			if err != nil || width <= 0 {
				width = 80
			}
			fmt.Fprint(file, "\r\x1b[2K"+agentStatusLine(frame, time.Since(started), progress, width-1))
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	return finished
}
