package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestClaudeProgress(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "projects", "-home-hi-projects-test", "s.jsonl")
	writeSkillTestFile(t, path, `{"type":"user","message":{"content":"go"}}
{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":2,"cache_read_input_tokens":30000,"cache_creation_input_tokens":2000,"output_tokens":10},"content":[{"type":"thinking"}]}}
{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":2,"cache_read_input_tokens":30000,"cache_creation_input_tokens":2000,"output_tokens":400},"content":[{"type":"tool_use","name":"Bash","input":{"command":"python3 -m venv .venv &&\n  pip install yfinance"}}]}}
`, 0o644)
	reader := newAgentProgressReader("claude", home)
	progress := reader.poll()
	if progress.in != 32002 || progress.out != 400 || progress.steps != 1 || progress.step != "Bash: python3 -m venv .venv &&\n  pip install yfinance" {
		t.Fatalf("%+v", progress)
	}

	// Only what was added is read, and a line still being written waits.
	file, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	file.WriteString(`{"type":"assistant","message":{"id":"m2","usage":{"input_tokens":1,"output_tokens":50},"content":[{"type":"tool_use","name":"Write","input":{"file_path":"/home/hi/projects/test/backtest.py"}}]}}` + "\n" + `{"type":"assistant","message":{"id":"m3"`)
	progress = reader.poll()
	if progress.in != 32003 || progress.out != 450 || progress.steps != 2 || progress.step != "Write: backtest.py" {
		t.Fatalf("%+v", progress)
	}
	file.WriteString(`,"content":[{"type":"text","text":"Done"}]}}` + "\n")
	file.Close()
	if progress = reader.poll(); progress.step != "writing" || progress.steps != 2 {
		t.Fatalf("%+v", progress)
	}
}

func TestCodexProgress(t *testing.T) {
	home := t.TempDir()
	writeSkillTestFile(t, filepath.Join(home, ".codex", "sessions", "2026", "10", "08", "rollout-1.jsonl"), `{"type":"session_meta","payload":{}}
{"type":"response_item","payload":{"type":"reasoning"}}
{"type":"response_item","payload":{"type":"custom_tool_call","name":"exec","input":"text( await tools.exec_command({cmd:\"rg -n \\\"sharpe\\\" .\",\"max_output_tokens\":3500}));"}}
{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":17422,"cached_input_tokens":12160,"output_tokens":89}}}}
{"type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"bash\",\"-lc\",\"python3 backtest.py\"]}"}}
`, 0o644)
	progress := newAgentProgressReader("codex", home).poll()
	if progress.in != 17422 || progress.out != 89 || progress.steps != 2 || progress.step != "shell: python3 backtest.py" {
		t.Fatalf("%+v", progress)
	}
}

func TestAgentStatusLine(t *testing.T) {
	// The head bounces between the ends, with a fading trail behind it.
	if got := scannerLevels(0, 6); got[0] != 0 || got[1] != -1 {
		t.Fatalf("frame 0: %v", got)
	}
	if got := scannerLevels(3, 6); got[3] != 0 || got[2] != 1 || got[0] != 3 || got[4] != -1 {
		t.Fatalf("frame 3: %v", got)
	}
	if got := scannerLevels(7, 6); got[3] != 0 || got[4] != 1 || got[5] != 2 || got[2] != -1 {
		t.Fatalf("frame 7, going back: %v", got)
	}

	progress := agentProgress{in: 1_234_567, out: 18_400, steps: 12, step: "Bash: .venv/bin/python backtest.py 2>&1 | tail -20"}
	line := agentStatusLine(0, 252*time.Second, progress, 200)
	for _, want := range []string{"4m12s · 1.2M in · 18k out · 12 steps · Bash: .venv/bin/python backtest.py"} {
		if !strings.Contains(line, want) {
			t.Fatalf("%q has no %q", line, want)
		}
	}
	if width := lipgloss.Width(agentStatusLine(0, time.Hour, progress, 60)); width > 60 {
		t.Fatalf("%d wide in 60", width)
	}
	if line := agentStatusLine(0, 3*time.Second, agentProgress{}, 80); !strings.Contains(line, "3s · starting") {
		t.Fatalf("before the log: %q", line)
	}
}
