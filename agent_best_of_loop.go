package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// Rounds: with --score, a best-of run measures your last commit, then runs
// rounds. Each round starts n boxes from the best result so far; hi scores
// each box itself after its agent ends, and commits the best one to
// best-of/<run>/best only if it beats the best so far. Agents never decide
// what is kept: they learn how earlier attempts did from the next round's
// task. The rounds run in a process of their own, so they outlive the
// terminal; hi agent best-of watch follows them and stop ends them.

// bestOfLoop is a run's rounds, in run.json.
type bestOfLoop struct {
	Lower    bool      `json:"lower"`
	Rounds   int       `json:"rounds"` // 0: no limit
	Deadline time.Time `json:"deadline,omitempty"`
	Budget   float64   `json:"budget_usd,omitempty"`
	Patience int       `json:"patience,omitempty"`
	MinGain  float64   `json:"min_gain,omitempty"`
	Edit     []string  `json:"edit,omitempty"`
	Branch   string    `json:"branch"`
	ScoreMax int       `json:"score_max_seconds,omitempty"`

	Baseline   *float64 `json:"baseline,omitempty"`
	Best       *float64 `json:"best,omitempty"`
	BestRound  int      `json:"best_round"`
	BestCommit string   `json:"best_commit,omitempty"`
	// Tip is the commit the next round starts from: the best so far.
	Tip string `json:"tip"`
	// Since is the round patience counts from, when no gain came after it.
	Since        int           `json:"since,omitempty"`
	Round        int           `json:"round"`
	RoundStarted time.Time     `json:"round_started,omitempty"`
	Spend        float64       `json:"spend_usd"`
	Started      time.Time     `json:"started"`
	PID          int           `json:"pid,omitempty"`
	State        string        `json:"state"` // running, stopped, done, or failed
	Reason       string        `json:"reason,omitempty"`
	Phase        string        `json:"phase,omitempty"`
	History      []bestOfRound `json:"history"`
}

// bestOfRound is one finished round.
type bestOfRound struct {
	Round   int         `json:"round"`
	Started time.Time   `json:"started"`
	Ended   time.Time   `json:"ended"`
	Result  string      `json:"result"` // kept, discarded, crash, or stopped
	Winner  string      `json:"winner,omitempty"`
	Agent   string      `json:"agent,omitempty"`
	Score   *float64    `json:"score,omitempty"`
	Before  *float64    `json:"before,omitempty"`
	Idea    string      `json:"idea,omitempty"`
	Commit  string      `json:"commit,omitempty"`
	Spend   float64     `json:"spend_usd"`
	Boxes   []bestOfBox `json:"boxes"`
}

// These are replaced in tests.
var (
	bestOfPoll  = 3 * time.Second
	bestOfSpawn = func(id string) error {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		log, err := os.OpenFile(bestOfStateFile(id, "loop.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return err
		}
		defer log.Close()
		command := exec.Command(executable, "agent", "best-of", "__loop", id)
		command.Stdout, command.Stderr = log, log
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := command.Start(); err != nil {
			return err
		}
		return command.Process.Release()
	}
)

// parseBestOfLoop reads the rounds' flags; without --score there are no
// rounds.
func parseBestOfLoop(flags bestOfFlags) (*bestOfLoop, error) {
	if flags.score == "" {
		if flags.roundsGiven || flags.within != "" || flags.budget != "" || flags.patience != "" || flags.minGain != "" ||
			len(flags.edit) > 0 || flags.lower || flags.higher {
			return nil, usageError{`--rounds, --for, --budget, --patience, --min-gain, --edit, --lower, and --higher go with --score "<command>"`}
		}
		return nil, nil
	}
	if flags.lower == flags.higher {
		return nil, usageError{"say which score is better: --lower or --higher"}
	}
	loop := &bestOfLoop{Lower: flags.lower, Rounds: 1, Edit: flags.edit, History: []bestOfRound{}}
	if err := loop.applyLimits(flags, 0, 0); err != nil {
		return nil, err
	}
	if flags.minGain != "" {
		value, err := strconv.ParseFloat(flags.minGain, 64)
		if err != nil || value < 0 {
			return nil, usageError{fmt.Sprintf("--min-gain is a number of 0 or more, not %q", flags.minGain)}
		}
		loop.MinGain = value
	}
	return loop, nil
}

// applyLimits sets --rounds, --for, --budget, and --patience; on resume
// they count from where the run is: done rounds and money spent.
func (l *bestOfLoop) applyLimits(flags bestOfFlags, doneRounds int, spent float64) error {
	if flags.roundsGiven {
		switch text := strings.ToLower(flags.rounds); text {
		case "forever", "inf", "0":
			l.Rounds = 0
		default:
			n, err := strconv.Atoi(text)
			if err != nil || n < 1 {
				return usageError{fmt.Sprintf("--rounds is a number of 1 or more, or forever, not %q", flags.rounds)}
			}
			l.Rounds = doneRounds + n
		}
	}
	if flags.within != "" {
		d, err := time.ParseDuration(flags.within)
		if err != nil || d < time.Minute {
			return usageError{fmt.Sprintf("--for is a duration of a minute or more, such as 8h, not %q", flags.within)}
		}
		l.Deadline = time.Now().Add(d).UTC()
	}
	if flags.budget != "" {
		value, err := strconv.ParseFloat(strings.TrimPrefix(flags.budget, "$"), 64)
		if err != nil || value <= 0 {
			return usageError{fmt.Sprintf("--budget is an amount in dollars, such as 50, not %q", flags.budget)}
		}
		l.Budget = spent + value
	}
	if flags.patience != "" {
		n, err := strconv.Atoi(flags.patience)
		if err != nil || n < 1 {
			return usageError{fmt.Sprintf("--patience is a number of rounds, 1 or more, not %q", flags.patience)}
		}
		l.Patience, l.Since = n, doneRounds
	}
	return nil
}

func (l *bestOfLoop) roundsText() string {
	switch l.Rounds {
	case 0:
		return "rounds until it is stopped"
	case 1:
		return "1 round"
	}
	return fmt.Sprintf("up to %d rounds", l.Rounds)
}

func (l *bestOfLoop) stopsText() string {
	var parts []string
	if l.Rounds > 0 {
		parts = append(parts, fmt.Sprintf("after round %d", l.Rounds))
	}
	if !l.Deadline.IsZero() {
		parts = append(parts, "after "+formatDuration(time.Until(l.Deadline))+" (no round starts later)")
	}
	if l.Budget > 0 {
		parts = append(parts, fmt.Sprintf("once agents have spent $%.2f", l.Budget))
	}
	if l.Patience > 0 {
		parts = append(parts, fmt.Sprintf("after %d rounds without a gain", l.Patience))
	}
	parts = append(parts, "or with hi agent best-of stop")
	return strings.Join(parts, ", ")
}

func (l *bestOfLoop) stateText(run bestOfRun) string {
	if bestOfDriverAlive(run) {
		if l.Rounds > 0 {
			return fmt.Sprintf("round %d of %d", l.Round, l.Rounds)
		}
		return fmt.Sprintf("round %d", l.Round)
	}
	if l.State == "running" {
		return "process gone"
	}
	return l.State
}

func (l *bestOfLoop) resultText() string {
	kept := 0
	for _, round := range l.History {
		if round.Result == "kept" {
			kept++
		}
	}
	if l.Best == nil {
		return "-"
	}
	if kept == 0 {
		return "no gain on " + formatScore(*l.Best)
	}
	return fmt.Sprintf("best %s (%d kept)", formatScore(*l.Best), kept)
}

func bestOfDriverAlive(run bestOfRun) bool {
	return run.Loop != nil && run.Loop.PID > 0 && syscall.Kill(run.Loop.PID, 0) == nil
}

func bestOfStopRequest(id string) string {
	data, _ := os.ReadFile(bestOfStateFile(id, "stop"))
	return strings.TrimSpace(string(data))
}

// ---------------------------------------------------------------------------
// starting

// startBestOfLoop makes the branch for the gains, measures the baseline in
// a box here, where the box's questions can be answered, and starts the
// rounds' process.
func startBestOfLoop(run *bestOfRun, options boxOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	info := stdout
	if options.json {
		info = stderr
	}
	loop := run.Loop
	var out bytes.Buffer
	if err := boxCommand(nil, &out, &out, "git", "-C", run.Root, "branch", loop.Branch, run.Base); err != nil {
		os.RemoveAll(bestOfStateFile(run.ID))
		return fmt.Errorf("git branch %s: %s", loop.Branch, qFirstLine(out.String(), err.Error()))
	}
	fail := func(err error) error {
		boxGit(run.Root, "branch", "-D", loop.Branch)
		os.RemoveAll(bestOfStateFile(run.ID))
		return err
	}
	loop.Tip = run.Base
	fmt.Fprintf(info, "\nMeasuring the baseline on %s.\n", run.Base[:min(12, len(run.Base))])
	value, seconds, options, err := bestOfBaseline(run, options, stdin, info, stderr)
	if err != nil {
		return fail(err)
	}
	loop.Baseline, loop.Best = &value, &value
	// A score that runs much longer than the baseline's is stopped.
	loop.ScoreMax = max(120, 2*seconds+60)
	run.Options.Network, run.Options.Allow = options.network, options.allow
	fmt.Fprintf(info, "Baseline: %s, in %s.\n", formatScore(value), formatDuration(time.Duration(seconds)*time.Second))
	loop.State, loop.Started = "running", time.Now().UTC()
	if err := saveBestOf(run); err != nil {
		return fail(err)
	}
	if err := bestOfSpawn(run.ID); err != nil {
		return fail(fmt.Errorf("starting the rounds: %w", err))
	}
	return followBestOf(*run, options, stdin, stdout, info)
}

// followBestOf opens the view in a terminal, or says how to follow.
func followBestOf(run bestOfRun, options boxOptions, stdin io.Reader, stdout, info io.Writer) error {
	if options.json {
		return printBestOfJSON(run, stdout)
	}
	if options.detach || !isTerminal(stdin) {
		fmt.Fprintf(info, "%s runs in the background.\n  hi agent best-of watch %s   follow it\n  hi agent best-of show %s    where it is\n  hi agent best-of stop %s    stop after the current round\n",
			run.ID, run.ID, run.ID, run.ID)
		return nil
	}
	return watchBestOf(run.ID, stdin, stdout)
}

// bestOfBaseline runs the check and the score on the run's starting commit,
// in a box with the run's options, and returns the score, how long it took,
// and the options as the box resolved them (network, allowed hosts).
func bestOfBaseline(run *bestOfRun, options boxOptions, stdin io.Reader, info, stderr io.Writer) (float64, int, boxOptions, error) {
	box := options
	box.name = fmt.Sprintf("%s-b%s-base", bestOfNameBase(filepath.Base(run.Root)), strings.TrimPrefix(run.ID, "b-"))
	box.branch = "best-of/" + run.ID + "/baseline"
	box.from, box.worktree, box.model, box.detach = run.Base, true, "", false
	script := `start=$(date +%s); c=0; if [ -n "$1" ]; then sh -c "$1" < /dev/null > /box/home/check.log 2>&1; c=$?; fi; ` +
		`s=255; if [ "$c" = 0 ]; then sh -c "$2" < /dev/null > /box/home/score.log 2>&1; s=$?; fi; ` +
		`echo "$c $s $(( $(date +%s) - start ))" > /box/home/baseline.txt`
	box.words = []string{"sh", "-c", script, "sh", run.Check, run.Score}
	meta, err := startBox("run", box, stdin, info, stderr)
	if meta.Name != "" {
		defer discardBestOfBox(*run, &bestOfBox{Name: meta.Name, Branch: box.branch})
	}
	if err != nil {
		return 0, 0, options, err
	}
	home := boxStateFile(meta.Name, "home")
	data, _ := os.ReadFile(filepath.Join(home, "baseline.txt"))
	fields := strings.Fields(string(data))
	if len(fields) != 3 {
		return 0, 0, options, errors.New("the baseline box ended without a result; is sh in its image?")
	}
	check, score, seconds := fields[0], fields[1], 0
	seconds, _ = strconv.Atoi(fields[2])
	if check != "0" {
		output, _ := os.ReadFile(filepath.Join(home, "check.log"))
		return 0, 0, options, fmt.Errorf("the check already fails on your last commit (exit status %s): %s", check, bestOfLastLine(string(output)))
	}
	output, _ := os.ReadFile(filepath.Join(home, "score.log"))
	value, ok := lastNumber(string(output))
	if score != "0" || !ok {
		return 0, 0, options, fmt.Errorf("the score doesn't work on your last commit (exit status %s), so nothing was started; its output ends:\n%s",
			score, qIndent(firstNonEmpty(lastLines(string(output), 10), "(nothing)"), "  "))
	}
	options.network = meta.Network
	options.allow = bestOfExtraHosts(meta, "run", options.allow)
	return value, seconds, options, nil
}

// ---------------------------------------------------------------------------
// the rounds' process

// driveBestOf runs rounds until a limit or a stop request ends them. It
// picks up a round that was going when an earlier process ended.
func driveBestOf(id string, log io.Writer) error {
	run, err := loadBestOf(id)
	if err != nil {
		return err
	}
	if run.Loop == nil {
		return fmt.Errorf("%s has no rounds", id)
	}
	loop := run.Loop
	loop.PID, loop.State, loop.Reason = os.Getpid(), "running", ""
	if err := saveBestOf(&run); err != nil {
		return err
	}
	finish := func(state, reason string) error {
		loop.State, loop.Reason, loop.PID, loop.Phase = state, reason, 0, ""
		fmt.Fprintf(log, "%s: %s: %s\n", time.Now().Format(time.DateTime), state, reason)
		return saveBestOf(&run)
	}
	for {
		if len(run.Boxes) == 0 {
			if state, reason := bestOfShouldStop(run); reason != "" {
				return finish(state, reason)
			}
			if err := startBestOfRound(&run, log); err != nil {
				finish("failed", err.Error())
				return err
			}
		}
		stopped, err := playBestOfRound(&run, log)
		if err != nil {
			finish("failed", err.Error())
			return err
		}
		if stopped {
			return finish("stopped", "stopped with --now")
		}
	}
}

// bestOfShouldStop is why no new round starts, if there is a reason.
func bestOfShouldStop(run bestOfRun) (state, reason string) {
	loop := run.Loop
	switch {
	case bestOfStopRequest(run.ID) != "":
		return "stopped", "stopped with hi agent best-of stop"
	case loop.Rounds > 0 && loop.Round >= loop.Rounds:
		return "done", fmt.Sprintf("finished %s", plural(loop.Rounds, "round"))
	case !loop.Deadline.IsZero() && time.Now().After(loop.Deadline):
		return "done", "its time is up (--for)"
	case loop.Budget > 0 && loop.Spend >= loop.Budget:
		return "done", fmt.Sprintf("agents spent $%.2f of the $%.2f budget", loop.Spend, loop.Budget)
	case loop.Patience > 0 && loop.Round-max(loop.BestRound, loop.Since) >= loop.Patience:
		return "done", fmt.Sprintf("no gain in %d rounds (--patience)", loop.Patience)
	}
	return "", ""
}

// startBestOfRound starts the next round's boxes from the best so far.
func startBestOfRound(run *bestOfRun, log io.Writer) error {
	loop := run.Loop
	loop.Round++
	loop.RoundStarted = time.Now().UTC()
	loop.Phase = fmt.Sprintf("round %d: starting %s", loop.Round, bestOfBoxes(len(run.Options.Agents), "", ""))
	if err := saveBestOf(run); err != nil {
		return err
	}
	task := bestOfRoundTask(*run)
	base := bestOfNameBase(filepath.Base(run.Root))
	number := strings.TrimPrefix(run.ID, "b-")
	var lastErr error
	for i, agent := range run.Options.Agents {
		box := run.Options.box()
		box.words = []string{task}
		box.name = fmt.Sprintf("%s-b%s-r%d-%d", base, number, loop.Round, i+1)
		box.branch = fmt.Sprintf("best-of/%s/r%d-%d", run.ID, loop.Round, i+1)
		box.from = loop.Tip
		box.check, box.score = run.Check, run.Score
		box.maxTime, box.scoreMax = time.Duration(run.MaxSeconds)*time.Second, time.Duration(loop.ScoreMax)*time.Second
		box.model = run.Options.Models[i]
		turn := bestOfStateFile(run.ID, "turns", box.name)
		if err := os.MkdirAll(turn, 0o755); err != nil {
			return err
		}
		box.mounts = []string{turn + ":/box/turn:ro"}
		meta, err := startBox(agent, box, nil, log, log)
		if err != nil {
			lastErr = err
			fmt.Fprintf(log, "round %d: %s didn't start: %v\n", loop.Round, box.name, err)
			continue
		}
		run.Boxes = append(run.Boxes, bestOfBox{Name: meta.Name, Agent: agent, Model: meta.Model, Branch: meta.Branch, State: "running",
			ChangedFiles: []string{}, Flags: []string{}})
		if err := saveBestOf(run); err != nil {
			return err
		}
	}
	if len(run.Boxes) == 0 {
		loop.History = append(loop.History, bestOfRound{Round: loop.Round, Started: loop.RoundStarted, Ended: time.Now().UTC(),
			Result: "crash", Idea: "no box started", Boxes: []bestOfBox{}})
		return fmt.Errorf("no box of round %d started: %v", loop.Round, lastErr)
	}
	return nil
}

// bestOfRoundTask is the task with what hi tells the agents about the
// round: how they are scored, the best so far, what they may change, and
// how earlier attempts did.
func bestOfRoundTask(run bestOfRun) string {
	loop := run.Loop
	direction := "lower"
	if !loop.Lower {
		direction = "higher"
	}
	var b strings.Builder
	b.WriteString(run.Prompt)
	b.WriteString("\n\n---\n\n")
	fmt.Fprintf(&b, "This is round %d of a best-of run that hi controls. ", loop.Round)
	if n := len(run.Options.Agents); n > 1 {
		fmt.Fprintf(&b, "%d agents start from the same commit, each in its own box. ", n)
	}
	b.WriteString("The commit holds the best result so far.\n\n")
	fmt.Fprintf(&b, "When you finish, hi runs this command in your box and takes the last number it prints as your score; %s is better:\n\n    %s\n\n", direction, run.Score)
	if run.Check != "" {
		fmt.Fprintf(&b, "First hi runs this check, and a result that fails it doesn't count:\n\n    %s\n\n", run.Check)
	}
	gain := ""
	if loop.MinGain > 0 {
		gain = " by more than " + formatScore(loop.MinGain)
	}
	if loop.Best != nil {
		fmt.Fprintf(&b, "The best score so far is %s. ", formatScore(*loop.Best))
	}
	fmt.Fprintf(&b, "hi keeps your result only if it beats that%s: hi commits it, and the next round starts from it. Otherwise it is discarded.\n", gain)
	if len(loop.Edit) > 0 {
		fmt.Fprintf(&b, "Change only %s. A result that changes any other file is discarded.\n", strings.Join(loop.Edit, ", "))
	}
	b.WriteString("You may run the command yourself while you work; only hi's run counts. Don't change how the score is measured. " +
		"Commit or not, as you like: hi takes your files as you leave them.\n" +
		"Try one idea, or a few that belong together. In your report, make the summary one line that says what you tried: it goes into the log that later rounds read.\n")
	var rows []string
	if loop.Baseline != nil {
		rows = append(rows, fmt.Sprintf("0\t-\t%s\tbaseline\tyour last commit", formatScore(*loop.Baseline)))
	}
	for _, round := range loop.History {
		for _, box := range round.Boxes {
			score, result := "-", "discarded"
			if box.Score != nil {
				score = formatScore(*box.Score)
			}
			switch {
			case round.Result == "kept" && box.Name == round.Winner:
				result = "kept"
			case len(box.Outside) > 0:
				result = "outside the files allowed"
			case !run.good(box):
				result = "crash"
			}
			rows = append(rows, fmt.Sprintf("%d\t%s\t%s\t%s\t%s", round.Round, box.Agent, score, result, firstNonEmpty(box.Idea, "-")))
		}
	}
	if len(rows) > 1 || len(loop.History) > 0 {
		if len(rows) > 60 {
			rows = append(rows[:1], rows[len(rows)-59:]...)
		}
		b.WriteString("\nEarlier attempts, oldest first:\n\n")
		var table strings.Builder
		w := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "round\tagent\tscore\tresult\tidea")
		for _, row := range rows {
			fmt.Fprintln(w, row)
		}
		w.Flush()
		b.WriteString(table.String())
	}
	return b.String()
}

// playBestOfRound waits for the round's agents, gives each box its turn to
// be scored, and settles the round. It reports whether the run was
// stopped with --now.
func playBestOfRound(run *bestOfRun, log io.Writer) (bool, error) {
	loop := run.Loop
	n := len(run.Boxes)
	phase := func(text string) {
		if loop.Phase != text {
			loop.Phase = text
			saveBestOf(run)
		}
	}
	stopNow := func() bool {
		if bestOfStopRequest(run.ID) != "now" {
			return false
		}
		bestOfDiscardRound(run, "stopped")
		return true
	}
	for {
		if stopNow() {
			return true, nil
		}
		done := 0
		for _, box := range run.Boxes {
			if fileExists(boxStateFile(box.Name, "home", agentResultDir, "agent.json")) || !bestOfBoxRunning(box.Name) {
				done++
			}
		}
		if done == n {
			break
		}
		phase(fmt.Sprintf("round %d: %d of %d agents done", loop.Round, done, n))
		time.Sleep(bestOfPoll)
	}
	turn := func(name string) {
		os.WriteFile(filepath.Join(bestOfStateFile(run.ID, "turns", name), "go"), nil, 0o644)
	}
	// waitScored waits until the boxes have stopped: their score is in.
	waitScored := func(names ...string) bool {
		for {
			if stopNow() {
				return true
			}
			running := false
			for _, name := range names {
				running = running || bestOfBoxRunning(name)
			}
			if !running {
				return false
			}
			time.Sleep(bestOfPoll)
		}
	}
	if run.Options.GPU {
		// A score with a time budget means nothing if boxes share the GPU,
		// so they take turns.
		for i, box := range run.Boxes {
			phase(fmt.Sprintf("round %d: scoring %s (%d of %d)", loop.Round, box.Name, i+1, n))
			turn(box.Name)
			if waitScored(box.Name) {
				return true, nil
			}
		}
	} else {
		phase(fmt.Sprintf("round %d: scoring %s", loop.Round, bestOfBoxes(n, "", "")))
		var names []string
		for _, box := range run.Boxes {
			turn(box.Name)
			names = append(names, box.Name)
		}
		if waitScored(names...) {
			return true, nil
		}
	}
	for i := range run.Boxes {
		measureBestOfBox(run, &run.Boxes[i])
	}
	return false, settleBestOfRound(run, log)
}

func bestOfBoxRunning(name string) bool {
	meta, err := loadBoxMeta(name)
	if err != nil {
		return false
	}
	engine := boxEngine{name: meta.Engine}
	if engine.bin, err = boxLookPath(meta.Engine); err != nil {
		return false
	}
	switch engine.state("hi-box-" + name) {
	case "running", "created", "configured", "initialized":
		return true
	}
	return false
}

// settleBestOfRound commits the round's best result if it beats the best so
// far, logs the round, and removes its boxes.
func settleBestOfRound(run *bestOfRun, log io.Writer) error {
	loop := run.Loop
	rankBestOf(run)
	var winner *bestOfBox
	for i := range run.Boxes {
		box := &run.Boxes[i]
		if !run.good(*box) || box.Files == 0 || box.Snapshot == "" {
			continue
		}
		if winner == nil || run.better(*box.Score, *winner.Score) {
			winner = box
		}
	}
	round := bestOfRound{Round: loop.Round, Started: loop.RoundStarted, Ended: time.Now().UTC(), Result: "crash", Before: loop.Best}
	for _, box := range run.Boxes {
		round.Spend += box.CostUSD
	}
	loop.Spend += round.Spend
	if winner != nil {
		round.Winner, round.Agent, round.Score, round.Idea = winner.Name, winner.Agent, winner.Score, winner.Idea
		round.Result = "discarded"
		if loop.Best == nil || bestOfGain(*run, *winner.Score, *loop.Best) {
			before := "none"
			if loop.Best != nil {
				before = formatScore(*loop.Best)
			}
			message := fmt.Sprintf("best-of %s round %d: %s\n\nScore %s, was %s. From %s (%s), checked and scored by hi.\n",
				run.ID, loop.Round, firstNonEmpty(winner.Idea, "no summary"), formatScore(*winner.Score), before, winner.Name, winner.Agent)
			commit, err := bestOfCommitTree(run.Root, winner.Snapshot, loop.Tip, message)
			if err == nil {
				var out bytes.Buffer
				if err = boxCommand(nil, &out, &out, "git", "-C", run.Root, "update-ref", "refs/heads/"+loop.Branch, commit, loop.Tip); err != nil {
					err = fmt.Errorf("git update-ref %s: %s", loop.Branch, qFirstLine(out.String(), err.Error()))
				}
			}
			if err != nil {
				bestOfDiscardRound(run, "crash")
				return err
			}
			round.Result, round.Commit = "kept", commit
			score := *winner.Score
			loop.Tip, loop.BestCommit, loop.Best, loop.BestRound = commit, commit, &score, loop.Round
		}
	}
	for i := range run.Boxes {
		box := run.Boxes[i]
		box.ScoreOutput = lastLines(box.ScoreOutput, 5)
		if box.Check != nil {
			check := *box.Check
			check.Output = lastLines(check.Output, 5)
			box.Check = &check
		}
		round.Boxes = append(round.Boxes, box)
	}
	loop.History = append(loop.History, round)
	score := "-"
	if round.Score != nil {
		score = formatScore(*round.Score)
	}
	fmt.Fprintf(log, "%s: round %d %s, score %s, %s\n", time.Now().Format(time.DateTime), loop.Round, round.Result, score, round.Idea)
	for i := range run.Boxes {
		discardBestOfBox(*run, &run.Boxes[i])
	}
	run.Boxes, loop.Phase = []bestOfBox{}, ""
	return saveBestOf(run)
}

// bestOfGain reports whether score beats best by more than --min-gain.
func bestOfGain(run bestOfRun, score, best float64) bool {
	if run.Loop.Lower {
		return best-score > run.Loop.MinGain
	}
	return score-best > run.Loop.MinGain
}

// bestOfDiscardRound ends the current round without a result.
func bestOfDiscardRound(run *bestOfRun, result string) {
	loop := run.Loop
	round := bestOfRound{Round: loop.Round, Started: loop.RoundStarted, Ended: time.Now().UTC(), Result: result, Before: loop.Best, Boxes: []bestOfBox{}}
	for i := range run.Boxes {
		measureBestOfBox(run, &run.Boxes[i])
		round.Spend += run.Boxes[i].CostUSD
		discardBestOfBox(*run, &run.Boxes[i])
	}
	loop.Spend += round.Spend
	loop.History = append(loop.History, round)
	run.Boxes, loop.Phase = []bestOfBox{}, ""
	saveBestOf(run)
}

// ---------------------------------------------------------------------------
// stop and resume

func stopBestOf(options boxOptions, now bool, stdout io.Writer) error {
	if len(options.words) != 2 {
		return usageError{"usage: hi agent best-of stop <run> [--now]"}
	}
	run, err := loadBestOf(options.words[1])
	if err != nil {
		return err
	}
	if run.Loop == nil {
		return fmt.Errorf("%s has no rounds to stop; hi box stop <box> stops a box, hi agent best-of rm %s removes them", run.ID, run.ID)
	}
	if !bestOfDriverAlive(run) {
		if now && len(run.Boxes) > 0 {
			bestOfDiscardRound(&run, "stopped")
		}
		if run.Loop.State == "running" {
			run.Loop.State, run.Loop.Reason = "stopped", "stopped with hi agent best-of stop"
		}
		if err := saveBestOf(&run); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s isn't running. hi agent best-of resume %s goes on from the best so far.\n", run.ID, run.ID)
		return nil
	}
	request := "round"
	if now {
		request = "now"
	}
	if err := os.WriteFile(bestOfStateFile(run.ID, "stop"), []byte(request+"\n"), 0o600); err != nil {
		return err
	}
	if now {
		fmt.Fprintf(stdout, "%s stops now; the boxes of round %d are removed. The gains so far stay on %s.\n", run.ID, run.Loop.Round, run.Loop.Branch)
	} else {
		fmt.Fprintf(stdout, "%s stops after round %d, so its work counts. --now stops it at once.\n", run.ID, run.Loop.Round)
	}
	return nil
}

func resumeBestOf(options boxOptions, flags bestOfFlags, stdin io.Reader, stdout io.Writer) error {
	if len(options.words) != 2 {
		return usageError{"usage: hi agent best-of resume <run> [--rounds n] [--for duration] [--budget dollars] [--patience n]"}
	}
	run, err := loadBestOf(options.words[1])
	if err != nil {
		return err
	}
	loop := run.Loop
	if loop == nil {
		return fmt.Errorf("%s has no rounds to resume", run.ID)
	}
	if bestOfDriverAlive(run) {
		return fmt.Errorf("%s is running; hi agent best-of watch %s follows it", run.ID, run.ID)
	}
	if err := loop.applyLimits(flags, loop.Round, loop.Spend); err != nil {
		return err
	}
	os.Remove(bestOfStateFile(run.ID, "stop"))
	if state, reason := bestOfShouldStop(run); reason != "" && len(run.Boxes) == 0 {
		return fmt.Errorf("%s would end at once (%s, %s); give it more with --rounds, --for, --budget, or --patience", run.ID, state, reason)
	}
	loop.State, loop.Reason = "running", ""
	if err := saveBestOf(&run); err != nil {
		return err
	}
	if err := bestOfSpawn(run.ID); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s goes on from %s.\n", run.ID, loop.resultText())
	return followBestOf(run, options, stdin, stdout, stdout)
}

// ---------------------------------------------------------------------------
// the view

// bestOfLoopView is the run's rounds as text: rows limits the rounds shown,
// and width the idea column; 0 shows everything.
func bestOfLoopView(run bestOfRun, width, rows int) string {
	loop := run.Loop
	var b strings.Builder
	alive := bestOfDriverAlive(run)
	end := time.Now()
	if !alive && len(loop.History) > 0 {
		end = loop.History[len(loop.History)-1].Ended
	}
	header := []string{fmt.Sprintf("Best-of %s", run.ID), fmt.Sprintf("%q", bestOfTaskText(run, 40)),
		bestOfBoxes(len(run.Options.Agents), " a round", " a round")}
	if loop.Rounds > 0 {
		header = append(header, fmt.Sprintf("round %d of %d", loop.Round, loop.Rounds))
	} else {
		header = append(header, fmt.Sprintf("round %d", loop.Round))
	}
	if !loop.Started.IsZero() {
		header = append(header, formatDuration(end.Sub(loop.Started)))
	}
	if loop.Spend > 0 {
		header = append(header, fmt.Sprintf("$%.2f spent", loop.Spend))
	}
	fmt.Fprintln(&b, strings.Join(header, " · "))
	direction := "lower is better"
	if !loop.Lower {
		direction = "higher is better"
	}
	fmt.Fprintf(&b, "Score: %s (%s)\n", run.Score, direction)
	if loop.Best != nil {
		line := "Best: " + formatScore(*loop.Best)
		if loop.BestRound > 0 {
			line += fmt.Sprintf(" from round %d, baseline %s", loop.BestRound, formatScore(*loop.Baseline))
		} else {
			line += ", the baseline"
		}
		fmt.Fprintf(&b, "%s · on %s\n", line, loop.Branch)
	}
	history := loop.History
	if len(history) > 0 {
		fmt.Fprintln(&b)
		if rows > 0 && len(history) > rows {
			fmt.Fprintf(&b, "  … %d earlier rounds (hi agent best-of show %s)\n", len(history)-rows, run.ID)
			history = history[len(history)-rows:]
		}
		ideaWidth := 60
		if width > 0 {
			ideaWidth = max(20, width-50)
		}
		table := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintln(table, "  ROUND\tRESULT\tSCORE\tBOX\tIDEA")
		for _, round := range history {
			score := "–"
			if round.Score != nil {
				score = formatScore(*round.Score)
			}
			idea := firstNonEmpty(round.Idea, bestOfRoundNote(round))
			if len([]rune(idea)) > ideaWidth {
				idea = string([]rune(idea)[:ideaWidth-1]) + "…"
			}
			fmt.Fprintf(table, "  %d\t%s\t%s\t%s\t%s\n", round.Round, round.Result, score, firstNonEmpty(round.Winner, "–"), idea)
		}
		table.Flush()
	}
	fmt.Fprintln(&b)
	switch {
	case alive:
		fmt.Fprintf(&b, "Now: %s\n", firstNonEmpty(loop.Phase, "between rounds"))
		switch bestOfStopRequest(run.ID) {
		case "now":
			fmt.Fprintln(&b, "It is stopping now.")
		case "round":
			fmt.Fprintln(&b, "It stops after this round.")
		}
	case loop.State == "running":
		fmt.Fprintf(&b, "Its process is gone. hi agent best-of resume %s goes on from the best so far.\n", run.ID)
	case loop.State == "stopped":
		fmt.Fprintf(&b, "Stopped: %s. hi agent best-of resume %s goes on.\n", loop.Reason, run.ID)
	case loop.State == "failed":
		fmt.Fprintf(&b, "Failed: %s. hi agent best-of resume %s tries again.\n", loop.Reason, run.ID)
	default:
		fmt.Fprintf(&b, "Done: %s.\n", loop.Reason)
	}
	if loop.BestCommit != "" {
		fmt.Fprintf(&b, "The gains are on %s: git log -p %s..%s shows them; git merge %s takes them.\n",
			loop.Branch, run.Base[:min(12, len(run.Base))], loop.Branch, loop.Branch)
	} else if !alive {
		fmt.Fprintln(&b, "No gain, so nothing was committed.")
	}
	return b.String()
}

func bestOfRoundNote(round bestOfRound) string {
	switch round.Result {
	case "crash":
		return "no box had a usable result"
	case "stopped":
		return "stopped before it ended"
	}
	return ""
}

type bestOfWatchModel struct {
	id       string
	run      bestOfRun
	loaded   bool
	width    int
	height   int
	asking   bool
	message  string
	finished bool
}

type bestOfWatchTick struct{}
type bestOfWatchData struct {
	run bestOfRun
	err error
}

func (m *bestOfWatchModel) load() tea.Cmd {
	id := m.id
	return func() tea.Msg {
		run, err := loadBestOf(id)
		return bestOfWatchData{run, err}
	}
}

func bestOfWatchNext() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return bestOfWatchTick{} })
}

func (m *bestOfWatchModel) Init() tea.Cmd { return tea.Batch(m.load(), bestOfWatchNext()) }

func (m *bestOfWatchModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
	case bestOfWatchTick:
		return m, tea.Batch(m.load(), bestOfWatchNext())
	case bestOfWatchData:
		if message.err == nil && message.run.Loop != nil {
			m.run, m.loaded = message.run, true
			if !bestOfDriverAlive(m.run) && m.run.Loop.State != "running" {
				m.finished = true
				return m, tea.Quit
			}
		}
	case tea.KeyMsg:
		key := message.String()
		if m.asking {
			m.asking = false
			if key == "y" {
				if err := os.WriteFile(bestOfStateFile(m.id, "stop"), []byte("round\n"), 0o600); err != nil {
					m.message = "hi: " + err.Error()
				} else {
					m.message = "It stops after this round."
				}
			} else {
				m.message = "It goes on."
			}
			return m, nil
		}
		switch key {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "s":
			m.asking = true
		}
	}
	return m, nil
}

func (m *bestOfWatchModel) View() string {
	if !m.loaded {
		return "Loading " + m.id + "…\n"
	}
	rows := 0
	if m.height > 0 {
		rows = max(3, m.height-12)
	}
	view := bestOfLoopView(m.run, m.width, rows)
	if m.finished {
		return view
	}
	footer := "q leaves the view; the run goes on · s stops it after this round"
	if m.asking {
		footer = "Stop after this round? [y/N]"
	} else if m.message != "" {
		footer = m.message + " · " + footer
	}
	return view + "\n" + footer + "\n"
}

// watchBestOf follows a run in the terminal until it ends or q is pressed.
func watchBestOf(id string, stdin io.Reader, stdout io.Writer) error {
	run, err := loadBestOf(id)
	if err != nil {
		return err
	}
	if run.Loop == nil {
		printBestOfTable(run, stdout)
		return nil
	}
	if !isTerminal(stdin) {
		fmt.Fprint(stdout, bestOfLoopView(run, 0, 0))
		return nil
	}
	model := &bestOfWatchModel{id: id}
	if _, err := tea.NewProgram(model, tea.WithInput(stdin), tea.WithOutput(stdout)).Run(); err != nil {
		return err
	}
	if !model.finished {
		fmt.Fprintf(stdout, "Left the view; %s goes on.\n  hi agent best-of watch %s   come back\n  hi agent best-of stop %s    stop after this round\n", id, id, id)
	}
	return nil
}
