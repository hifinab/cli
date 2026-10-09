package main

import (
	"bytes"
	"encoding/json"
	"io"
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
	stderr.Reset()
	if status := runAgent([]string{"best-of", "2", "--agents", "claude,codex,claude", "--yes", "x"}, strings.NewReader(""), &stdout, &stderr); status != 2 || !strings.Contains(stderr.String(), "3 agents for 2 boxes") {
		t.Errorf("more agents than boxes: status %d, %q", status, stderr.String())
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
	run := bestOfRun{Check: "make check", Boxes: []bestOfBox{
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
	command := bestOfBoxCommand([]string{"sh", "-c", "claude -p"}, 0, false)
	if command[0] != "sh" || command[3] != "sh" || strings.Join(command[4:], " ") != "sh -c claude -p" || strings.Contains(command[2], "timeout -k 30") {
		t.Fatalf("command %q", command)
	}
	if limited := bestOfBoxCommand([]string{"codex"}, 45*time.Minute, false); !strings.Contains(limited[2], "timeout -k 30 2700") || strings.Contains(limited[2], "/box/turn") {
		t.Fatalf("limited %q", limited[2])
	}
	// With turns, the check and score wait for hi; the snapshot is taken first.
	turned := bestOfBoxCommand([]string{"codex"}, 0, true)[2]
	if strings.Index(turned, "write-tree") > strings.Index(turned, "/box/turn/go") || strings.Index(turned, "/box/turn/go") > strings.Index(turned, "HI_CHECK") {
		t.Fatalf("turned %q", turned)
	}
}

func TestBestOfLoopFlags(t *testing.T) {
	for _, flags := range []bestOfFlags{
		{rounds: "3", roundsGiven: true},        // --rounds without --score
		{score: "x"},                            // no direction
		{score: "x", lower: true, higher: true}, // both
		{score: "x", lower: true, rounds: "0x", roundsGiven: true},
		{score: "x", lower: true, within: "10s"},
		{score: "x", lower: true, budget: "-3"},
		{then: "make results"}, // --then without --score
	} {
		if _, err := parseBestOfLoop(flags); err == nil {
			t.Errorf("%+v was accepted", flags)
		}
	}
	loop, err := parseBestOfLoop(bestOfFlags{score: "x", higher: true, rounds: "forever", roundsGiven: true, budget: "$20", patience: "5", minGain: "0.01"})
	if err != nil || loop.Lower || loop.Rounds != 0 || loop.Budget != 20 || loop.Patience != 5 || loop.MinGain != 0.01 {
		t.Fatalf("%+v %v", loop, err)
	}
	// On resume, limits count from where the run is.
	loop.applyLimits(bestOfFlags{rounds: "10", roundsGiven: true, budget: "5"}, 7, 12.5)
	if loop.Rounds != 17 || loop.Budget != 17.5 {
		t.Fatalf("resumed %+v", loop)
	}
	if outside := bestOfOutside([]string{"train.py", "src/", "*.md"}, []string{"train.py", "src/a/b.py", "README.md", "prepare.py"}); strings.Join(outside, ",") != "prepare.py" {
		t.Fatalf("outside %v", outside)
	}
	if value, ok := lastNumber("step 953\nval_bpb:          0.997900\n"); !ok || value != 0.9979 {
		t.Fatalf("last number %v %v", value, ok)
	}
}

func TestBestOfRounds(t *testing.T) {
	project, calls, exited := agentTestSetup(t)
	os.WriteFile(filepath.Join(project, "train.py"), []byte("lr = 0.01\n"), 0o644)
	os.WriteFile(filepath.Join(project, "prepare.py"), []byte("data\n"), 0o644)
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "train"}} {
		boxGit(project, args...)
	}
	*exited = "0"
	bestOfPoll = time.Millisecond
	spawned := ""
	previousSpawn := bestOfSpawn
	bestOfSpawn = func(id string) error { spawned = id; return nil }
	t.Cleanup(func() { bestOfPoll, bestOfSpawn = 3*time.Second, previousSpawn })

	// The fake engine plays each box: the baseline scores 1.0; in round 1
	// box 1 reaches 0.9 and box 2 edits prepare.py for 0.5; in round 2
	// nothing beats 0.9.
	scores := map[string]string{"r1-1": "0.9", "r1-2": "0.5", "r2-1": "0.95", "r2-2": "0.92"}
	files := map[string][2]string{"r1-1": {"train.py", "lr = 0.04\n"}, "r1-2": {"prepare.py", "cheat\n"},
		"r2-1": {"train.py", "lr = 0.05\n"}, "r2-2": {"train.py", "lr = 0.03\n"}}
	previousCommand := boxCommand
	boxCommand = func(stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
		err := previousCommand(stdin, stdout, stderr, name, args...)
		if name == "git" || args[0] != "run" {
			return err
		}
		var box string
		for i, arg := range args {
			if arg == "--name" {
				box = strings.TrimPrefix(args[i+1], "hi-box-")
			}
		}
		home := boxStateFile(box, "home")
		if strings.HasSuffix(box, "-base") {
			os.WriteFile(filepath.Join(home, "score.log"), []byte("val_bpb: 1.0\n"), 0o600)
			os.WriteFile(filepath.Join(home, "baseline.txt"), []byte("0 0 30\n"), 0o600)
			return err
		}
		meta, _ := loadBoxMeta(box)
		key := box[strings.Index(box, "-r")+1:]
		if key == "r2-1" {
			// Round 2 starts from round 1's gain.
			if data, _ := os.ReadFile(filepath.Join(meta.Workdir, "train.py")); string(data) != "lr = 0.04\n" {
				t.Errorf("round 2 started from %q", data)
			}
			task, _ := os.ReadFile(filepath.Join(home, agentResultDir, "task.md"))
			for _, want := range []string{"tune it", "round 2", "best score so far is 0.9", "Change only train.py", "raise lr", "outside the files allowed"} {
				if !strings.Contains(string(task), want) {
					t.Errorf("round 2's task lacks %q:\n%s", want, task)
				}
			}
		}
		os.WriteFile(filepath.Join(meta.Workdir, files[key][0]), []byte(files[key][1]), 0o644)
		boxGit(meta.Workdir, "add", "-A")
		tree := boxGit(meta.Workdir, "write-tree")
		boxGit(meta.Workdir, "reset", "-q")
		// The score writes a file after the snapshot; it isn't the agent's.
		os.WriteFile(filepath.Join(meta.Workdir, "run.log"), []byte("written by the score\n"), 0o644)
		results := filepath.Join(home, agentResultDir)
		os.WriteFile(filepath.Join(results, "tree.txt"), []byte(tree+"\n"), 0o600)
		os.WriteFile(filepath.Join(results, "agent.json"), []byte(`{"agent_exit":0,"agent_seconds":60}`), 0o600)
		os.WriteFile(filepath.Join(results, "check.json"), []byte(`{"agent_exit":0,"agent_seconds":60,"check_exit":0,"check_seconds":1,"score_exit":0,"score_seconds":30}`), 0o600)
		os.WriteFile(filepath.Join(results, "score.log"), []byte("val_bpb: "+scores[key]+"\n"), 0o600)
		idea := map[string]string{"r1-1": "raise lr to 0.04", "r1-2": "change the data"}[key]
		os.WriteFile(filepath.Join(results, "result.json"), []byte(`{"result":"ok\n<<<REPORT\n{\"status\":\"complete\",\"summary\":\"`+idea+`\"}\nREPORT>>>","total_cost_usd":0.5}`), 0o600)
		return err
	}
	t.Cleanup(func() { boxCommand = previousCommand })

	var stdout, stderr bytes.Buffer
	args := []string{"best-of", "2", "--rounds", "2", "--score", "grep val_bpb run.log", "--lower", "--edit", "train.py",
		"--then", `echo "$HI_BEST_OF_RUN $(git rev-parse --abbrev-ref HEAD)" > then.txt`, "--yes", "tune it"}
	if status := runAgent(args, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("status %d: %s %s", status, stdout.String(), stderr.String())
	}
	if spawned != "b-1" || !strings.Contains(stdout.String(), "Baseline: 1") {
		t.Fatalf("spawned %q: %s", spawned, stdout.String())
	}
	var log bytes.Buffer
	if err := driveBestOf("b-1", &log); err != nil {
		t.Fatal(err)
	}
	run, _ := loadBestOf("b-1")
	loop := run.Loop
	// --then ran once, in the project, after the last round, with the run's name.
	if then, _ := os.ReadFile(filepath.Join(project, "then.txt")); !strings.HasPrefix(string(then), "b-1 ") || !strings.Contains(log.String(), "then done") {
		t.Fatalf("then.txt %q, log:\n%s", then, log.String())
	}
	// Every attempt's files stay reachable, kept or not.
	refs := strings.Fields(boxGit(project, "for-each-ref", "--format=%(refname)", "refs/best-of/b-1/"))
	if len(refs) != 4 {
		t.Fatalf("attempt refs %v", refs)
	}
	if cheat := boxGit(project, "show", "refs/best-of/b-1/r1-2:prepare.py"); cheat != "cheat" {
		t.Fatalf("r1-2's prepare.py %q", cheat)
	}
	if loop.State != "done" || loop.Round != 2 || len(loop.History) != 2 || *loop.Best != 0.9 || loop.BestRound != 1 || loop.Spend != 2 {
		t.Fatalf("loop %+v", loop)
	}
	if loop.History[0].Result != "kept" || loop.History[0].Idea != "raise lr to 0.04" || loop.History[1].Result != "discarded" || *loop.History[1].Score != 0.92 {
		t.Fatalf("history %+v", loop.History)
	}
	// hi made one commit, with the agent's file and not the score's.
	if log := boxGit(project, "log", "--format=%s", run.Base+".."+loop.Branch); log != "best-of b-1 round 1: raise lr to 0.04" {
		t.Fatalf("log %q", log)
	}
	if changed := boxGit(project, "diff", "--name-only", run.Base, loop.Branch); changed != "train.py" {
		t.Fatalf("committed %q", changed)
	}
	// Every box and its branch is gone; the gains' branch stays.
	if branches := boxGit(project, "branch", "--list", "best-of/*"); strings.TrimSpace(branches) != "best-of/b-1/best" {
		t.Fatalf("branches %q", branches)
	}
	for _, call := range *calls {
		if call[0] == "run" && !strings.Contains(strings.Join(call, " "), "-base") && !strings.Contains(strings.Join(call, " "), ":/box/turn:ro") {
			t.Fatalf("box without its turn folder: %v", call)
		}
	}
	stdout.Reset()
	runAgent([]string{"best-of", "show", "b-1"}, strings.NewReader(""), &stdout, &stderr)
	for _, want := range []string{"Best: 0.9 from round 1, baseline 1", "kept", "discarded", "Done: finished 2 rounds", "git merge best-of/b-1/best"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("show lacks %q:\n%s", want, stdout.String())
		}
	}
	// Resuming without more rounds would end at once; with more, it goes on.
	if status := runAgent([]string{"best-of", "resume", "b-1"}, strings.NewReader(""), &stdout, &stderr); status != 1 {
		t.Fatalf("resume without more: %d", status)
	}
	spawned = ""
	if status := runAgent([]string{"best-of", "resume", "b-1", "--rounds", "1"}, strings.NewReader(""), &stdout, &stderr); status != 0 || spawned != "b-1" {
		t.Fatalf("resume: %d %q %s", status, spawned, stderr.String())
	}
	if run, _ := loadBestOf("b-1"); run.Loop.Rounds != 3 || run.Loop.Then == "" {
		t.Fatalf("resumed rounds %d, then %q", run.Loop.Rounds, run.Loop.Then)
	}
	stdout.Reset()
	if status := runAgent([]string{"best-of", "ls", "--json"}, strings.NewReader(""), &stdout, &stderr); status != 0 {
		t.Fatalf("ls --json: %d %s", status, stderr.String())
	}
	var runs []bestOfRun
	if err := json.Unmarshal(stdout.Bytes(), &runs); err != nil || len(runs) != 1 || runs[0].ID != "b-1" || runs[0].Root != project {
		t.Fatalf("ls --json %v: %s", err, stdout.String())
	}
}

func TestBestOfRoundThatChangesNothing(t *testing.T) {
	run := bestOfRun{ID: "b-9", Root: t.TempDir(), Score: "x", Loop: &bestOfLoop{Lower: true, Round: 1}, Boxes: []bestOfBox{
		{Name: "same", State: "finished", Score: new(float64), Snapshot: "abc", ChangedFiles: []string{}},
	}}
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	os.MkdirAll(bestOfStateFile("b-9"), 0o700)
	if err := settleBestOfRound(&run, io.Discard); err != nil {
		t.Fatal(err)
	}
	if result := run.Loop.History[0].Result; result != "unchanged" {
		t.Fatalf("result %q", result)
	}
}

func TestBestOfWatchShowsLiveTokens(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	home := boxStateFile("p-b1-r2-1", "home")
	writeSkillTestFile(t, filepath.Join(home, ".claude", "projects", "-box", "s.jsonl"),
		`{"type":"assistant","message":{"id":"m1","usage":{"input_tokens":1200,"output_tokens":300},"content":[{"type":"tool_use","name":"Bash","input":{"command":"make score"}}]}}`+"\n", 0o644)
	writeSkillTestFile(t, boxStateFile("p-b1-r2-2", "home", agentResultDir, "agent.json"), "{}", 0o644)
	run := bestOfRun{ID: "b-1", Loop: &bestOfLoop{Round: 2, Spend: 1.5}, Boxes: []bestOfBox{
		{Name: "p-b1-r2-1", Agent: "claude", State: "running"},
		{Name: "p-b1-r2-2", Agent: "codex", State: "running"},
		{Name: "p-b1-r1-1", Agent: "claude", State: "finished"},
	}}
	readers := map[string]*agentProgressReader{}
	live := bestOfLiveProgress(run, readers)
	if len(live) != 2 || live["p-b1-r2-1"].in != 1200 || live["p-b1-r2-1"].out != 300 {
		t.Fatalf("live progress: %+v", live)
	}
	lines := bestOfLiveLines(run, live, 0)
	for _, want := range []string{"p-b1-r2-1", "working", "1.2k in", "Bash: make score", "p-b1-r2-2", "done", "on top of $1.50 at API prices"} {
		if !strings.Contains(lines, want) {
			t.Errorf("missing %q in:\n%s", want, lines)
		}
	}
	if strings.Contains(lines, "p-b1-r1-1") {
		t.Errorf("a finished box is shown live:\n%s", lines)
	}
	// The reader is kept, so the next poll reads only what the log gained.
	if readers["p-b1-r2-1"] == nil {
		t.Error("the watch didn't keep the box's reader")
	}
}
