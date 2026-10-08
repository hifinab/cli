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
	if !strings.Contains(run, "exec codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check -o /box/home/.hi-agent/last.txt - < /box/home/.hi-agent/task.md") {
		t.Fatalf("codex command: %s", run)
	}

	// Codex refreshed its sign-in in the box: the machine gets the new one,
	// since the old refresh token no longer works.
	refreshed := `{"tokens":{"refresh_token":"new"},"last_refresh":"2026-10-07T18:00:00Z"}`
	os.WriteFile(boxStateFile("c1", "home", ".codex", "auth.json"), []byte(refreshed), 0o600)

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
	if host, _ := os.ReadFile(codexAuthPath()); string(host) != refreshed {
		t.Fatalf("the machine's Codex sign-in = %s", host)
	}
	if info, _ := os.Stat(codexAuthPath()); info.Mode().Perm() != 0o600 {
		t.Fatalf("auth.json mode %v", info.Mode())
	}
	// An older sign-in from a box doesn't replace a newer one.
	os.WriteFile(boxStateFile("c1", "home", ".codex", "auth.json"), []byte(`{"last_refresh":"2026-10-01T00:00:00Z"}`), 0o600)
	syncCodexAuth(boxMeta{Name: "c1", Agent: "codex"})
	if host, _ := os.ReadFile(codexAuthPath()); string(host) != refreshed {
		t.Fatalf("an older sign-in replaced the machine's: %s", host)
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

func TestAgentTaskFiles(t *testing.T) {
	project, _, _ := agentTestSetup(t)
	os.MkdirAll(filepath.Join(project, "briefs"), 0o755)
	brief := "# GPU prices\n\nCollect them into prices.csv.\n"
	os.WriteFile(filepath.Join(project, "briefs", "Prices.MD"), []byte(brief), 0o644)
	os.WriteFile(filepath.Join(project, "brief.txt"), []byte("from a txt file\n"), 0o644)
	os.WriteFile(filepath.Join(project, "empty.md"), []byte(" \n"), 0o644)
	os.WriteFile(filepath.Join(project, "big.md"), bytes.Repeat([]byte("x"), agentMaxTask+1), 0o644)
	task := func(name string) string {
		data, _ := os.ReadFile(boxStateFile(name, "home", agentResultDir, "task.md"))
		return string(data)
	}
	for _, test := range []struct {
		name, stdin, file, want string
		args                    []string
	}{
		// A single word ending in .md, in any case, is a file; it needn't be committed.
		{"f1", "", filepath.Join(project, "briefs", "Prices.MD"), "# GPU prices\n\nCollect them into prices.csv.\n\nWhen you are finished", []string{"briefs/Prices.MD"}},
		{"f2", "", filepath.Join(project, "brief.txt"), "from a txt file\n\nWhen", []string{"--task-file", "brief.txt"}},
		{"f3", "from stdin\n", "", "from stdin\n\nWhen", []string{"-"}},
		// Two words are a plain task, even with a .md in them.
		{"f4", "", "", "fix the typo in README.md\n\nWhen", []string{"fix", "the typo in README.md"}},
	} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"claude", "--detach", "--json", "--name", test.name}, test.args...)
		if status := runAgent(args, strings.NewReader(test.stdin), &stdout, &stderr); status != 0 {
			t.Fatalf("%v: status %d: %s", test.args, status, stderr.String())
		}
		if got := task(test.name); !strings.HasPrefix(got, test.want) {
			t.Errorf("%v: task %q", test.args, got)
		}
		if meta, _ := loadBoxMeta(test.name); meta.TaskFile != test.file {
			t.Errorf("%v: task file %q, want %q", test.args, meta.TaskFile, test.file)
		}
	}
	// The report says where the task came from.
	report := collectAgentReport(boxEngine{name: "podman", bin: "/usr/bin/podman"}, func() boxMeta { m, _ := loadBoxMeta("f1"); return m }())
	if report.TaskFile != filepath.Join(project, "briefs", "Prices.MD") {
		t.Errorf("report task_file %q", report.TaskFile)
	}

	for _, test := range []struct {
		stdin, want string
		args        []string
	}{
		{"", "there is no task file breif.md", []string{"breif.md"}},
		{"", "empty.md is empty", []string{"empty.md"}},
		{"", "at most 1 MB", []string{"big.md"}},
		{"", "the task on stdin is empty", []string{"-"}},
		{"", "is a folder", []string{"--task-file", "briefs"}},
		{"", "not both", []string{"--task-file", "brief.txt", "and", "words"}},
	} {
		var stdout, stderr bytes.Buffer
		args := append([]string{"claude", "--detach", "--name", "bad"}, test.args...)
		if status := runAgent(args, strings.NewReader(test.stdin), &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), test.want) {
			t.Errorf("%v: status %d, want %q: %s", test.args, status, test.want, stderr.String())
		}
	}
	if _, err := loadBoxMeta("bad"); err == nil {
		t.Fatal("a refused task file made a box")
	}
}

func TestAgentWorksInPlaceOutsideGit(t *testing.T) {
	_, calls, exited := agentTestSetup(t)
	folder := filepath.Join(t.TempDir(), "gpu-prices")
	os.MkdirAll(filepath.Join(folder, "notes"), 0o755)
	os.WriteFile(filepath.Join(folder, "keep.txt"), []byte("keep"), 0o644)
	os.WriteFile(filepath.Join(folder, "edit.txt"), []byte("old"), 0o644)
	os.WriteFile(filepath.Join(folder, "notes", "gone.txt"), []byte("gone"), 0o644)
	t.Chdir(folder)

	var stdout, stderr bytes.Buffer
	if status := runAgent([]string{"claude", "--detach", "--name", "p1", "collect", "prices"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	if !strings.Contains(stdout.String(), folder+" (not a git repository: it works in place)") {
		t.Fatalf("stdout: %s", stdout.String())
	}
	var run string
	for _, call := range *calls {
		if call[0] == "run" {
			run = strings.Join(call, " ")
		}
	}
	if !strings.Contains(run, "-v "+folder+":"+folder+" ") || strings.Contains(run, "worktree") {
		t.Fatalf("run: %s", run)
	}

	// The agent adds, changes, and removes files.
	os.WriteFile(filepath.Join(folder, "prices.csv"), []byte("a,b\n"), 0o644)
	os.WriteFile(filepath.Join(folder, "edit.txt"), []byte("newer"), 0o644)
	os.Remove(filepath.Join(folder, "notes", "gone.txt"))
	os.WriteFile(boxStateFile("p1", "home", agentResultDir, "result.json"), []byte(`{"result":"Wrote prices.csv."}`), 0o600)
	*exited = "0"

	stdout.Reset()
	if status := runAgent([]string{"wait", "p1", "--json"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("wait: %d %s", status, stderr.String())
	}
	var report agentReport
	json.Unmarshal(stdout.Bytes(), &report)
	if report.Folder != folder || report.Branch != "" || strings.Join(report.ChangedFiles, ",") != "edit.txt,notes/gone.txt,prices.csv" {
		t.Fatalf("report = %+v", report)
	}
	stdout.Reset()
	runAgent([]string{"wait", "p1"}, strings.NewReader(""), &stdout, &stderr)
	if !strings.Contains(stdout.String(), "Changed in "+folder+":\n  edit.txt\n  notes/gone.txt\n  prices.csv\n") {
		t.Fatalf("human report: %s", stdout.String())
	}
	stdout.Reset()
	if status := runBox([]string{"diff", "p1"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("diff: %d %s", status, stderr.String())
	}
	for _, want := range []string{"changed  edit.txt", "removed  notes/gone.txt", "added    prices.csv"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("diff lacks %q:\n%s", want, stdout.String())
		}
	}

	// Not in the home folder or above it: the box would get all of it.
	t.Chdir(os.Getenv("HOME"))
	stderr.Reset()
	if status := runAgent([]string{"claude", "--detach", "--name", "p2", "anything"}, strings.NewReader(""), &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), "not in") {
		t.Fatalf("home: %d %s", status, stderr.String())
	}
	t.Chdir(filepath.Dir(os.Getenv("HOME")))
	stderr.Reset()
	if status := runAgent([]string{"claude", "--detach", "--name", "p3", "anything"}, strings.NewReader(""), &stdout, &stderr); status == 0 || !strings.Contains(stderr.String(), "not in") {
		t.Fatalf("above home: %d %s", status, stderr.String())
	}
}

func TestAgentModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	t.Setenv("ANTHROPIC_MODEL", "")
	// Without settings, opus for Claude Code and Codex's own default.
	if got := defaultAgentModel("claude"); got != "opus" {
		t.Fatalf("claude without settings: %q", got)
	}
	if got := defaultAgentModel("codex"); got != "" {
		t.Fatalf("codex without settings: %q", got)
	}
	// With settings, the same model as on this machine.
	writeSkillTestFile(t, filepath.Join(home, ".claude", "settings.json"), `{"model":"opus[1m]"}`, 0o644)
	writeSkillTestFile(t, filepath.Join(home, ".codex", "config.toml"), "model = \"gpt-6.1-sol\"\nmodel_reasoning_effort = \"medium\"\n[profiles.fast]\nmodel = \"other\"\n", 0o644)
	if got := defaultAgentModel("claude"); got != "opus[1m]" {
		t.Fatalf("claude: %q", got)
	}
	if got := defaultAgentModel("codex"); got != "gpt-6.1-sol" {
		t.Fatalf("codex: %q", got)
	}

	front, _, err := splitFrontMatter("---\nmodel: sonnet\n---\nGo.", "brief.md")
	if err != nil || front.Model != "sonnet" {
		t.Fatalf("%+v %v", front, err)
	}
	if _, _, err := splitFrontMatter("---\nmodel: x; rm -rf /\n---\nGo.", "brief.md"); err == nil {
		t.Fatal("a model name with a shell command")
	}
	options := boxOptions{fromBrief: front, model: "haiku"}
	applyFrontMatter("claude", &options, strings.NewReader(""), io.Discard)
	if options.model != "haiku" {
		t.Fatalf("the flag didn't win: %q", options.model)
	}

	// The model reaches each agent's command.
	boxHome := t.TempDir()
	for kind, want := range map[string]string{"claude": "--model 'opus[1m]'", "codex": "-m 'opus[1m]'"} {
		writeSkillTestFile(t, filepath.Join(home, ".codex", "auth.json"), `{}`, 0o600)
		var run []string
		command, err := agentBoxSetup(kind, "Go.", boxMeta{Name: "m", Model: "opus[1m]", Workdir: "/w"}, boxHome, &run, map[string]string{})
		if err != nil {
			t.Logf("%s: %v", kind, err) // the agent isn't installed here
			continue
		}
		if !strings.Contains(strings.Join(command, " "), want) {
			t.Fatalf("%s: %v", kind, command)
		}
	}
}
