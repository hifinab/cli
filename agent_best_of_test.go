package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBestOfNeedsACheckAndConfirmation(t *testing.T) {
	project, _, _ := agentTestSetup(t)
	var stdout, stderr bytes.Buffer
	// Without --check, the project needs a make check target.
	if status := runAgent([]string{"best-of", "2", "--yes", "speed it up"}, strings.NewReader(""), &stdout, &stderr); status != 2 || !strings.Contains(stderr.String(), "--check") {
		t.Fatalf("no Makefile: status %d, %q", status, stderr.String())
	}
	os.WriteFile(filepath.Join(project, "Makefile"), []byte("all:\n\techo\n\ncheck: all\n\tgo test ./...\n"), 0o644)
	if !bestOfHasMakeCheck(project) {
		t.Fatal("the Makefile's check target wasn't found")
	}
	// Without a terminal, starting needs --yes; nothing is started.
	stderr.Reset()
	if status := runAgent([]string{"best-of", "2", "speed it up"}, strings.NewReader(""), &stdout, &stderr); status != 1 || !strings.Contains(stderr.String(), "--yes") {
		t.Fatalf("no confirmation: status %d, %q", status, stderr.String())
	}
	if entries, _ := os.ReadDir(bestOfStateFile()); len(entries) != 0 {
		t.Fatalf("a run was saved without confirmation: %v", entries)
	}
	for _, args := range [][]string{{"best-of", "1", "x"}, {"best-of", "9", "x"}, {"best-of", "2", "--name", "n", "x"}, {"best-of", "2", "--max", "10s", "x"}, {"best-of", "frob"}} {
		stderr.Reset()
		if status := runAgent(args, strings.NewReader(""), &stdout, &stderr); status != 2 {
			t.Errorf("%v: status %d, %q", args, status, stderr.String())
		}
	}
}

func TestBestOfRun(t *testing.T) {
	_, calls, exited := agentTestSetup(t)
	var stdout, stderr bytes.Buffer
	args := []string{"best-of", "3", "--agents", "claude,codex", "--check", "pytest -q", "--max", "30m", "--yes", "--detach", "make", "the loader faster"}
	if status := runAgent(args, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s", status, stderr.String())
	}
	run, err := loadBestOf("b-1")
	if err != nil {
		t.Fatal(err)
	}
	if run.Check != "pytest -q" || run.MaxSeconds != 1800 || run.Prompt != "make the loader faster" || len(run.Boxes) != 3 {
		t.Fatalf("run = %+v", run)
	}
	var agents, branches []string
	for _, box := range run.Boxes {
		agents, branches = append(agents, box.Agent), append(branches, box.Branch)
	}
	if strings.Join(agents, ",") != "claude,codex,claude" || strings.Join(branches, ",") != "best-of/b-1/1,best-of/b-1/2,best-of/b-1/3" {
		t.Fatalf("agents %v, branches %v", agents, branches)
	}
	// Every box runs the agent under the limit, then the check, by hi.
	starts := 0
	for _, call := range *calls {
		if call[0] != "run" {
			continue
		}
		starts++
		line := strings.Join(call, " ")
		if !strings.Contains(line, "HI_CHECK=pytest -q") || !strings.Contains(line, `timeout -k 30 1800 "$@"`) || !strings.Contains(line, "kill -9 -1") {
			t.Fatalf("box command: %s", line)
		}
	}
	if starts != 3 {
		t.Fatalf("%d boxes started", starts)
	}

	// The boxes finish. 1 passes with a small change; 2 fails its check;
	// 3 passes with a bigger change that touches the Makefile.
	finish := func(name, check string, files map[string]string) {
		meta, err := loadBoxMeta(name)
		if err != nil {
			t.Fatal(err)
		}
		results := boxStateFile(name, "home", agentResultDir)
		os.WriteFile(filepath.Join(results, "check.json"), []byte(check), 0o600)
		os.WriteFile(filepath.Join(results, "check.log"), []byte("collected 3 items\nFAILED test_loader.py::test_hash\n"), 0o600)
		if meta.Agent == "claude" {
			os.WriteFile(filepath.Join(results, "result.json"), []byte(`{"result":"Done.\n<<<REPORT\n{\"status\":\"complete\"}\nREPORT>>>","total_cost_usd":1.5}`), 0o600)
		} else {
			os.WriteFile(filepath.Join(results, "last.txt"), []byte("Done."), 0o600)
		}
		for path, content := range files {
			os.WriteFile(filepath.Join(meta.Workdir, path), []byte(content), 0o644)
		}
	}
	names := []string{run.Boxes[0].Name, run.Boxes[1].Name, run.Boxes[2].Name}
	finish(names[0], `{"agent_exit":0,"agent_seconds":600,"check_exit":0,"check_seconds":41}`, map[string]string{"loader.py": "a\nb\n"})
	finish(names[1], `{"agent_exit":0,"agent_seconds":500,"check_exit":1,"check_seconds":12}`, map[string]string{"loader.py": "x\n"})
	finish(names[2], `{"agent_exit":0,"agent_seconds":400,"check_exit":0,"check_seconds":39}`, map[string]string{"loader.py": "a\nb\nc\n", "Makefile": "check:\n"})
	*exited = "0"

	stdout.Reset()
	if status := runAgent([]string{"best-of", "show", "b-1"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("show: %d %s", status, stderr.String())
	}
	table := stdout.String()
	rows := map[string]int{}
	for i, line := range strings.Split(table, "\n") {
		for _, name := range names {
			if strings.Contains(line, name) && !strings.Contains(line, "keep") {
				rows[name] = i
			}
		}
	}
	if !(rows[names[0]] < rows[names[2]] && rows[names[2]] < rows[names[1]]) {
		t.Fatalf("order %v in:\n%s", rows, table)
	}
	for _, want := range []string{"✓ 41s", "✗ 12s", "+2 −0 1f", "Makefile", "check: FAILED test_loader.py::test_hash", "$1.50",
		"hi agent best-of keep b-1 " + names[0], "every box passed"} {
		if want == "every box passed" {
			if strings.Contains(table, want) {
				t.Errorf("a failing box still warned that every box passed:\n%s", table)
			}
			continue
		}
		if !strings.Contains(table, want) {
			t.Errorf("table lacks %q:\n%s", want, table)
		}
	}

	// JSON has the ranking too.
	stdout.Reset()
	runAgent([]string{"best-of", "show", "b-1", "--json"}, strings.NewReader(""), &stdout, &stderr)
	var shown bestOfRun
	if err := json.Unmarshal(stdout.Bytes(), &shown); err != nil || shown.Best != names[0] || shown.Boxes[0].Rank != 1 || !shown.Boxes[0].passed() {
		t.Fatalf("json: %v %s", err, stdout.String())
	}

	// Keeping the best removes the others with their branches.
	stdout.Reset()
	if status := runAgent([]string{"best-of", "keep", "b-1", "1", "--yes"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("keep: %d %s", status, stderr.String())
	}
	if !fileExists(boxStateFile(names[0])) || fileExists(boxStateFile(names[1])) || fileExists(boxStateFile(names[2])) {
		t.Fatalf("boxes after keep: %s", stdout.String())
	}
	if boxGit(run.Root, "rev-parse", "--verify", "-q", "refs/heads/best-of/b-1/1") == "" ||
		boxGit(run.Root, "rev-parse", "--verify", "-q", "refs/heads/best-of/b-1/2") != "" {
		t.Fatal("the kept branch should stay and the others go")
	}
	stdout.Reset()
	runAgent([]string{"best-of", "ls"}, strings.NewReader(""), &stdout, &stderr)
	if !strings.Contains(stdout.String(), "kept "+names[0]) {
		t.Fatalf("ls: %s", stdout.String())
	}
	if status := runAgent([]string{"best-of", "keep", "b-1", names[1], "--yes"}, strings.NewReader(""), &stdout, &stderr); status != 1 {
		t.Fatalf("a settled run kept again: %d", status)
	}
}

func TestRankBestOf(t *testing.T) {
	passed := &bestOfCheck{Exit: 0}
	run := bestOfRun{Boxes: []bestOfBox{
		{Name: "failing", State: "finished", AgentStatus: "done", Check: &bestOfCheck{Exit: 2}, Files: 1},
		{Name: "unchanged", State: "finished", AgentStatus: "done", Check: passed},
		{Name: "running", State: "running"},
		{Name: "big", State: "finished", AgentStatus: "done", Check: passed, Files: 5, Added: 200},
		{Name: "small", State: "finished", AgentStatus: "done", Check: passed, Files: 1, Added: 10},
		{Name: "timed-out", State: "finished", AgentStatus: "timed_out", Check: passed, Files: 1},
	}}
	rankBestOf(&run)
	var order []string
	for _, box := range run.Boxes {
		order = append(order, box.Name)
	}
	if strings.Join(order, ",") != "small,big,timed-out,unchanged,running,failing" || run.Best != "small" {
		t.Fatalf("order %v, best %s", order, run.Best)
	}
}

func TestBestOfBoxCommand(t *testing.T) {
	command := bestOfBoxCommand([]string{"sh", "-c", "claude -p"}, 0)
	if command[0] != "sh" || command[3] != "sh" || strings.Join(command[4:], " ") != "sh -c claude -p" || strings.Contains(command[2], "timeout") {
		t.Fatalf("command %q", command)
	}
	if limited := bestOfBoxCommand([]string{"codex"}, 45*time.Minute); !strings.Contains(limited[2], "timeout -k 30 2700") {
		t.Fatalf("limited %q", limited[2])
	}
}
