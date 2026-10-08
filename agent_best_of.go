package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// hi agent best-of runs one task in several boxes at once, checks each
// result the same way, and helps a person keep the best one. hi runs the
// check itself, in the box, after the agent ends, so an agent that says
// its tests pass is checked, not believed. See
// docs/specs/ideas/hi_agent_best_of.md.

const (
	bestOfMinBoxes = 2
	bestOfMaxBoxes = 8
	// bestOfTail is how many lines of the check's output a run keeps.
	bestOfTail = 50
)

// bestOfRun is run.json in the run's state folder.
type bestOfRun struct {
	ID         string      `json:"id"`
	Prompt     string      `json:"prompt"`
	TaskFile   string      `json:"task_file,omitempty"`
	Root       string      `json:"root"`
	Base       string      `json:"base"`
	Check      string      `json:"check"`
	MaxSeconds int         `json:"max_seconds,omitempty"`
	Created    time.Time   `json:"created"`
	Boxes      []bestOfBox `json:"boxes"`
	Kept       string      `json:"kept,omitempty"`
	Discarded  bool        `json:"discarded,omitempty"`
	// Best and Warnings are worked out each time the table is shown.
	Best     string   `json:"best,omitempty"`
	Warnings []string `json:"warnings"`
}

// bestOfBox is one box of a run and what was measured once it finished.
type bestOfBox struct {
	Rank         int          `json:"rank"`
	Name         string       `json:"name"`
	Agent        string       `json:"agent"`
	Model        string       `json:"model,omitempty"`
	Branch       string       `json:"branch"`
	State        string       `json:"state"`                  // running, finished, or gone
	AgentStatus  string       `json:"agent_status,omitempty"` // done, failed, or timed_out
	AgentExit    int          `json:"agent_exit"`
	AgentSeconds int          `json:"agent_seconds,omitempty"`
	Seconds      int          `json:"seconds,omitempty"` // the whole box, with the check
	Contract     string       `json:"contract,omitempty"`
	Check        *bestOfCheck `json:"check"`
	Files        int          `json:"files"`
	Added        int          `json:"added"`
	Removed      int          `json:"removed"`
	ChangedFiles []string     `json:"changed_files"`
	Flags        []string     `json:"flags"`
	Tokens       *agentTokens `json:"tokens,omitempty"`
	CostUSD      float64      `json:"cost_usd,omitempty"`
	Discarded    bool         `json:"discarded,omitempty"`
}

type bestOfCheck struct {
	Exit    int    `json:"exit"`
	Seconds int    `json:"seconds"`
	Output  string `json:"output"` // the last 50 lines
}

func (b bestOfBox) passed() bool { return b.Check != nil && b.Check.Exit == 0 }

func bestOfStateFile(parts ...string) string {
	box := boxStateFile()
	if box == "" {
		return ""
	}
	return filepath.Join(append([]string{filepath.Dir(box), "best-of"}, parts...)...)
}

func runBestOf(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printBestOfUsage(stdout)
		return 0
	}
	var rest []string
	var agents, check, limit string
	yes := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func() string {
			if _, after, ok := strings.Cut(arg, "="); ok {
				return after
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch {
		case arg == "--":
			rest = append(rest, args[i:]...)
			i = len(args)
		case arg == "--agents" || strings.HasPrefix(arg, "--agents="):
			agents = value()
		case arg == "--check" || strings.HasPrefix(arg, "--check="):
			check = value()
			if strings.TrimSpace(check) == "" {
				fmt.Fprintln(stderr, "hi: --check needs a command, such as --check \"go test ./...\"")
				return 2
			}
		case arg == "--max" || strings.HasPrefix(arg, "--max="):
			limit = value()
		case arg == "--yes" || arg == "-y":
			yes = true
		default:
			rest = append(rest, arg)
		}
	}
	options, err := parseBoxOptions("agent", rest)
	if err != nil {
		fmt.Fprintf(stderr, "hi: %v\n\n", err)
		printBestOfUsage(stderr)
		return 2
	}
	options.yes = yes
	command := ""
	if len(options.words) > 0 {
		command = options.words[0]
	}
	switch command {
	case "ls":
		return exitCode(listBestOf(stdout), stderr)
	case "show":
		return exitCode(showBestOf(options, stdout), stderr)
	case "keep":
		return exitCode(keepBestOf(options, stdin, stdout), stderr)
	case "rm":
		return exitCode(removeBestOf(options, stdin, stdout), stderr)
	}
	return exitCode(startBestOf(options, agents, check, limit, stdin, stdout, stderr), stderr)
}

func printBestOfUsage(w io.Writer) {
	fmt.Fprintln(w, `hi agent best-of runs the same task in n boxes at once, runs a check in each
when its agent finishes, and ranks the results, so you keep the best one.

usage:
  hi agent best-of <n> "<task>"     start n boxes (2 to 8) on the same task and wait for the table
  hi agent best-of <n> <brief.md>   the task is the file's contents; - reads it from stdin
  hi agent best-of ls               runs, their boxes, and their state
  hi agent best-of show <run> [--full]
                                    the table again; --full adds each box's diff
  hi agent best-of keep <run> <box> keep one box and its branch, remove the others
  hi agent best-of rm <run>         remove every box of a run and its branches

options:
  --agents claude,codex  the agents, taken in turn (default: the first one ready)
  --check "<command>"    how to check a result (default: make check); hi runs it in
                         each box after its agent ends, on the box's network
  --max <duration>       a time limit for each agent, such as 45m or 2h
  --yes                  start without asking; agents pass it only after their person agreed
  --detach               start the boxes and return; hi agent best-of show <run> has the table
  --json                 the run as JSON on stdout
  --model, --bundle, --network, --allow, --gpu, --data, --image, --memory
                         as for hi agent, for every box

Each box works on its own branch, best-of/<run>/<i>, from your last commit.
Nothing is merged or pushed: keep one, review it with hi box diff, and merge it.`)
}

// ---------------------------------------------------------------------------
// starting a run

func startBestOf(options boxOptions, agentList, check, limit string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(options.words) == 0 {
		return usageError{`usage: hi agent best-of <n> "<task>"`}
	}
	n, err := strconv.Atoi(options.words[0])
	if err != nil {
		return usageError{fmt.Sprintf("unknown best-of command %q; hi agent best-of help lists them", options.words[0])}
	}
	if n < bestOfMinBoxes || n > bestOfMaxBoxes {
		return usageError{fmt.Sprintf("n is between %d and %d, not %d", bestOfMinBoxes, bestOfMaxBoxes, n)}
	}
	switch {
	case options.name != "":
		return usageError{"best-of names its boxes itself; --name doesn't go with it"}
	case options.here || options.worktree:
		return usageError{"every best-of box gets its own worktree; --here and --worktree don't go with it"}
	}
	options.words = options.words[1:]
	task, err := agentTask(&options, stdin)
	if err != nil {
		return err
	}
	if task == "" {
		return usageError{`best-of needs a task: hi agent best-of <n> "<task>"`}
	}
	root, inGit := boxProjectRoot()
	if !inGit {
		return errors.New("best-of needs a git repository: each box works on its own branch")
	}
	var maxTime time.Duration
	if limit != "" {
		if maxTime, err = time.ParseDuration(limit); err != nil || maxTime < time.Minute {
			return usageError{fmt.Sprintf("--max is a duration of a minute or more, such as 45m or 2h, not %q", limit)}
		}
	}
	info := stdout
	if options.json {
		info = stderr
	}
	kind, err := applyFrontMatter("", &options, stdin, info)
	if err != nil {
		return err
	}
	kinds, err := bestOfAgents(agentList, kind)
	if err != nil {
		return err
	}
	if options.model != "" {
		for _, other := range kinds {
			if other != kinds[0] {
				return usageError{"--model goes with one agent; with several, each uses its own default model"}
			}
		}
		if !agentModelName.MatchString(options.model) {
			return fmt.Errorf("%q isn't a model name", options.model)
		}
	}
	if check == "" {
		if !bestOfHasMakeCheck(root) {
			return usageError{`this project's Makefile has no check target; give the check with --check, such as --check "go test ./..."`}
		}
		check = "make check"
	}

	id := nextBestOfID()
	run := bestOfRun{ID: id, Prompt: task, TaskFile: options.taskFile, Root: root, Base: boxGit(root, "rev-parse", "HEAD"),
		Check: check, MaxSeconds: int(maxTime / time.Second), Created: time.Now().UTC(), Warnings: []string{}}
	plan := make([]string, n)
	for i := range plan {
		plan[i] = kinds[i%len(kinds)]
	}
	if err := confirmBestOf(run, plan, options, stdin, info); err != nil {
		return err
	}
	if err := os.MkdirAll(bestOfStateFile(id), 0o700); err != nil {
		return err
	}
	if err := saveBestOf(&run); err != nil {
		return err
	}

	base := bestOfNameBase(filepath.Base(root))
	number := strings.TrimPrefix(id, "b-")
	for i, agent := range plan {
		box := options
		box.words = []string{task}
		box.name = fmt.Sprintf("%s-b%s-%d", base, number, i+1)
		box.branch = fmt.Sprintf("best-of/%s/%d", id, i+1)
		box.check, box.maxTime = check, maxTime
		box.model = firstNonEmpty(options.model, defaultAgentModel(agent))
		meta, err := startBox(agent, box, stdin, info, stderr)
		if err != nil {
			if len(run.Boxes) == 0 {
				os.RemoveAll(bestOfStateFile(id))
				return err
			}
			return fmt.Errorf("started %d of %d boxes, then: %w\n  hi agent best-of show %s   the table for the boxes that started\n  hi agent best-of rm %s     remove them", len(run.Boxes), n, err, id, id)
		}
		run.Boxes = append(run.Boxes, bestOfBox{Name: meta.Name, Agent: agent, Model: meta.Model, Branch: meta.Branch, State: "running",
			ChangedFiles: []string{}, Flags: []string{}})
		if err := saveBestOf(&run); err != nil {
			return err
		}
		if i == 0 {
			// The rest start with what the first was allowed, so its
			// questions aren't asked again.
			options.network = meta.Network
			options.allow = bestOfExtraHosts(meta, agent, options.allow)
		}
	}
	if options.detach {
		if options.json {
			return printBestOfJSON(run, stdout)
		}
		fmt.Fprintf(info, "\n%s: %d boxes are working in the background.\n  hi agent best-of show %s   the table so far\n  hi box attach <box>        follow one\n", id, n, id)
		return nil
	}
	if err := waitBestOf(&run, info); err != nil {
		return err
	}
	if options.json {
		if err := printBestOfJSON(run, stdout); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(info)
		printBestOfTable(run, info)
	}
	for _, box := range run.Boxes {
		if box.passed() {
			return nil
		}
	}
	return exitStatusError{code: 1, message: "no box passed the check"}
}

// bestOfAgents is the agents to take in turn: --agents, else the brief's
// agent, else the first one ready.
func bestOfAgents(list, kind string) ([]string, error) {
	if list == "" {
		if kind == "" {
			chosen, err := chooseAgent()
			return []string{chosen}, err
		}
		return []string{kind}, agentReady(kind)
	}
	var kinds []string
	for _, name := range strings.Split(list, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !agentKinds[name] {
			return nil, fmt.Errorf("--agents takes claude, codex, and hermes, not %q", name)
		}
		kinds = append(kinds, name)
	}
	if len(kinds) == 0 {
		return nil, usageError{"--agents needs at least one agent, such as --agents claude,codex"}
	}
	checked := map[string]bool{}
	for _, name := range kinds {
		if !checked[name] {
			checked[name] = true
			if err := agentReady(name); err != nil {
				return nil, err
			}
		}
	}
	return kinds, nil
}

var makeCheckTarget = regexp.MustCompile(`(?m)^check\s*:`)

// bestOfHasMakeCheck reports whether the project's Makefile has a check
// target, without running make.
func bestOfHasMakeCheck(root string) bool {
	for _, name := range []string{"GNUmakefile", "makefile", "Makefile"} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err == nil {
			return makeCheckTarget.Match(data)
		}
	}
	return false
}

// confirmBestOf says what the run costs and asks before anything starts.
func confirmBestOf(run bestOfRun, plan []string, options boxOptions, stdin io.Reader, w io.Writer) error {
	fmt.Fprintf(w, "Best-of %s: %d boxes on the same task, from %s.\n", run.ID, len(plan), run.Base[:min(12, len(run.Base))])
	fmt.Fprintf(w, "  agents  %s\n", strings.Join(plan, ", "))
	fmt.Fprintf(w, "  check   %s, run by hi in each box when its agent ends\n", run.Check)
	if run.MaxSeconds > 0 {
		fmt.Fprintf(w, "  limit   %s for each agent\n", formatDuration(time.Duration(run.MaxSeconds)*time.Second))
	} else {
		fmt.Fprintln(w, "  limit   none; --max sets one for each agent")
	}
	fmt.Fprintf(w, "  cost    about %d times the tokens of one run\n", len(plan))
	if options.gpu && len(plan) > 2 {
		fmt.Fprintf(w, "Note: the %d boxes share one GPU and its memory.\n", len(plan))
	}
	if options.yes {
		return nil
	}
	if !isTerminal(stdin) {
		return errors.New("best-of needs confirmation; rerun with --yes once the cost is agreed")
	}
	fmt.Fprint(w, "Start? [y/N] ")
	answer, _ := readLine(stdin)
	if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
		return errors.New("nothing was started")
	}
	return nil
}

// bestOfBoxCommand wraps an agent's command: the agent runs under the time
// limit, whatever it left running is stopped, and then the check runs on
// the box's final state. check.json holds the agent's exit status and
// time, and the check's; the box exits with the agent's status.
func bestOfBoxCommand(command []string, limit time.Duration) []string {
	results := "/box/home/" + agentResultDir
	run := `"$@"`
	if limit > 0 {
		run = fmt.Sprintf(`timeout -k 30 %d "$@"`, int(limit/time.Second))
	}
	script := `start=$(date +%s); ` + run + `; status=$?; ended=$(date +%s); ` +
		`kill -9 -1 2>/dev/null; ` +
		`sh -c "$HI_CHECK" < /dev/null > ` + results + `/check.log 2>&1; check=$?; done=$(date +%s); ` +
		`printf '{"agent_exit":%d,"agent_seconds":%d,"check_exit":%d,"check_seconds":%d}\n' "$status" $((ended-start)) "$check" $((done-ended)) > ` + results + `/check.json; ` +
		`exit $status`
	return append([]string{"sh", "-c", script, "sh"}, command...)
}

// bestOfExtraHosts is what the first box was allowed beyond its preset and
// its own agent's hosts, for the boxes after it.
func bestOfExtraHosts(meta boxMeta, agent string, allow []string) []string {
	hosts := append([]string{}, allow...)
	data, err := os.ReadFile(boxStateFile(meta.Name, "allow"))
	if err != nil {
		return hosts
	}
	skip := append(boxPresetHosts(meta.Network), agentHosts[agent]...)
	if meta.Data {
		skip = append(skip, boxDataHosts...)
	}
	for _, host := range strings.Split(string(data), "\n") {
		if host = strings.TrimSpace(host); host != "" && !containsString(skip, host) && !containsString(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	return hosts
}

func bestOfNameBase(project string) string {
	base := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(project), "-"), "-")
	if base == "" {
		base = "box"
	}
	if len(base) > 28 {
		base = strings.TrimRight(base[:28], "-")
	}
	return base
}

func nextBestOfID() string {
	entries, _ := os.ReadDir(bestOfStateFile())
	highest := 0
	for _, entry := range entries {
		if number, err := strconv.Atoi(strings.TrimPrefix(entry.Name(), "b-")); err == nil && number > highest {
			highest = number
		}
	}
	return fmt.Sprintf("b-%d", highest+1)
}

func saveBestOf(run *bestOfRun) error {
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	path := bestOfStateFile(run.ID, "run.json")
	temp := path + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func loadBestOf(id string) (bestOfRun, error) {
	var run bestOfRun
	data, err := os.ReadFile(bestOfStateFile(id, "run.json"))
	if err != nil {
		return run, fmt.Errorf("no best-of run %s; hi agent best-of ls lists them", id)
	}
	return run, json.Unmarshal(data, &run)
}

// ---------------------------------------------------------------------------
// waiting and measuring

// waitBestOf waits for every box, measuring each as it finishes. Ctrl+C
// stops waiting, not the boxes.
func waitBestOf(run *bestOfRun, w io.Writer) error {
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	finished := make(chan int, len(run.Boxes))
	waiting := 0
	for i, box := range run.Boxes {
		meta, err := loadBoxMeta(box.Name)
		if err != nil {
			finished <- i
			waiting++
			continue
		}
		engine := boxEngine{name: meta.Engine}
		if engine.bin, err = boxLookPath(meta.Engine); err != nil {
			return fmt.Errorf("%s made this box but is not installed now", meta.Engine)
		}
		waiting++
		go func() {
			engine.interactive(nil, io.Discard, io.Discard, "wait", "hi-box-"+box.Name)
			finished <- i
		}()
	}
	fmt.Fprintf(w, "\nWaiting for %d boxes. hi box attach <box> follows one; Ctrl+C stops waiting, not the boxes.\n", waiting)
	for ; waiting > 0; waiting-- {
		select {
		case i := <-finished:
			box := &run.Boxes[i]
			measureBestOfBox(run, box)
			if err := saveBestOf(run); err != nil {
				return err
			}
			fmt.Fprintf(w, "  %s (%s) %s\n", box.Name, box.Agent, describeBestOfFinish(*box))
		case <-interrupts:
			return exitStatusError{code: 130, message: fmt.Sprintf("stopped waiting; the boxes keep working. hi agent best-of show %s shows the table", run.ID)}
		}
	}
	return nil
}

func describeBestOfFinish(box bestOfBox) string {
	agent := "finished"
	switch box.AgentStatus {
	case "timed_out":
		agent = "ran out of time"
	case "failed":
		agent = fmt.Sprintf("failed (exit status %d)", box.AgentExit)
	}
	if box.AgentSeconds > 0 {
		agent += " after " + formatAgentElapsed(time.Duration(box.AgentSeconds)*time.Second)
	}
	switch {
	case box.State == "gone":
		return "is gone"
	case box.State == "running":
		return "is still working"
	case box.Check == nil:
		return agent + "; its check didn't run"
	case box.passed():
		return fmt.Sprintf("%s; check passed in %s", agent, formatDuration(time.Duration(box.Check.Seconds)*time.Second))
	}
	return fmt.Sprintf("%s; check failed (exit status %d)", agent, box.Check.Exit)
}

// measureBestOf measures every box that has finished since the last look.
func measureBestOf(run *bestOfRun) {
	for i := range run.Boxes {
		measureBestOfBox(run, &run.Boxes[i])
	}
}

// measureBestOfBox measures a box once it has finished: the agent's report,
// the check hi ran, and what changed. A finished box is measured once.
func measureBestOfBox(run *bestOfRun, box *bestOfBox) {
	if box.State == "finished" || box.State == "gone" || box.Discarded {
		return
	}
	meta, err := loadBoxMeta(box.Name)
	if err != nil {
		box.State = "gone"
		return
	}
	engine := boxEngine{name: meta.Engine}
	if engine.bin, err = boxLookPath(meta.Engine); err != nil {
		return
	}
	container := "hi-box-" + meta.Name
	switch engine.state(container) {
	case "running", "created", "configured", "initialized":
		box.State = "running"
		return
	}
	engine.output("stop", "-t", "2", container+"-proxy")
	syncCodexAuth(meta)
	report := collectAgentReport(engine, meta)
	box.State = "finished"
	box.Model = firstNonEmpty(report.Model, box.Model)
	box.AgentExit, box.Seconds, box.Tokens, box.CostUSD = report.ExitCode, report.Seconds, report.Tokens, report.CostUSD
	box.AgentStatus = "done"
	if report.Status == "failed" {
		box.AgentStatus = "failed"
	}
	var contract struct {
		Status string `json:"status"`
	}
	json.Unmarshal(report.Report, &contract)
	box.Contract = contract.Status

	results := boxStateFile(meta.Name, "home", agentResultDir)
	var times struct {
		AgentExit    int `json:"agent_exit"`
		AgentSeconds int `json:"agent_seconds"`
		CheckExit    int `json:"check_exit"`
		CheckSeconds int `json:"check_seconds"`
	}
	if data, err := os.ReadFile(filepath.Join(results, "check.json")); err == nil && json.Unmarshal(data, &times) == nil {
		box.AgentSeconds = times.AgentSeconds
		if times.AgentExit == 124 && run.MaxSeconds > 0 {
			box.AgentStatus = "timed_out"
		}
		output, _ := os.ReadFile(filepath.Join(results, "check.log"))
		box.Check = &bestOfCheck{Exit: times.CheckExit, Seconds: times.CheckSeconds, Output: lastLines(string(output), bestOfTail)}
	}

	box.ChangedFiles = report.ChangedFiles
	if box.ChangedFiles == nil {
		box.ChangedFiles = []string{}
	}
	box.Files = len(box.ChangedFiles)
	box.Flags = []string{}
	for _, path := range box.ChangedFiles {
		if boxRiskyFiles.MatchString(path) {
			box.Flags = append(box.Flags, path)
		}
	}
	box.Added, box.Removed = bestOfLineCounts(meta)
}

// bestOfLineCounts is the lines added and removed since the box started,
// counting new files that aren't committed yet.
func bestOfLineCounts(meta boxMeta) (added, removed int) {
	if !fileExists(meta.Workdir) || meta.Base == "" {
		return 0, 0
	}
	for _, line := range strings.Split(boxGit(meta.Workdir, "diff", "--numstat", meta.Base), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		plus, _ := strconv.Atoi(fields[0]) // binary files are "-"
		minus, _ := strconv.Atoi(fields[1])
		added, removed = added+plus, removed+minus
	}
	for _, path := range boxUntracked(meta.Workdir) {
		if info, err := os.Stat(filepath.Join(meta.Workdir, path)); err != nil || info.Size() > 1<<20 {
			continue
		}
		if data, err := os.ReadFile(filepath.Join(meta.Workdir, path)); err == nil {
			added += strings.Count(string(data), "\n")
			if len(data) > 0 && data[len(data)-1] != '\n' {
				added++
			}
		}
	}
	return added, removed
}

// bestOfBoxes is "1 box is" or "3 boxes are".
func bestOfBoxes(n int, one, many string) string {
	if n == 1 {
		return "1 box" + one
	}
	return fmt.Sprintf("%d boxes%s", n, many)
}

func lastLines(text string, count int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// ranking and the table

// rankBestOf orders the boxes and works out the run's best box and its
// warnings. Without a judge, the order is: boxes that passed the check and
// changed something with an agent that finished, then the other passing
// boxes, the running ones, and the rest; within each, fewer flagged files,
// then a smaller diff, then a shorter run.
func rankBestOf(run *bestOfRun) {
	tier := func(box bestOfBox) int {
		switch {
		case box.passed() && box.AgentStatus == "done" && box.Files > 0:
			return 0
		case box.passed():
			return 1
		case box.State == "running":
			return 2
		case box.State == "finished":
			return 3
		}
		return 4
	}
	sort.SliceStable(run.Boxes, func(i, j int) bool {
		a, b := run.Boxes[i], run.Boxes[j]
		if tier(a) != tier(b) {
			return tier(a) < tier(b)
		}
		if len(a.Flags) != len(b.Flags) {
			return len(a.Flags) < len(b.Flags)
		}
		if a.Added+a.Removed != b.Added+b.Removed {
			return a.Added+a.Removed < b.Added+b.Removed
		}
		if a.AgentSeconds != b.AgentSeconds {
			return a.AgentSeconds < b.AgentSeconds
		}
		return a.Name < b.Name
	})
	run.Best, run.Warnings = "", []string{}
	passed, finished := 0, 0
	for i := range run.Boxes {
		run.Boxes[i].Rank = i + 1
		box := run.Boxes[i]
		if box.State == "finished" {
			finished++
		}
		if box.passed() {
			passed++
		}
		if run.Best == "" && tier(box) == 0 && !box.Discarded {
			run.Best = box.Name
		}
	}
	if passed >= 2 && passed == len(run.Boxes) && !bestOfTestsTouched(*run) {
		run.Warnings = append(run.Warnings, "every box passed, and no box changed a test or has a test named after the files it changed: the check may not test this task, so read the diffs")
	}
	if finished == len(run.Boxes) && passed == 0 {
		run.Warnings = append(run.Warnings, "no box passed the check")
	}
}

var bestOfTestFile = regexp.MustCompile(`(^|/)(tests?|spec|__tests__)/|(^|/)test_[^/]*$|_test\.[A-Za-z]+$|\.(test|spec)\.[A-Za-z]+$|(^|/)[^/]*Tests?\.[A-Za-z]+$`)

// bestOfTestsTouched reports whether any box changed a test, or changed a
// file that a test in the project is named after (loader.py and
// test_loader.py). It is a hint about the check, not coverage.
func bestOfTestsTouched(run bestOfRun) bool {
	var stems []string
	for _, box := range run.Boxes {
		for _, path := range box.ChangedFiles {
			if bestOfTestFile.MatchString(path) {
				return true
			}
			name := filepath.Base(path)
			if stem := strings.TrimSuffix(name, filepath.Ext(name)); len(stem) >= 3 {
				stems = append(stems, strings.ToLower(stem))
			}
		}
	}
	if len(stems) == 0 {
		return false
	}
	for _, path := range strings.Split(boxGit(run.Root, "ls-tree", "-r", "--name-only", run.Base), "\n") {
		if !bestOfTestFile.MatchString(path) {
			continue
		}
		name := strings.ToLower(filepath.Base(path))
		for _, stem := range stems {
			if strings.Contains(name, stem) {
				return true
			}
		}
	}
	return false
}

func printBestOfTable(run bestOfRun, w io.Writer) {
	rankBestOf(&run)
	elapsed := 0
	running := 0
	for _, box := range run.Boxes {
		elapsed = max(elapsed, box.Seconds)
		if box.State == "running" {
			running++
		}
	}
	took := formatDuration(time.Duration(elapsed) * time.Second)
	if running > 0 {
		took = formatDuration(time.Since(run.Created)) + " so far"
	}
	prompt := strings.Join(strings.Fields(run.Prompt), " ")
	if run.TaskFile != "" {
		prompt = filepath.Base(run.TaskFile)
	}
	if len([]rune(prompt)) > 50 {
		prompt = string([]rune(prompt)[:49]) + "…"
	}
	fmt.Fprintf(w, "Best-of %s · %q · %d boxes · %s\n", run.ID, prompt, len(run.Boxes), took)
	fmt.Fprintf(w, "Check: %s\n\n", run.Check)
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "  #\tBOX\tAGENT\tCHECK\tDIFF\tFLAGS\tTIME\tSPEND\tNOTE")
	for _, box := range run.Boxes {
		check := "–"
		switch {
		case box.State == "running":
			check = "…"
		case box.Check != nil && box.passed():
			check = "✓ " + formatDuration(time.Duration(box.Check.Seconds)*time.Second)
		case box.Check != nil:
			check = "✗ " + formatDuration(time.Duration(box.Check.Seconds)*time.Second)
		}
		diff := "–"
		if box.State == "finished" {
			diff = "none"
			if box.Files > 0 {
				diff = fmt.Sprintf("+%d −%d %df", box.Added, box.Removed, box.Files)
			}
		}
		flags := "–"
		if len(box.Flags) > 0 {
			flags = strings.Join(box.Flags, ",")
		}
		elapsed := "–"
		if box.AgentSeconds > 0 {
			elapsed = formatAgentElapsed(time.Duration(box.AgentSeconds) * time.Second)
		}
		spend := "–"
		switch {
		case box.CostUSD > 0:
			spend = fmt.Sprintf("$%.2f", box.CostUSD)
		case box.Tokens != nil:
			spend = formatTokenCount(box.Tokens.Input+box.Tokens.Output) + " tok"
		}
		fmt.Fprintf(table, "  %d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", box.Rank, box.Name, box.Agent, check, diff, flags, elapsed, spend, bestOfNote(run, box))
	}
	table.Flush()
	for _, warning := range run.Warnings {
		fmt.Fprintf(w, "\nNote: %s.\n", warning)
	}
	switch {
	case run.Kept != "":
		var branch string
		for _, box := range run.Boxes {
			if box.Name == run.Kept {
				branch = box.Branch
			}
		}
		fmt.Fprintf(w, "\nKept %s on %s. Review with hi box diff %s, merge with git merge %s.\n", run.Kept, branch, run.Kept, branch)
	case run.Discarded:
		fmt.Fprintln(w, "\nEvery box was removed.")
	case running > 0:
		fmt.Fprintf(w, "\n%s still working. hi agent best-of show %s shows the table again.\n", bestOfBoxes(running, " is", " are"), run.ID)
	case run.Best != "":
		fmt.Fprintln(w, "\nRanked by the check, then fewer flagged files and a smaller diff: read the diff before you keep one.")
		fmt.Fprintf(w, "  hi agent best-of keep %s %s     (or: hi box diff %s)\n", run.ID, run.Best, run.Best)
	default:
		fmt.Fprintf(w, "\nNo box is worth keeping. hi box attach <box> shows what an agent did; hi agent best-of rm %s removes them.\n", run.ID)
	}
}

// bestOfNote is the table's last column: why a box is where it is.
func bestOfNote(run bestOfRun, box bestOfBox) string {
	switch {
	case box.Name == run.Kept:
		return "kept"
	case box.Discarded:
		return "removed"
	case box.State == "gone":
		return "the box is gone"
	case box.State == "running":
		return "working"
	case box.AgentStatus == "timed_out":
		return "ran out of time"
	case box.AgentStatus == "failed":
		return fmt.Sprintf("the agent failed (exit status %d)", box.AgentExit)
	case box.Check == nil:
		return "the check didn't run"
	case !box.passed():
		return "check: " + bestOfLastLine(box.Check.Output)
	case box.Files == 0:
		return "changed nothing"
	case box.Contract == "partial" || box.Contract == "blocked":
		return "the agent says " + box.Contract
	}
	return ""
}

func bestOfLastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			if len([]rune(line)) > 60 {
				line = string([]rune(line)[:59]) + "…"
			}
			return line
		}
	}
	return "failed with no output"
}

func printBestOfJSON(run bestOfRun, stdout io.Writer) error {
	rankBestOf(&run)
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

// ---------------------------------------------------------------------------
// ls, show, keep, and rm

func listBestOf(stdout io.Writer) error {
	entries, _ := os.ReadDir(bestOfStateFile())
	var runs []bestOfRun
	for _, entry := range entries {
		if run, err := loadBestOf(entry.Name()); err == nil {
			runs = append(runs, run)
		}
	}
	if len(runs) == 0 {
		fmt.Fprintln(stdout, `No best-of runs. Start one with hi agent best-of 3 "<task>".`)
		return nil
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].Created.After(runs[j].Created) })
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "RUN\tSTATE\tBOXES\tPASSED\tTASK\tAGE")
	for _, run := range runs {
		before, _ := json.Marshal(run)
		measureBestOf(&run)
		if after, _ := json.Marshal(run); string(after) != string(before) {
			saveBestOf(&run)
		}
		running, passed := 0, 0
		for _, box := range run.Boxes {
			if box.State == "running" {
				running++
			}
			if box.passed() {
				passed++
			}
		}
		state := "done"
		switch {
		case run.Kept != "":
			state = "kept " + run.Kept
		case run.Discarded:
			state = "removed"
		case running > 0:
			state = fmt.Sprintf("running (%d left)", running)
		}
		task := strings.Join(strings.Fields(run.Prompt), " ")
		if run.TaskFile != "" {
			task = filepath.Base(run.TaskFile)
		}
		if len([]rune(task)) > 40 {
			task = string([]rune(task)[:39]) + "…"
		}
		fmt.Fprintf(table, "%s\t%s\t%d\t%d\t%s\t%s\n", run.ID, state, len(run.Boxes), passed, task, formatDuration(time.Since(run.Created)))
	}
	return table.Flush()
}

// loadAndMeasureBestOf loads the run named in the arguments and measures
// the boxes that finished since it was last looked at.
func loadAndMeasureBestOf(id string) (bestOfRun, error) {
	run, err := loadBestOf(id)
	if err != nil {
		return run, err
	}
	measureBestOf(&run)
	return run, saveBestOf(&run)
}

func showBestOf(options boxOptions, stdout io.Writer) error {
	if len(options.words) != 2 {
		return usageError{"usage: hi agent best-of show <run> [--full]"}
	}
	run, err := loadAndMeasureBestOf(options.words[1])
	if err != nil {
		return err
	}
	if options.json {
		return printBestOfJSON(run, stdout)
	}
	printBestOfTable(run, stdout)
	if !options.full {
		return nil
	}
	rankBestOf(&run)
	for _, box := range run.Boxes {
		meta, err := loadBoxMeta(box.Name)
		if err != nil || box.State != "finished" {
			continue
		}
		fmt.Fprintf(stdout, "\n#%d ", box.Rank)
		if err := diffBox(meta, true, stdout); err != nil {
			fmt.Fprintf(stdout, "%s: %v\n", box.Name, err)
		}
	}
	return nil
}

func keepBestOf(options boxOptions, stdin io.Reader, stdout io.Writer) error {
	if len(options.words) != 3 {
		return usageError{"usage: hi agent best-of keep <run> <box>"}
	}
	run, err := loadAndMeasureBestOf(options.words[1])
	if err != nil {
		return err
	}
	if run.Kept != "" || run.Discarded {
		return fmt.Errorf("%s is already settled; hi agent best-of show %s shows it", run.ID, run.ID)
	}
	rankBestOf(&run)
	which := options.words[2]
	keep := -1
	for i, box := range run.Boxes {
		if box.Name == which || strconv.Itoa(box.Rank) == strings.TrimPrefix(which, "#") {
			keep = i
		}
	}
	if keep < 0 {
		return fmt.Errorf("%s has no box %s; hi agent best-of show %s lists them", run.ID, which, run.ID)
	}
	kept := run.Boxes[keep]
	switch kept.State {
	case "running":
		return fmt.Errorf("%s is still working; wait for it, or stop it with hi box stop %s first", kept.Name, kept.Name)
	case "gone":
		return fmt.Errorf("%s is gone", kept.Name)
	}
	var others []int
	for i, box := range run.Boxes {
		if i != keep && !box.Discarded {
			others = append(others, i)
		}
	}
	if len(others) > 0 {
		fmt.Fprintf(stdout, "Keep %s on %s, and remove these boxes with their branches and work:\n", kept.Name, kept.Branch)
		for _, i := range others {
			note := ""
			if run.Boxes[i].State == "running" {
				note = "   still working: it is stopped"
			}
			fmt.Fprintf(stdout, "  %s (%s)%s\n", run.Boxes[i].Name, run.Boxes[i].Branch, note)
		}
		if err := confirmRemove(fmt.Sprintf("Remove %s?", bestOfBoxes(len(others), "", "")), options, stdin, stdout); err != nil {
			return err
		}
	}
	for _, i := range others {
		discardBestOfBox(run, &run.Boxes[i])
	}
	run.Kept = kept.Name
	if err := saveBestOf(&run); err != nil {
		return err
	}
	review, merge := "hi box diff "+kept.Name, "git merge "+kept.Branch
	width := max(len(review), len(merge)) + 3
	fmt.Fprintf(stdout, "Kept %s. Its work is on %s:\n  %-*sreview it\n  %-*smerge it, then hi box rm %s\n",
		kept.Name, kept.Branch, width, review, width, merge, kept.Name)
	return nil
}

func removeBestOf(options boxOptions, stdin io.Reader, stdout io.Writer) error {
	if len(options.words) != 2 {
		return usageError{"usage: hi agent best-of rm <run>"}
	}
	run, err := loadAndMeasureBestOf(options.words[1])
	if err != nil {
		return err
	}
	var remove []int
	for i, box := range run.Boxes {
		if !box.Discarded && box.Name != run.Kept {
			remove = append(remove, i)
		}
	}
	if len(remove) == 0 {
		fmt.Fprintf(stdout, "%s has no boxes left to remove.\n", run.ID)
		return nil
	}
	fmt.Fprintf(stdout, "Remove these boxes of %s, with their branches and work:\n", run.ID)
	for _, i := range remove {
		fmt.Fprintf(stdout, "  %s (%s)\n", run.Boxes[i].Name, run.Boxes[i].Branch)
	}
	if err := confirmRemove(fmt.Sprintf("Remove %s?", bestOfBoxes(len(remove), "", "")), options, stdin, stdout); err != nil {
		return err
	}
	for _, i := range remove {
		discardBestOfBox(run, &run.Boxes[i])
	}
	if run.Kept == "" {
		run.Discarded = true
	}
	if err := saveBestOf(&run); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed %s.\n", bestOfBoxes(len(remove), "", ""))
	return nil
}

func confirmRemove(question string, options boxOptions, stdin io.Reader, stdout io.Writer) error {
	if options.yes {
		return nil
	}
	if !isTerminal(stdin) {
		return errors.New("removing boxes needs confirmation; rerun with --yes")
	}
	fmt.Fprintf(stdout, "%s [y/N] ", question)
	answer, _ := readLine(stdin)
	if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
		return errors.New("nothing was removed")
	}
	return nil
}

// discardBestOfBox removes a box, its worktree, and its branch, which only
// this run used.
func discardBestOfBox(run bestOfRun, box *bestOfBox) {
	if meta, err := loadBoxMeta(box.Name); err == nil {
		engine := boxEngine{name: meta.Engine}
		if engine.bin, err = boxLookPath(meta.Engine); err == nil {
			removeBox(engine, meta, true, io.Discard)
		}
	}
	if box.Branch != "" && boxGit(run.Root, "rev-parse", "--verify", "-q", "refs/heads/"+box.Branch) != "" {
		boxGit(run.Root, "branch", "-D", box.Branch)
	}
	if box.State == "running" {
		box.State = "gone"
	}
	box.Discarded = true
}
