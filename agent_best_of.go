package main

import (
	"bytes"
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
// its tests pass is checked, not believed. With --score it runs rounds
// and keeps a result only when it beats the best so far
// (agent_best_of_loop.go). See docs/specs/ideas/hi_agent_best_of.md.

const (
	bestOfMaxBoxes = 8
	// bestOfTail is how many lines of the check's output a run keeps.
	bestOfTail = 50
)

// bestOfRun is run.json in the run's state folder.
type bestOfRun struct {
	ID         string        `json:"id"`
	Prompt     string        `json:"prompt"`
	TaskFile   string        `json:"task_file,omitempty"`
	Root       string        `json:"root"`
	Base       string        `json:"base"`
	Check      string        `json:"check,omitempty"`
	Score      string        `json:"score,omitempty"`
	MaxSeconds int           `json:"max_seconds,omitempty"`
	Created    time.Time     `json:"created"`
	Options    bestOfOptions `json:"options"`
	// Boxes are the run's boxes, or for a run with rounds, the current
	// round's.
	Boxes     []bestOfBox `json:"boxes"`
	Loop      *bestOfLoop `json:"loop,omitempty"`
	Kept      string      `json:"kept,omitempty"`
	Discarded bool        `json:"discarded,omitempty"`
	// Best and Warnings are worked out each time the table is shown.
	Best     string   `json:"best,omitempty"`
	Warnings []string `json:"warnings"`
}

// bestOfOptions are the box options every box of the run gets, kept so
// rounds started later get them too.
type bestOfOptions struct {
	Agents  []string `json:"agents"` // one per box, in order
	Models  []string `json:"models"`
	Network string   `json:"network,omitempty"`
	Allow   []string `json:"allow,omitempty"`
	GPU     bool     `json:"gpu,omitempty"`
	Data    bool     `json:"data,omitempty"`
	Image   string   `json:"image,omitempty"`
	Memory  string   `json:"memory,omitempty"`
	Bundles []string `json:"bundles,omitempty"`
}

func (o bestOfOptions) box() boxOptions {
	return boxOptions{network: o.Network, allow: o.Allow, gpu: o.GPU, data: o.Data, image: o.Image,
		memory: firstNonEmpty(o.Memory, "16g"), bundles: o.Bundles}
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
	Idea         string       `json:"idea,omitempty"` // the agent's summary, on one line
	Check        *bestOfCheck `json:"check"`
	Score        *float64     `json:"score,omitempty"`
	ScoreSeconds int          `json:"score_seconds,omitempty"`
	ScoreOutput  string       `json:"score_output,omitempty"` // the last 50 lines
	// Snapshot is the git tree of the files as the agent left them, taken
	// in the box before the check and score could write anything.
	Snapshot     string       `json:"snapshot,omitempty"`
	Files        int          `json:"files"`
	Added        int          `json:"added"`
	Removed      int          `json:"removed"`
	ChangedFiles []string     `json:"changed_files"`
	Flags        []string     `json:"flags"`
	Outside      []string     `json:"outside,omitempty"` // changed files outside --edit
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

// good reports whether a box's result counts: it finished, passed the
// check, has a score when the run is scored, and stayed inside --edit.
func (run bestOfRun) good(box bestOfBox) bool {
	switch {
	case box.State != "finished" || box.Discarded || len(box.Outside) > 0:
		return false
	case run.Check != "" && !box.passed():
		return false
	case run.Score != "" && box.Score == nil:
		return false
	}
	return true
}

// better reports whether score a beats b in the run's direction.
func (run bestOfRun) better(a, b float64) bool {
	if run.Loop != nil && !run.Loop.Lower {
		return a > b
	}
	return a < b
}

func bestOfStateFile(parts ...string) string {
	box := boxStateFile()
	if box == "" {
		return ""
	}
	return filepath.Join(append([]string{filepath.Dir(box), "best-of"}, parts...)...)
}

// bestOfFlags are best-of's own flags; the rest are hi agent's.
type bestOfFlags struct {
	agents, check, limit, score      string
	rounds, within, budget, patience string
	minGain                          string
	edit                             []string
	lower, higher, now, roundsGiven  bool
}

func runBestOf(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printBestOfUsage(stdout)
		return 0
	}
	var rest []string
	var flags bestOfFlags
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
		is := func(name string) bool { return arg == name || strings.HasPrefix(arg, name+"=") }
		switch {
		case arg == "--":
			rest = append(rest, args[i:]...)
			i = len(args)
		case is("--agents"):
			flags.agents = value()
		case is("--check"), is("--score"):
			name, text := strings.SplitN(arg, "=", 2)[0], value()
			if strings.TrimSpace(text) == "" {
				fmt.Fprintf(stderr, "hi: %s needs a command, such as %s \"go test ./...\"\n", name, name)
				return 2
			}
			if name == "--check" {
				flags.check = text
			} else {
				flags.score = text
			}
		case is("--max"):
			flags.limit = value()
		case is("--rounds"):
			flags.rounds, flags.roundsGiven = value(), true
		case is("--for"):
			flags.within = value()
		case is("--budget"):
			flags.budget = value()
		case is("--patience"):
			flags.patience = value()
		case is("--min-gain"):
			flags.minGain = value()
		case is("--edit"):
			for _, path := range strings.Split(value(), ",") {
				if path = strings.TrimPrefix(strings.TrimSpace(path), "./"); path != "" {
					flags.edit = append(flags.edit, path)
				}
			}
		case arg == "--lower":
			flags.lower = true
		case arg == "--higher":
			flags.higher = true
		case arg == "--now":
			flags.now = true
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
	case "watch":
		if len(options.words) != 2 {
			return exitCode(usageError{"usage: hi agent best-of watch <run>"}, stderr)
		}
		return exitCode(watchBestOf(options.words[1], stdin, stdout), stderr)
	case "stop":
		return exitCode(stopBestOf(options, flags.now, stdout), stderr)
	case "resume":
		return exitCode(resumeBestOf(options, flags, stdin, stdout), stderr)
	case "__loop":
		if len(options.words) != 2 {
			return 2
		}
		return exitCode(driveBestOf(options.words[1], stdout), stderr)
	}
	return exitCode(startBestOf(options, flags, stdin, stdout, stderr), stderr)
}

func printBestOfUsage(w io.Writer) {
	fmt.Fprintln(w, `hi agent best-of runs the same task in n boxes at once, runs a check in each
when its agent finishes, and ranks the results, so you keep the best one.
With --score it runs rounds, and keeps a result only when it beats the best so far.

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
  --check "<command>"    how to check a result (default: make check, without --score);
                         hi runs it in each box after its agent ends, on the box's network
  --max <duration>       a time limit for each agent, such as 45m or 2h
  --yes                  start without asking; agents pass it only after their person agreed
  --detach               start and return; hi agent best-of show <run> has the table
  --json                 the run as JSON on stdout
  --model, --bundle, --network, --allow, --gpu, --data, --image, --memory
                         as for hi agent, for every box

rounds (n can be 1):
  --score "<command>"    hi runs it in each box after the check and takes the last
                         number it prints; with --gpu, one box at a time
  --lower | --higher     which way is better
  --rounds <n>|forever   how many rounds (default 1)
  --for <duration>       start no round after this long, such as 8h
  --budget <dollars>     start no round once agents have spent this much
  --patience <n>         stop after n rounds without a gain
  --min-gain <number>    a smaller gain is noise (default: any gain counts)
  --edit a,b             the files and folders an agent may change; anything else
                         disqualifies its result
  hi agent best-of watch <run>      follow a run; q leaves the view, the run goes on
  hi agent best-of stop <run> [--now]
                                    stop after this round; --now stops the boxes too
  hi agent best-of resume <run> [--rounds n] [--for d] [--budget $]
                                    go on from the best so far

Each box works on its own branch, best-of/<run>/<i>, from your last commit.
Nothing is merged or pushed: keep one, review it with hi box diff, and merge it.
With --score, hi alone commits: each gain is one commit on best-of/<run>/best.`)
}

// ---------------------------------------------------------------------------
// starting a run

func startBestOf(options boxOptions, flags bestOfFlags, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(options.words) == 0 {
		return usageError{`usage: hi agent best-of <n> "<task>"`}
	}
	n, err := strconv.Atoi(options.words[0])
	if err != nil {
		return usageError{fmt.Sprintf("unknown best-of command %q; hi agent best-of help lists them", options.words[0])}
	}
	least := 2
	if flags.score != "" {
		least = 1
	}
	if n < least || n > bestOfMaxBoxes {
		return usageError{fmt.Sprintf("n is between %d and %d, not %d", least, bestOfMaxBoxes, n)}
	}
	switch {
	case options.name != "":
		return usageError{"best-of names its boxes itself; --name doesn't go with it"}
	case options.here || options.worktree:
		return usageError{"every best-of box gets its own worktree; --here and --worktree don't go with it"}
	}
	loop, err := parseBestOfLoop(flags)
	if err != nil {
		return err
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
	if flags.limit != "" {
		if maxTime, err = time.ParseDuration(flags.limit); err != nil || maxTime < time.Minute {
			return usageError{fmt.Sprintf("--max is a duration of a minute or more, such as 45m or 2h, not %q", flags.limit)}
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
	kinds, err := bestOfAgents(flags.agents, kind)
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
	check := flags.check
	if check == "" && loop == nil {
		if !bestOfHasMakeCheck(root) {
			return usageError{`this project's Makefile has no check target; give the check with --check, such as --check "go test ./..."`}
		}
		check = "make check"
	}

	id := nextBestOfID()
	if loop != nil {
		loop.Branch = "best-of/" + id + "/best"
	}
	run := bestOfRun{ID: id, Prompt: task, TaskFile: options.taskFile, Root: root, Base: boxGit(root, "rev-parse", "HEAD"),
		Check: check, Score: flags.score, MaxSeconds: int(maxTime / time.Second), Created: time.Now().UTC(), Loop: loop, Warnings: []string{}}
	run.Options = bestOfOptions{Network: options.network, Allow: options.allow, GPU: options.gpu, Data: options.data,
		Image: options.image, Memory: options.memory, Bundles: options.bundles}
	for i := 0; i < n; i++ {
		agent := kinds[i%len(kinds)]
		run.Options.Agents = append(run.Options.Agents, agent)
		run.Options.Models = append(run.Options.Models, firstNonEmpty(options.model, defaultAgentModel(agent)))
	}
	if err := confirmBestOf(run, options, stdin, info); err != nil {
		return err
	}
	if err := os.MkdirAll(bestOfStateFile(id), 0o700); err != nil {
		return err
	}
	if loop != nil {
		return startBestOfLoop(&run, options, stdin, stdout, stderr)
	}
	if err := saveBestOf(&run); err != nil {
		return err
	}

	base := bestOfNameBase(filepath.Base(root))
	number := strings.TrimPrefix(id, "b-")
	for i, agent := range run.Options.Agents {
		box := options
		box.words = []string{task}
		box.name = fmt.Sprintf("%s-b%s-%d", base, number, i+1)
		box.branch = fmt.Sprintf("best-of/%s/%d", id, i+1)
		box.check, box.maxTime = check, maxTime
		box.model = run.Options.Models[i]
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
		if run.good(box) {
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
func confirmBestOf(run bestOfRun, options boxOptions, stdin io.Reader, w io.Writer) error {
	n := len(run.Options.Agents)
	from := run.Base[:min(12, len(run.Base))]
	if loop := run.Loop; loop != nil {
		fmt.Fprintf(w, "Best-of %s: %s a round, %s, from %s.\n", run.ID, bestOfBoxes(n, "", ""), loop.roundsText(), from)
	} else {
		fmt.Fprintf(w, "Best-of %s: %d boxes on the same task, from %s.\n", run.ID, n, from)
	}
	fmt.Fprintf(w, "  agents  %s\n", strings.Join(run.Options.Agents, ", "))
	if run.Score != "" {
		direction := "lower is better"
		if !run.Loop.Lower {
			direction = "higher is better"
		}
		fmt.Fprintf(w, "  score   %s (%s), run by hi after each agent ends", run.Score, direction)
		if options.gpu {
			fmt.Fprint(w, ", one box at a time")
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "  check   %s\n", firstNonEmpty(run.Check, "none; a score is enough"))
		if len(run.Loop.Edit) > 0 {
			fmt.Fprintf(w, "  edit    only %s\n", strings.Join(run.Loop.Edit, ", "))
		}
		fmt.Fprintf(w, "  keeps   a result only if it beats the best so far; hi commits it to %s\n", run.Loop.Branch)
	} else {
		fmt.Fprintf(w, "  check   %s, run by hi in each box when its agent ends\n", run.Check)
	}
	if run.MaxSeconds > 0 {
		fmt.Fprintf(w, "  limit   %s for each agent\n", formatDuration(time.Duration(run.MaxSeconds)*time.Second))
	} else {
		fmt.Fprintln(w, "  limit   none; --max sets one for each agent")
	}
	if loop := run.Loop; loop != nil {
		fmt.Fprintf(w, "  stops   %s\n", loop.stopsText())
		if loop.Rounds > 0 {
			fmt.Fprintf(w, "  cost    up to %d agent runs; after the first round hi knows what a round costs\n", n*loop.Rounds)
		} else {
			fmt.Fprintf(w, "  cost    %d agent runs a round, with no end set\n", n)
		}
		if loop.Budget > 0 && containsAny(run.Options.Agents, "codex", "hermes") {
			fmt.Fprintln(w, "Note: --budget counts what agents report in dollars; Codex reports only tokens.")
		}
		if loop.Rounds == 0 && loop.Deadline.IsZero() && loop.Budget == 0 && loop.Patience == 0 {
			fmt.Fprintf(w, "Note: nothing ends this run but hi agent best-of stop %s.\n", run.ID)
		}
	} else {
		fmt.Fprintf(w, "  cost    about %d times the tokens of one run\n", n)
	}
	if options.gpu && n > 2 && run.Score == "" {
		fmt.Fprintf(w, "Note: the %d boxes share one GPU and its memory.\n", n)
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

func containsAny(list []string, items ...string) bool {
	for _, item := range items {
		if containsString(list, item) {
			return true
		}
	}
	return false
}

// bestOfBoxCommand wraps an agent's command: the agent runs under the time
// limit, whatever it left running is stopped, the files it left are
// snapshotted as a git tree, and then the check and the score run on that
// state. With turn, the check and score wait until hi puts a go file in
// /box/turn, so scores are taken one box at a time. check.json holds the
// exit statuses and times; the box exits with the agent's status.
func bestOfBoxCommand(command []string, limit time.Duration, turn bool) []string {
	results := "/box/home/" + agentResultDir
	run := `"$@"`
	if limit > 0 {
		run = fmt.Sprintf(`timeout -k 30 %d "$@"`, int(limit/time.Second))
	}
	script := `start=$(date +%s); ` + run + `; status=$?; ended=$(date +%s); ` +
		`kill -9 -1 2>/dev/null; ` +
		`git add -A >/dev/null 2>&1 && git write-tree > ` + results + `/tree.txt 2>/dev/null; ` +
		`printf '{"agent_exit":%d,"agent_seconds":%d}\n' "$status" $((ended-start)) > ` + results + `/agent.json; `
	if turn {
		script += `while [ ! -e /box/turn/go ]; do sleep 2; done; `
	}
	script += `checked=$(date +%s); check=0; ` +
		`if [ -n "$HI_CHECK" ]; then sh -c "$HI_CHECK" < /dev/null > ` + results + `/check.log 2>&1; check=$?; fi; ` +
		`scored=$(date +%s); score=255; ` +
		`if [ -n "$HI_SCORE" ] && [ "$check" = 0 ]; then timeout -k 10 "${HI_SCORE_MAX:-0}" sh -c "$HI_SCORE" < /dev/null > ` + results + `/score.log 2>&1; score=$?; fi; ` +
		`finished=$(date +%s); ` +
		`printf '{"agent_exit":%d,"agent_seconds":%d,"check_exit":%d,"check_seconds":%d,"score_exit":%d,"score_seconds":%d}\n' ` +
		`"$status" $((ended-start)) "$check" $((scored-checked)) "$score" $((finished-scored)) > ` + results + `/check.json; ` +
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
	if len(base) > 24 {
		base = strings.TrimRight(base[:24], "-")
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
			fmt.Fprintf(w, "  %s (%s) %s\n", box.Name, box.Agent, describeBestOfFinish(*run, *box))
		case <-interrupts:
			return exitStatusError{code: 130, message: fmt.Sprintf("stopped waiting; the boxes keep working. hi agent best-of show %s shows the table", run.ID)}
		}
	}
	return nil
}

func describeBestOfFinish(run bestOfRun, box bestOfBox) string {
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
	case run.Check != "" && box.Check == nil:
		return agent + "; its check didn't run"
	case run.Check != "" && !box.passed():
		return fmt.Sprintf("%s; check failed (exit status %d)", agent, box.Check.Exit)
	case run.Score != "" && box.Score == nil:
		return agent + "; no score: " + bestOfLastLine(box.ScoreOutput)
	case run.Score != "":
		return fmt.Sprintf("%s; score %s in %s", agent, formatScore(*box.Score), formatDuration(time.Duration(box.ScoreSeconds)*time.Second))
	}
	return fmt.Sprintf("%s; check passed in %s", agent, formatDuration(time.Duration(box.Check.Seconds)*time.Second))
}

// measureBestOf measures every box that has finished since the last look.
func measureBestOf(run *bestOfRun) {
	for i := range run.Boxes {
		measureBestOfBox(run, &run.Boxes[i])
	}
}

// measureBestOfBox measures a box once it has finished: the agent's report,
// the check and score hi ran, and what changed. A finished box is measured
// once.
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
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	json.Unmarshal(report.Report, &contract)
	box.Contract = contract.Status
	box.Idea = strings.Join(strings.Fields(firstNonEmpty(contract.Summary, qFirstLine(report.Text, ""))), " ")
	if len([]rune(box.Idea)) > 200 {
		box.Idea = string([]rune(box.Idea)[:199]) + "…"
	}

	results := boxStateFile(meta.Name, "home", agentResultDir)
	var times struct {
		AgentExit    int `json:"agent_exit"`
		AgentSeconds int `json:"agent_seconds"`
		CheckExit    int `json:"check_exit"`
		CheckSeconds int `json:"check_seconds"`
		ScoreExit    int `json:"score_exit"`
		ScoreSeconds int `json:"score_seconds"`
	}
	if data, err := os.ReadFile(filepath.Join(results, "check.json")); err == nil && json.Unmarshal(data, &times) == nil {
		box.AgentSeconds = times.AgentSeconds
		if times.AgentExit == 124 && run.MaxSeconds > 0 {
			box.AgentStatus = "timed_out"
		}
		if run.Check != "" {
			output, _ := os.ReadFile(filepath.Join(results, "check.log"))
			box.Check = &bestOfCheck{Exit: times.CheckExit, Seconds: times.CheckSeconds, Output: lastLines(string(output), bestOfTail)}
		}
		if run.Score != "" && times.CheckExit == 0 {
			output, _ := os.ReadFile(filepath.Join(results, "score.log"))
			box.ScoreOutput, box.ScoreSeconds = lastLines(string(output), bestOfTail), times.ScoreSeconds
			if value, ok := lastNumber(string(output)); ok && times.ScoreExit == 0 {
				box.Score = &value
			} else if times.ScoreExit == 124 {
				box.ScoreOutput = strings.TrimSpace(box.ScoreOutput + "\nthe score ran out of time")
			}
		}
	}

	// What changed: from the snapshot when the box took one, so files the
	// check or score wrote don't count.
	box.ChangedFiles = report.ChangedFiles
	box.Added, box.Removed = 0, 0
	if data, err := os.ReadFile(filepath.Join(results, "tree.txt")); err == nil && meta.Base != "" {
		if tree := strings.TrimSpace(string(data)); boxGit(meta.Root, "cat-file", "-t", tree) == "tree" {
			box.Snapshot = tree
			box.ChangedFiles = splitLines(boxGit(meta.Root, "diff", "--name-only", meta.Base, tree))
			box.Added, box.Removed = numstatTotals(boxGit(meta.Root, "diff", "--numstat", meta.Base, tree))
		}
	}
	if box.Snapshot == "" {
		box.Added, box.Removed = bestOfLineCounts(meta)
	}
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
	if run.Loop != nil {
		box.Outside = bestOfOutside(run.Loop.Edit, box.ChangedFiles)
		if box.Snapshot == "" && box.Files > 0 {
			// Without a snapshot hi can't tell the agent's files from the
			// score's, so the result can't be kept.
			box.Outside = append(box.Outside, "(no snapshot)")
		}
	}
}

func splitLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// numstatTotals adds up git diff --numstat; binary files count as 0.
func numstatTotals(numstat string) (added, removed int) {
	for _, line := range strings.Split(numstat, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		plus, _ := strconv.Atoi(fields[0])
		minus, _ := strconv.Atoi(fields[1])
		added, removed = added+plus, removed+minus
	}
	return added, removed
}

// bestOfLineCounts is the lines added and removed since the box started,
// counting new files that aren't committed yet.
func bestOfLineCounts(meta boxMeta) (added, removed int) {
	if !fileExists(meta.Workdir) || meta.Base == "" {
		return 0, 0
	}
	added, removed = numstatTotals(boxGit(meta.Workdir, "diff", "--numstat", meta.Base))
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

// bestOfOutside is the changed files that --edit doesn't allow: a path,
// a folder, or a glob.
func bestOfOutside(edit, files []string) []string {
	if len(edit) == 0 {
		return nil
	}
	var outside []string
	for _, path := range files {
		allowed := false
		for _, pattern := range edit {
			folder := strings.TrimSuffix(pattern, "/")
			if matched, _ := filepath.Match(pattern, path); matched || path == folder || strings.HasPrefix(path, folder+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			outside = append(outside, path)
		}
	}
	return outside
}

var bestOfNumber = regexp.MustCompile(`[-+]?(\d+\.?\d*|\.\d+)([eE][-+]?\d+)?`)

// lastNumber is the last number in a command's output: the score.
func lastNumber(text string) (float64, bool) {
	matches := bestOfNumber.FindAllString(text, -1)
	if len(matches) == 0 {
		return 0, false
	}
	value, err := strconv.ParseFloat(matches[len(matches)-1], 64)
	return value, err == nil
}

func formatScore(value float64) string { return strconv.FormatFloat(value, 'g', 6, 64) }

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
// warnings. Without a judge, the order is: good boxes (see good) that
// changed something with an agent that finished, then the other good
// boxes, the running ones, and the rest; within each, the better score,
// then fewer flagged files, then a smaller diff, then a shorter run.
func rankBestOf(run *bestOfRun) {
	tier := func(box bestOfBox) int {
		switch {
		case run.good(box) && box.AgentStatus == "done" && box.Files > 0:
			return 0
		case run.good(box):
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
		if a.Score != nil && b.Score != nil && *a.Score != *b.Score {
			return run.better(*a.Score, *b.Score)
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
	good, finished := 0, 0
	for i := range run.Boxes {
		run.Boxes[i].Rank = i + 1
		box := run.Boxes[i]
		if box.State == "finished" {
			finished++
		}
		if run.good(box) {
			good++
		}
		if run.Best == "" && tier(box) == 0 && !box.Discarded {
			run.Best = box.Name
		}
	}
	if run.Score == "" && good >= 2 && good == len(run.Boxes) && !bestOfTestsTouched(*run) {
		run.Warnings = append(run.Warnings, "every box passed, and no box changed a test or has a test named after the files it changed: the check may not test this task, so read the diffs")
	}
	if finished == len(run.Boxes) && finished > 0 && good == 0 {
		if run.Score != "" {
			run.Warnings = append(run.Warnings, "no box has a score")
		} else {
			run.Warnings = append(run.Warnings, "no box passed the check")
		}
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

func bestOfTaskText(run bestOfRun, width int) string {
	prompt := strings.Join(strings.Fields(run.Prompt), " ")
	if run.TaskFile != "" {
		prompt = filepath.Base(run.TaskFile)
	}
	if len([]rune(prompt)) > width {
		prompt = string([]rune(prompt)[:width-1]) + "…"
	}
	return prompt
}

func printBestOfTable(run bestOfRun, w io.Writer) {
	run.Boxes = append([]bestOfBox{}, run.Boxes...)
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
	fmt.Fprintf(w, "Best-of %s · %q · %d boxes · %s\n", run.ID, bestOfTaskText(run, 50), len(run.Boxes), took)
	if run.Check != "" {
		fmt.Fprintf(w, "Check: %s\n", run.Check)
	}
	if run.Score != "" {
		fmt.Fprintf(w, "Score: %s\n", run.Score)
	}
	fmt.Fprintln(w)
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	header := "  #\tBOX\tAGENT\tCHECK\tDIFF\tFLAGS\tTIME\tSPEND\tNOTE"
	if run.Score != "" {
		header = "  #\tBOX\tAGENT\tSCORE\tDIFF\tFLAGS\tTIME\tSPEND\tNOTE"
	}
	fmt.Fprintln(table, header)
	for _, box := range run.Boxes {
		check := "–"
		switch {
		case box.State == "running":
			check = "…"
		case run.Score != "" && box.Score != nil:
			check = formatScore(*box.Score)
		case run.Score != "":
			check = "–"
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
	case run.Loop != nil:
		// A run with rounds says what's next in its own view.
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
	case len(box.Outside) > 0:
		return "changed " + strings.Join(box.Outside, ",") + ", outside --edit"
	case box.AgentStatus == "timed_out" && run.Score == "":
		return "ran out of time"
	case box.AgentStatus == "failed" && run.Score == "":
		return fmt.Sprintf("the agent failed (exit status %d)", box.AgentExit)
	case run.Check != "" && box.Check == nil:
		return "the check didn't run"
	case run.Check != "" && !box.passed():
		return "check: " + bestOfLastLine(box.Check.Output)
	case run.Score != "" && box.Score == nil:
		return "score: " + bestOfLastLine(box.ScoreOutput)
	case box.Files == 0:
		return "changed nothing"
	case run.Score != "" && box.Idea != "":
		return box.Idea
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
	run.Boxes = append([]bestOfBox{}, run.Boxes...)
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
	fmt.Fprintln(table, "RUN\tSTATE\tBOXES\tRESULT\tTASK\tAGE")
	for _, run := range runs {
		if run.Loop == nil {
			before, _ := json.Marshal(run)
			measureBestOf(&run)
			if after, _ := json.Marshal(run); string(after) != string(before) {
				saveBestOf(&run)
			}
		}
		running, passed := 0, 0
		for _, box := range run.Boxes {
			if box.State == "running" {
				running++
			}
			if run.good(box) {
				passed++
			}
		}
		state, result := "done", fmt.Sprintf("%d passed", passed)
		switch {
		case run.Loop != nil:
			state, result = run.Loop.stateText(run), run.Loop.resultText()
		case run.Kept != "":
			state = "kept " + run.Kept
		case run.Discarded:
			state = "removed"
		case running > 0:
			state = fmt.Sprintf("running (%d left)", running)
		}
		boxes := len(run.Boxes)
		if run.Loop != nil {
			boxes = len(run.Options.Agents)
		}
		fmt.Fprintf(table, "%s\t%s\t%d\t%s\t%s\t%s\n", run.ID, state, boxes, result, bestOfTaskText(run, 40), formatDuration(time.Since(run.Created)))
	}
	return table.Flush()
}

// loadAndMeasureBestOf loads the run named in the arguments and measures
// the boxes that finished since it was last looked at. A run with rounds
// is measured by its own process.
func loadAndMeasureBestOf(id string) (bestOfRun, error) {
	run, err := loadBestOf(id)
	if err != nil || run.Loop != nil {
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
	if run.Loop != nil {
		fmt.Fprint(stdout, bestOfLoopView(run, 0, 0))
		if options.full && run.Loop.BestCommit != "" {
			var out bytes.Buffer
			boxCommand(nil, &out, io.Discard, "git", "-C", run.Root, "log", "-p", "--reverse", run.Base+".."+run.Loop.Branch)
			fmt.Fprintf(stdout, "\n%s", out.String())
		}
		return nil
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
	if run.Loop != nil {
		return fmt.Errorf("%s keeps its gains itself, on %s; git merge %s takes them", run.ID, run.Loop.Branch, run.Loop.Branch)
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
	// Work the agent left uncommitted goes on the branch too, as it was
	// before the check ran, so git merge takes all of it.
	if meta, err := loadBoxMeta(kept.Name); err == nil && kept.Snapshot != "" && fileExists(meta.Workdir) {
		head := boxGit(meta.Workdir, "rev-parse", "HEAD")
		if head != "" && boxGit(meta.Workdir, "rev-parse", "HEAD^{tree}") != kept.Snapshot {
			if commit, err := bestOfCommitTree(meta.Root, kept.Snapshot, head, "Work "+kept.Name+" left uncommitted\n\nCommitted by hi agent best-of keep."); err == nil {
				boxGit(meta.Workdir, "reset", "-q", commit)
				fmt.Fprintf(stdout, "Committed the work %s left uncommitted as %s.\n", kept.Name, commit[:min(12, len(commit))])
			}
		}
	}
	review, merge := "hi box diff "+kept.Name, "git merge "+kept.Branch
	width := max(len(review), len(merge)) + 3
	fmt.Fprintf(stdout, "Kept %s. Its work is on %s:\n  %-*sreview it\n  %-*smerge it, then hi box rm %s\n",
		kept.Name, kept.Branch, width, review, width, merge, kept.Name)
	return nil
}

// bestOfCommitTree makes a commit of tree on parent, with the project's
// git identity, or hi's when it has none.
func bestOfCommitTree(dir, tree, parent, message string) (string, error) {
	var args []string
	if boxGit(dir, "config", "user.name") == "" || boxGit(dir, "config", "user.email") == "" {
		args = append(args, "-c", "user.name=hi agent best-of", "-c", "user.email=hi@localhost")
	}
	args = append(args, "commit-tree", tree, "-p", parent, "-m", message)
	var out, problem bytes.Buffer
	if err := boxCommand(nil, &out, &problem, "git", append([]string{"-C", dir}, args...)...); err != nil {
		return "", fmt.Errorf("git commit-tree: %s", qFirstLine(problem.String(), err.Error()))
	}
	return strings.TrimSpace(out.String()), nil
}

func removeBestOf(options boxOptions, stdin io.Reader, stdout io.Writer) error {
	if len(options.words) != 2 {
		return usageError{"usage: hi agent best-of rm <run>"}
	}
	run, err := loadAndMeasureBestOf(options.words[1])
	if err != nil {
		return err
	}
	if run.Loop != nil && bestOfDriverAlive(run) {
		return fmt.Errorf("%s is still running; hi agent best-of stop %s --now stops it first", run.ID, run.ID)
	}
	var remove []int
	for i, box := range run.Boxes {
		if !box.Discarded && box.Name != run.Kept {
			remove = append(remove, i)
		}
	}
	if run.Loop != nil && run.Loop.BestCommit == "" && boxGit(run.Root, "rev-parse", "--verify", "-q", "refs/heads/"+run.Loop.Branch) != "" {
		// A run without a gain leaves nothing worth a branch.
		boxGit(run.Root, "branch", "-D", run.Loop.Branch)
		fmt.Fprintf(stdout, "Deleted %s: it had no gains.\n", run.Loop.Branch)
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
	if run.Kept == "" && run.Loop == nil {
		run.Discarded = true
	}
	if run.Loop != nil {
		run.Boxes = []bestOfBox{}
	}
	if err := saveBestOf(&run); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed %s.\n", bestOfBoxes(len(remove), "", ""))
	if run.Loop != nil && run.Loop.BestCommit != "" {
		fmt.Fprintf(stdout, "The gains stay on %s; git branch -D %s deletes them.\n", run.Loop.Branch, run.Loop.Branch)
	}
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
	os.RemoveAll(bestOfStateFile(run.ID, "turns", box.Name))
	if box.State == "running" {
		box.State = "gone"
	}
	box.Discarded = true
}
