package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSplitAgentReport(t *testing.T) {
	text, report := splitAgentReport("Fixed it.\n\n<<<REPORT\n{\"status\": \"complete\",\n \"tests\": \"pass\"}\nREPORT>>>\n")
	if text != "Fixed it." || string(report) != `{"status":"complete","tests":"pass"}` {
		t.Fatalf("got %q, %s", text, report)
	}
	// The last block counts; one that isn't JSON stays in the text.
	text, report = splitAgentReport("<<<REPORT\n{}\nREPORT>>>\nmore\n<<<REPORT\n{\"status\":\"partial\"}\nREPORT>>>")
	if text != "<<<REPORT\n{}\nREPORT>>>\nmore" || string(report) != `{"status":"partial"}` {
		t.Fatalf("got %q, %s", text, report)
	}
	text, report = splitAgentReport("Done.\n<<<REPORT\nnot json\nREPORT>>>")
	if !strings.Contains(text, "not json") || string(report) != "null" {
		t.Fatalf("got %q, %s", text, report)
	}
	if text, report = splitAgentReport("  plain\n"); text != "plain" || string(report) != "null" {
		t.Fatalf("got %q, %s", text, report)
	}
}

func TestBoxPointsAgentsToHiAgent(t *testing.T) {
	for _, args := range [][]string{{"claude", "fix it"}, {"codex"}, {"token", "claude"}} {
		var stdout, stderr bytes.Buffer
		if status := runBox(args, strings.NewReader(""), &stdout, &stderr); status != 2 || !strings.Contains(stderr.String(), "hi agent") {
			t.Errorf("hi box %v: status %d, %q", args, status, stderr.String())
		}
	}
}

// agentTestSetup makes a home folder with Claude Code and Codex signed in,
// a git project to work in, and a fake container engine whose containers
// report exitedWith once started.
func agentTestSetup(t *testing.T) (project string, calls *[][]string, exited *string) {
	root := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CODEX_HOME", "")
	home := filepath.Join(root, "home")
	os.MkdirAll(filepath.Join(home, ".claude"), 0o700)
	os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"real"}}`), 0o600)
	os.MkdirAll(filepath.Join(home, ".codex"), 0o700)
	os.WriteFile(filepath.Join(home, ".codex", "auth.json"), []byte(`{"tokens":{}}`), 0o600)
	os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755)
	os.MkdirAll(filepath.Join(home, ".local", "codex", "bin"), 0o755)
	os.WriteFile(filepath.Join(home, ".local", "bin", "claude"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(home, ".local", "codex", "bin", "codex"), []byte("#!/bin/sh\n"), 0o755)
	os.Symlink(filepath.Join(home, ".local", "codex", "bin", "codex"), filepath.Join(home, ".local", "bin", "codex"))
	project = filepath.Join(root, "project")
	os.MkdirAll(project, 0o755)
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", project}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git: %s", out)
		}
	}
	t.Chdir(project)

	calls = &[][]string{}
	exitStatus := ""
	exited = &exitStatus
	started := map[string]bool{}
	previousCommand, previousLook := boxCommand, boxLookPath
	boxCommand = func(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
		if name == "git" {
			return previousCommand(stdin, stdout, stderr, name, args...)
		}
		*calls = append(*calls, args)
		switch {
		case len(args) > 1 && args[0] == "image" && args[1] == "inspect":
			return nil // the base image exists
		case args[0] == "run":
			for i, arg := range args {
				if arg == "--name" {
					started[args[i+1]] = true
				}
			}
		case len(args) > 1 && args[0] == "container" && args[1] == "inspect":
			container := args[len(args)-1]
			if !started[container] || *exited == "" {
				return fmt.Errorf("no such container")
			}
			if strings.Contains(strings.Join(args, " "), "ExitCode") {
				fmt.Fprintln(stdout, *exited)
			} else {
				fmt.Fprintln(stdout, "exited")
			}
		case args[0] == "logs":
			fmt.Fprintln(stderr, "session id: 019a-codex")
		}
		return nil
	}
	boxLookPath = func(name string) (string, error) {
		if name == "podman" {
			return "/usr/bin/podman", nil
		}
		return "", exec.ErrNotFound
	}
	t.Cleanup(func() { boxCommand, boxLookPath = previousCommand, previousLook })
	return project, calls, exited
}

func TestAgentWaitReport(t *testing.T) {
	_, _, exited := agentTestSetup(t)
	var stdout, stderr bytes.Buffer
	// Without a name, Claude Code is the first agent ready.
	if status := runAgent([]string{"--detach", "--json", "--name", "a1", "add a test"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	var started agentReport
	if err := json.Unmarshal(stdout.Bytes(), &started); err != nil || started.Status != "running" || started.Agent != "claude" || started.Branch != "hi-box/a1" {
		t.Fatalf("detached report %q: %v", stdout.String(), err)
	}

	// The agent finishes: a result file, a commit, and a new file.
	meta, err := loadBoxMeta("a1")
	if err != nil {
		t.Fatal(err)
	}
	result := map[string]any{
		"result":     "Added the test.\n\n<<<REPORT\n{\"status\":\"complete\",\"tests\":\"pass\",\"follow_ups\":[]}\nREPORT>>>",
		"session_id": "sess-1", "is_error": false,
		"usage": map[string]int{"input_tokens": 10, "cache_read_input_tokens": 90, "output_tokens": 5},
	}
	data, _ := json.Marshal(result)
	os.WriteFile(boxStateFile("a1", "home", agentResultDir, "result.json"), data, 0o600)
	os.WriteFile(filepath.Join(meta.Workdir, "test_x.py"), []byte("x"), 0o644)
	*exited = "0"

	stdout.Reset()
	if status := runAgent([]string{"wait", "a1", "--json"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("wait status %d: %s", status, stderr.String())
	}
	var report agentReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("%v: %s", err, stdout.String())
	}
	if report.Status != "done" || report.Text != "Added the test." || report.SessionID != "sess-1" ||
		compactJSON(report.Report) != `{"status":"complete","tests":"pass","follow_ups":[]}` ||
		strings.Join(report.ChangedFiles, ",") != "test_x.py" || report.Tokens == nil || report.Tokens.Input != 100 || len(report.Warnings) != 0 {
		t.Fatalf("report = %+v", report)
	}

	// A non-zero exit fails, with exit status 1.
	*exited = "1"
	stdout.Reset()
	if status := runAgent([]string{"wait", "a1"}, strings.NewReader(""), &stdout, &stderr); status != 1 {
		t.Fatalf("failed run: status %d", status)
	}
	if !strings.Contains(stdout.String(), "Added the test.") || !strings.Contains(stdout.String(), "test_x.py") {
		t.Fatalf("human report: %s", stdout.String())
	}
}

func TestAgentCodex(t *testing.T) {
	_, calls, exited := agentTestSetup(t)
	var stdout, stderr bytes.Buffer
	if status := runAgent([]string{"codex", "--detach", "--network", "locked", "--name", "c1", "review", "it"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	allow, _ := os.ReadFile(boxStateFile("c1", "allow"))
	if !strings.Contains(string(allow), "chatgpt.com") || strings.Contains(string(allow), "pypi.org") {
		t.Fatalf("locked codex allowlist: %q", allow)
	}
	var run string
	for _, call := range *calls {
		if call[0] == "run" {
			run = strings.Join(call, " ")
		}
	}
	if !strings.Contains(run, "codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check -o /box/home/.hi-agent/last.txt review it") {
		t.Fatalf("codex command: %s", run)
	}

	// No report block is a warning, and the session ID comes from the log.
	os.WriteFile(boxStateFile("c1", "home", agentResultDir, "last.txt"), []byte("Looks fine.\n"), 0o600)
	*exited = "0"
	stdout.Reset()
	if status := runAgent([]string{"wait", "c1", "--json"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("wait: %d %s", status, stderr.String())
	}
	var report agentReport
	json.Unmarshal(stdout.Bytes(), &report)
	if report.Status != "done" || report.Text != "Looks fine." || report.SessionID != "019a-codex" || len(report.Warnings) != 1 || len(report.ChangedFiles) != 0 {
		t.Fatalf("report = %+v", report)
	}
}

func TestAgentUsage(t *testing.T) {
	agentTestSetup(t)
	for _, args := range [][]string{{"--json"}, {"claude", "--detach"}, {"--detach"}, {"wait"}, {"token", "codex"}} {
		var stdout, stderr bytes.Buffer
		if status := runAgent(args, strings.NewReader(""), &stdout, &stderr); status != 2 {
			t.Errorf("%v: status %d, %s", args, status, stderr.String())
		}
	}
}

func compactJSON(data json.RawMessage) string {
	var out bytes.Buffer
	json.Compact(&out, data)
	return out.String()
}
