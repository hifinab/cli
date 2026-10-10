package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

// The broker is the lead's only way to put members to work. hi team
// __serve listens on a socket in the team's run/ folder, which the lead's
// box gets at /team/run; the lead runs hi team task, review, wait, and
// merge there. The broker checks each request against team.json, then
// runs the member as hi agent on the host, in a box of its own on a branch
// of repo/. The lead never holds the agents' or the repository's
// credentials, and nothing it asks for widens a member's box.

const (
	teamSocketName = "broker.sock"
	teamSocketEnv  = "HI_TEAM_SOCKET"
	// teamMaxBrief is the largest brief the broker takes.
	teamMaxBrief = 256 << 10
)

// teamTask is tasks/<id>.json.
type teamTask struct {
	ID       string       `json:"id"`
	Role     string       `json:"role"`
	Agent    string       `json:"agent"`
	Model    string       `json:"model,omitempty"`
	Kind     string       `json:"kind"`             // task or review
	On       string       `json:"on,omitempty"`     // a task this one continues, on its branch
	Review   string       `json:"review,omitempty"` // for a review: the task it reviews
	Commit   string       `json:"commit,omitempty"` // for a review: the commit it reviewed
	Title    string       `json:"title"`
	Box      string       `json:"box"`
	Branch   string       `json:"branch,omitempty"`
	Status   string       `json:"status"`            // starting, running, done, failed, stopped, merged
	Verdict  string       `json:"verdict,omitempty"` // for a review: approve or changes
	Error    string       `json:"error,omitempty"`
	Report   *agentReport `json:"report,omitempty"`
	Merged   string       `json:"merged,omitempty"` // the merge commit
	Created  time.Time    `json:"created"`
	Finished time.Time    `json:"finished,omitempty"`
}

var teamTaskID = regexp.MustCompile(`^t-[0-9]{1,6}$`)

func teamTaskDir(team string) string { return filepath.Join(teamDir(team), "tasks") }

func loadTeamTask(team, id string) (teamTask, error) {
	var task teamTask
	if !teamTaskID.MatchString(id) {
		return task, fmt.Errorf("a task is named like t-3, not %q", id)
	}
	data, err := os.ReadFile(filepath.Join(teamTaskDir(team), id+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return task, fmt.Errorf("no task %s", id)
	}
	if err != nil {
		return task, err
	}
	return task, json.Unmarshal(data, &task)
}

func saveTeamTask(team string, task teamTask) error {
	data, _ := json.MarshalIndent(task, "", "  ")
	path := filepath.Join(teamTaskDir(team), task.ID+".json")
	if err := os.WriteFile(path+".tmp", append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// loadTeamTasks lists a team's tasks, oldest first.
func loadTeamTasks(team string) []teamTask {
	entries, _ := os.ReadDir(teamTaskDir(team))
	var tasks []teamTask
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok {
			continue
		}
		if task, err := loadTeamTask(team, id); err == nil {
			tasks = append(tasks, task)
		}
	}
	sort.Slice(tasks, func(i, j int) bool { return teamTaskNumber(tasks[i].ID) < teamTaskNumber(tasks[j].ID) })
	return tasks
}

func teamTaskNumber(id string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(id, "t-"))
	return n
}

func printTeamTasks(tasks []teamTask, last int, w io.Writer) {
	if len(tasks) > last {
		tasks = tasks[len(tasks)-last:]
	}
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "TASK\tROLE\tSTATUS\tSTARTED\tTITLE")
	for _, task := range tasks {
		status := task.Status
		if task.Verdict != "" {
			status += " (" + task.Verdict + ")"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", task.ID, task.Role, status, task.Created.Local().Format("Jan 2 15:04"), qClip(task.Title))
	}
	table.Flush()
}

// ---------------------------------------------------------------------------
// the broker, on the host

type teamBroker struct {
	team string
	repo string
	hi   string // this hi binary
	mu   sync.Mutex
	// run starts hi with arguments in repo/ and returns its stdout; tests
	// replace it.
	run func(args ...string) ([]byte, error)
}

func newTeamBroker(team, hi string) *teamBroker {
	b := &teamBroker{team: team, repo: filepath.Join(teamDir(team), "repo"), hi: hi}
	b.run = func(args ...string) ([]byte, error) {
		command := exec.Command(b.hi, args...)
		command.Dir = b.repo
		var stderr strings.Builder
		command.Stderr = &stderr
		out, err := command.Output()
		if err != nil {
			return out, fmt.Errorf("%w: %s", err, strings.ReplaceAll(lastLines(strings.TrimSpace(stderr.String()), 3), "\n", " "))
		}
		return out, nil
	}
	return b
}

// listen opens the socket, then serves it until ctx ends.
func (b *teamBroker) listen(ctx context.Context, socket string) (func() error, error) {
	os.Remove(socket)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		return nil, err
	}
	os.Chmod(socket, 0o600)
	return func() error { return b.serve(ctx, listener) }, nil
}

func (b *teamBroker) serve(ctx context.Context, listener net.Listener) error {
	server := &http.Server{Handler: b.handler()}
	go func() {
		<-ctx.Done()
		server.Close()
	}()
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// resume picks up tasks that were running when the broker last stopped.
func (b *teamBroker) resume() {
	for _, task := range loadTeamTasks(b.team) {
		if task.Status != "running" && task.Status != "starting" {
			continue
		}
		if _, err := loadBoxMeta(task.Box); err != nil {
			task.Status, task.Error, task.Finished = "failed", "hi team stopped before the task's box started", time.Now().UTC()
			saveTeamTask(b.team, task)
			continue
		}
		go b.wait(task)
	}
}

type teamRequest struct {
	Role  string `json:"role,omitempty"`
	Brief string `json:"brief,omitempty"`
	Task  string `json:"task,omitempty"`
	On    string `json:"on,omitempty"`
	Notes string `json:"notes,omitempty"`
}

func (b *teamBroker) handler() http.Handler {
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, value any, err error) {
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		json.NewEncoder(w).Encode(value)
	}
	read := func(r *http.Request) (teamRequest, error) {
		var request teamRequest
		err := json.NewDecoder(io.LimitReader(r.Body, teamMaxBrief+4096)).Decode(&request)
		return request, err
	}
	mux.HandleFunc("POST /task", func(w http.ResponseWriter, r *http.Request) {
		request, err := read(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		task, err := b.startTask(request)
		reply(w, task, err)
	})
	mux.HandleFunc("POST /review", func(w http.ResponseWriter, r *http.Request) {
		request, err := read(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		task, err := b.startReview(request)
		reply(w, task, err)
	})
	mux.HandleFunc("POST /merge", func(w http.ResponseWriter, r *http.Request) {
		request, err := read(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		task, err := b.merge(request.Task)
		reply(w, task, err)
	})
	mux.HandleFunc("POST /stop", func(w http.ResponseWriter, r *http.Request) {
		request, err := read(r)
		if err != nil {
			reply(w, nil, err)
			return
		}
		task, err := b.stop(request.Task)
		reply(w, task, err)
	})
	mux.HandleFunc("GET /tasks", func(w http.ResponseWriter, r *http.Request) {
		reply(w, loadTeamTasks(b.team), nil)
	})
	mux.HandleFunc("GET /task", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		wait, _ := time.ParseDuration(r.URL.Query().Get("wait"))
		deadline := time.Now().Add(min(wait, 30*time.Minute))
		for {
			task, err := loadTeamTask(b.team, id)
			if err != nil || !teamTaskActive(task) || time.Now().After(deadline) {
				reply(w, task, err)
				return
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Second):
			}
		}
	})
	return mux
}

func teamTaskActive(task teamTask) bool { return task.Status == "starting" || task.Status == "running" }

// newTask numbers a task and saves it before its box starts.
func (b *teamBroker) newTask(task teamTask) (teamTask, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	tasks := loadTeamTasks(b.team)
	n := 1
	if len(tasks) > 0 {
		n = teamTaskNumber(tasks[len(tasks)-1].ID) + 1
	}
	task.ID = fmt.Sprintf("t-%d", n)
	task.Box = b.team + "-" + task.ID
	task.Status, task.Created = "starting", time.Now().UTC()
	return task, saveTeamTask(b.team, task)
}

func (b *teamBroker) member(role string) (teamMember, error) {
	config, err := loadTeam(b.team)
	if err != nil {
		return teamMember{}, err
	}
	if role == "lead" {
		return teamMember{}, errors.New("the lead doesn't hand tasks to itself")
	}
	member, ok := config.Members[role]
	if !ok {
		return teamMember{}, fmt.Errorf("the team has no %s; its members are %s", role, strings.Join(teamRoles(config)[1:], ", "))
	}
	return member, nil
}

func (b *teamBroker) startTask(request teamRequest) (teamTask, error) {
	member, err := b.member(request.Role)
	if err != nil {
		return teamTask{}, err
	}
	brief := strings.TrimSpace(request.Brief)
	if brief == "" {
		return teamTask{}, errors.New("the brief is empty")
	}
	if len(brief) > teamMaxBrief {
		return teamTask{}, fmt.Errorf("the brief is longer than %d KB", teamMaxBrief>>10)
	}
	task := teamTask{Role: request.Role, Agent: member.Agent, Model: member.Model, Kind: "task", Title: teamTitle(brief)}
	from := ""
	if request.On != "" {
		on, err := loadTeamTask(b.team, request.On)
		if err != nil {
			return teamTask{}, err
		}
		if on.Kind != "task" || on.Branch == "" || teamTaskActive(on) {
			return teamTask{}, fmt.Errorf("%s isn't a finished task with a branch to continue", on.ID)
		}
		task.On, from = on.ID, on.Branch
	}
	if task, err = b.newTask(task); err != nil {
		return task, err
	}
	// A heading first, so the brief can't carry front matter that would
	// widen the box.
	text := fmt.Sprintf("# Task %s for the %s of the %s team\n\n%s\n", task.ID, task.Role, b.team, brief)
	if task.On != "" {
		text += fmt.Sprintf("\nYou continue task %s: its commits are already on your branch.\n", task.On)
	}
	go b.launch(task, member, text, from)
	return task, nil
}

func (b *teamBroker) startReview(request teamRequest) (teamTask, error) {
	member, err := b.member("reviewer")
	if err != nil {
		return teamTask{}, err
	}
	reviewed, err := loadTeamTask(b.team, request.Task)
	if err != nil {
		return teamTask{}, err
	}
	if reviewed.Kind != "task" || reviewed.Status != "done" || reviewed.Branch == "" {
		return teamTask{}, fmt.Errorf("%s isn't a finished task with work to review (%s)", reviewed.ID, reviewed.Status)
	}
	config, err := loadTeam(b.team)
	if err != nil {
		return teamTask{}, err
	}
	commit := boxGit(b.repo, "rev-parse", reviewed.Branch)
	base := boxGit(b.repo, "merge-base", config.Branch, reviewed.Branch)
	if commit == "" || base == "" {
		return teamTask{}, fmt.Errorf("%s's branch %s is gone", reviewed.ID, reviewed.Branch)
	}
	if commit == base {
		return teamTask{}, fmt.Errorf("%s committed nothing to review", reviewed.ID)
	}
	task := teamTask{Role: "reviewer", Agent: member.Agent, Model: member.Model, Kind: "review", Review: reviewed.ID, Commit: commit,
		Title: "Review " + reviewed.ID + ": " + reviewed.Title}
	if task, err = b.newTask(task); err != nil {
		return task, err
	}
	go b.launch(task, member, teamReviewBrief(b.team, task, reviewed, base, b.brief(reviewed), request.Notes), reviewed.Branch)
	return task, nil
}

// brief is the text a task was given.
func (b *teamBroker) brief(task teamTask) string {
	data, _ := os.ReadFile(filepath.Join(teamTaskDir(b.team), task.ID+".md"))
	return strings.TrimSpace(string(data))
}

func teamReviewBrief(team string, task, reviewed teamTask, base, brief, notes string) string {
	var text strings.Builder
	fmt.Fprintf(&text, "# Review %s for the %s team\n\n", task.ID, team)
	fmt.Fprintf(&text, "You are the team's reviewer. The %s did task %s on this branch. Review its work before the lead merges it.\n\n", reviewed.Role, reviewed.ID)
	fmt.Fprintf(&text, "The work is the commits since %s:\n\n    git log --oneline %s..HEAD\n    git diff %s...HEAD\n\n", base[:min(len(base), 12)], base, base)
	text.WriteString("Check that:\n\n")
	text.WriteString("- it does what the brief asks, and nothing it doesn't;\n")
	text.WriteString("- it has tests for what the brief says must work, and the tests pass when you run them;\n")
	text.WriteString("- it doesn't break what worked before, leak secrets, or leave debugging behind;\n")
	text.WriteString("- the README still says how to run and test the project.\n\n")
	text.WriteString("Don't fix anything yourself and don't commit: say what must change.\n\n")
	text.WriteString("In your report, start the summary with \"Approve:\" if it can be merged as it is, or with \"Changes needed:\" and put each change in follow_ups.\n\n")
	if notes = strings.TrimSpace(notes); notes != "" {
		fmt.Fprintf(&text, "## The lead's notes\n\n%s\n\n", notes)
	}
	fmt.Fprintf(&text, "## The brief for %s\n\n%s\n", reviewed.ID, brief)
	if reviewed.Report != nil && len(reviewed.Report.Report) > 0 && string(reviewed.Report.Report) != "null" {
		fmt.Fprintf(&text, "\n## The %s's report\n\n%s\n", reviewed.Role, string(reviewed.Report.Report))
	}
	return text.String()
}

// launch starts the member's box with hi agent, then waits for it.
func (b *teamBroker) launch(task teamTask, member teamMember, brief, from string) {
	path := filepath.Join(teamTaskDir(b.team), task.ID+".md")
	if err := os.WriteFile(path, []byte(brief), 0o600); err != nil {
		b.fail(task, err)
		return
	}
	args := []string{"agent", task.Agent, "--detach", "--json", "--name", task.Box, "--task-file", path}
	if member.Model != "" {
		args = append(args, "--model", member.Model)
	}
	if member.Bundle != "" {
		args = append(args, "--bundle", member.Bundle)
	}
	if member.Network != "" {
		args = append(args, "--network", member.Network)
	}
	for _, host := range member.Allow {
		args = append(args, "--allow", host)
	}
	if from != "" {
		args = append(args, "--from", from)
	}
	out, err := b.run(args...)
	if err != nil {
		b.fail(task, err)
		return
	}
	var started agentReport
	if err := json.Unmarshal(out, &started); err != nil {
		b.fail(task, fmt.Errorf("hi agent: %w", err))
		return
	}
	task.Status, task.Branch = "running", started.Branch
	saveTeamTask(b.team, task)
	b.wait(task)
}

// wait collects a running task's report when its box stops.
func (b *teamBroker) wait(task teamTask) {
	out, err := b.run("agent", "wait", task.Box, "--json")
	var report agentReport
	if jsonErr := json.Unmarshal(out, &report); jsonErr != nil {
		if err == nil {
			err = jsonErr
		}
		b.fail(task, err)
		return
	}
	current, loadErr := loadTeamTask(b.team, task.ID)
	if loadErr == nil && current.Status == "stopped" {
		task.Status = "stopped"
	} else if report.Status == "failed" {
		task.Status = "failed"
	} else {
		task.Status = "done"
	}
	task.Report, task.Finished = &report, time.Now().UTC()
	if task.Kind == "review" {
		task.Verdict = teamVerdict(report)
	}
	saveTeamTask(b.team, task)
}

func (b *teamBroker) fail(task teamTask, err error) {
	task.Status, task.Error, task.Finished = "failed", err.Error(), time.Now().UTC()
	saveTeamTask(b.team, task)
}

// teamVerdict reads a review's report: approve only when the reviewer
// finished and said so.
func teamVerdict(report agentReport) string {
	var block struct {
		Status  string `json:"status"`
		Summary string `json:"summary"`
	}
	json.Unmarshal(report.Report, &block)
	if report.Status == "done" && block.Status == "complete" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(block.Summary)), "approve") {
		return "approve"
	}
	return "changes"
}

func (b *teamBroker) stop(id string) (teamTask, error) {
	task, err := loadTeamTask(b.team, id)
	if err != nil {
		return task, err
	}
	if !teamTaskActive(task) {
		return task, fmt.Errorf("%s isn't running (%s)", id, task.Status)
	}
	task.Status = "stopped"
	if err := saveTeamTask(b.team, task); err != nil {
		return task, err
	}
	if _, err := b.run("box", "stop", task.Box); err != nil {
		return task, err
	}
	return task, nil
}

// merge merges a task's branch into the team's branch, once a review of
// its latest commit approved it.
func (b *teamBroker) merge(id string) (teamTask, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	task, err := loadTeamTask(b.team, id)
	if err != nil {
		return task, err
	}
	if task.Kind != "task" || task.Status != "done" || task.Branch == "" {
		return task, fmt.Errorf("%s isn't a finished task with a branch (%s)", id, task.Status)
	}
	config, err := loadTeam(b.team)
	if err != nil {
		return task, err
	}
	head := boxGit(b.repo, "rev-parse", task.Branch)
	if head == "" {
		return task, fmt.Errorf("%s's branch %s is gone", id, task.Branch)
	}
	var approval *teamTask
	for _, review := range loadTeamTasks(b.team) {
		if review.Kind == "review" && review.Review == id && review.Commit == head && review.Status == "done" && review.Verdict == "approve" {
			approval = &review
		}
	}
	if approval == nil {
		return task, fmt.Errorf("no review approved %s's latest commit; run hi team review %s", id, id)
	}
	if dirty := boxGit(b.repo, "status", "--porcelain", "--untracked-files=no"); dirty != "" {
		return task, errors.New("repo/ has changes that aren't committed; the lead's branch must be clean to merge")
	}
	if current := boxGit(b.repo, "branch", "--show-current"); current != config.Branch {
		if out, err := teamGit(b.repo, "checkout", "-q", config.Branch); err != nil {
			return task, fmt.Errorf("git checkout %s: %s", config.Branch, out)
		}
	}
	message := fmt.Sprintf("%s\n\nTask %s by the %s, approved in review %s by the reviewer.", task.Title, task.ID, task.Role, approval.ID)
	if out, err := teamGit(b.repo, "merge", "--no-ff", "-m", message, task.Branch); err != nil {
		teamGit(b.repo, "merge", "--abort")
		return task, fmt.Errorf("%s doesn't merge cleanly into %s (%s); give the coder a new task to redo it on the latest %s", id, config.Branch, qFirstLine(out, err.Error()), config.Branch)
	}
	task.Status, task.Merged = "merged", boxGit(b.repo, "rev-parse", "HEAD")
	if err := saveTeamTask(b.team, task); err != nil {
		return task, err
	}
	if config.Remote != "" {
		if out, err := teamGit(b.repo, "push", "-q", "origin", config.Branch); err != nil {
			task.Error = "merged, but git push failed: " + qFirstLine(out, err.Error())
			saveTeamTask(b.team, task)
		}
	}
	// The boxes of this task, the tasks it continued, and their reviews are
	// done with, and so are their branches.
	tasks := loadTeamTasks(b.team)
	merged := map[string]bool{}
	for _, other := range tasks {
		if other.Kind == "task" && !teamTaskActive(other) && teamIsAncestor(b.repo, other.Branch, "HEAD") {
			merged[other.ID] = true
			if other.Status == "done" {
				other.Status = "merged"
				saveTeamTask(b.team, other)
			}
		}
	}
	for _, other := range tasks {
		if (merged[other.ID] || merged[other.Review]) && !teamTaskActive(other) {
			b.run("box", "rm", "--force", other.Box)
			if other.Branch != "" {
				teamGit(b.repo, "branch", "-D", other.Branch)
			}
		}
	}
	return task, nil
}

func teamIsAncestor(repo, branch, of string) bool {
	if branch == "" {
		return false
	}
	return exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", branch, of).Run() == nil
}

// teamGit runs git in the repository as the team's lead.
func teamGit(repo string, args ...string) (string, error) {
	name := firstNonEmpty(boxGit(repo, "config", "user.name"), "hi team")
	email := firstNonEmpty(boxGit(repo, "config", "user.email"), "team@hi.invalid")
	out, err := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=" + name, "-c", "user.email=" + email}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// teamTitle is a brief's first line, without its heading marks.
func teamTitle(brief string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(brief), "\n")
	return qClip(strings.TrimSpace(strings.TrimLeft(line, "# ")))
}

// ---------------------------------------------------------------------------
// the lead's commands, in its box

var teamLeadCommands = map[string]bool{"task": true, "review": true, "wait": true, "show": true, "tasks": true, "merge": true, "stop": true}

func runTeamLeadCommand(command string, args []string, stdin io.Reader, stdout io.Writer) error {
	socket := os.Getenv(teamSocketEnv)
	if socket == "" {
		return errors.New("hi team " + command + " is for a team's lead, in its box; on this machine, hi team status <name> shows a team's tasks")
	}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
	call := func(method, path string, body any, out any) error {
		var reader io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reader = strings.NewReader(string(data))
		}
		request, _ := http.NewRequest(method, "http://team"+path, reader)
		response, err := client.Do(request)
		if err != nil {
			return fmt.Errorf("the team's broker isn't answering: %w", err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK {
			var failure struct{ Error string }
			json.Unmarshal(data, &failure)
			return errors.New(firstNonEmpty(failure.Error, response.Status))
		}
		return json.Unmarshal(data, out)
	}
	var task teamTask
	switch command {
	case "task":
		var request teamRequest
		var words []string
		for i := 0; i < len(args); i++ {
			if args[i] == "--on" && i+1 < len(args) {
				request.On = args[i+1]
				i++
			} else {
				words = append(words, args[i])
			}
		}
		if len(words) != 2 {
			return usageError{"usage: hi team task <role> <brief.md|-> [--on <task>]"}
		}
		brief, err := teamReadBrief(words[1], stdin)
		if err != nil {
			return err
		}
		request.Role, request.Brief = words[0], brief
		if err := call("POST", "/task", request, &task); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s started: the %s (%s) works on %q.\nhi team wait %s waits for its report.\n", task.ID, task.Role, agentDisplayName(task.Agent), task.Title, task.ID)
	case "review":
		if len(args) < 1 || len(args) > 2 {
			return usageError{"usage: hi team review <task> [<notes.md>]"}
		}
		request := teamRequest{Task: args[0]}
		if len(args) == 2 {
			notes, err := teamReadBrief(args[1], stdin)
			if err != nil {
				return err
			}
			request.Notes = notes
		}
		if err := call("POST", "/review", request, &task); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s started: the reviewer checks %s.\nhi team wait %s waits for the verdict.\n", task.ID, args[0], task.ID)
	case "wait", "show":
		if len(args) != 1 {
			return usageError{"usage: hi team " + command + " <task>"}
		}
		wait := "0s"
		if command == "wait" {
			wait = "25m"
		}
		if err := call("GET", "/task?id="+url.QueryEscape(args[0])+"&wait="+wait, nil, &task); err != nil {
			return err
		}
		printTeamTask(task, stdout)
	case "tasks":
		var tasks []teamTask
		if err := call("GET", "/tasks", nil, &tasks); err != nil {
			return err
		}
		if len(tasks) == 0 {
			fmt.Fprintln(stdout, "No tasks yet.")
			return nil
		}
		printTeamTasks(tasks, 30, stdout)
	case "merge":
		if len(args) != 1 {
			return usageError{"usage: hi team merge <task>"}
		}
		if err := call("POST", "/merge", teamRequest{Task: args[0]}, &task); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Merged %s (%s) as %s.\n", task.ID, task.Title, task.Merged[:min(len(task.Merged), 12)])
		if task.Error != "" {
			fmt.Fprintln(stdout, task.Error)
		}
	case "stop":
		if len(args) != 1 {
			return usageError{"usage: hi team stop <task>"}
		}
		if err := call("POST", "/stop", teamRequest{Task: args[0]}, &task); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "Stopped %s.\n", task.ID)
	}
	return nil
}

func teamReadBrief(path string, stdin io.Reader) (string, error) {
	var data []byte
	var err error
	if path == "-" {
		data, err = io.ReadAll(io.LimitReader(stdin, teamMaxBrief+1))
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// printTeamTask is what the lead reads about one task.
func printTeamTask(task teamTask, w io.Writer) {
	fmt.Fprintf(w, "%s · %s (%s) · %s\n", task.ID, task.Role, agentDisplayName(task.Agent), task.Status)
	fmt.Fprintf(w, "Title: %s\n", task.Title)
	if task.Review != "" {
		fmt.Fprintf(w, "Reviews: %s at %s\n", task.Review, task.Commit[:min(len(task.Commit), 12)])
	}
	if task.On != "" {
		fmt.Fprintf(w, "Continues: %s\n", task.On)
	}
	if task.Branch != "" {
		fmt.Fprintf(w, "Branch: %s\n", task.Branch)
	}
	if task.Verdict != "" {
		fmt.Fprintf(w, "Verdict: %s\n", task.Verdict)
	}
	if task.Merged != "" {
		fmt.Fprintf(w, "Merged: %s\n", task.Merged[:min(len(task.Merged), 12)])
	}
	if task.Error != "" {
		fmt.Fprintf(w, "Error: %s\n", task.Error)
	}
	if teamTaskActive(task) {
		fmt.Fprintf(w, "Still working; hi team wait %s waits again.\n", task.ID)
		return
	}
	if report := task.Report; report != nil {
		if len(report.Report) > 0 && string(report.Report) != "null" {
			fmt.Fprintf(w, "Report: %s\n", string(report.Report))
		}
		if len(report.ChangedFiles) > 0 {
			fmt.Fprintf(w, "Changed: %s\n", strings.Join(report.ChangedFiles, ", "))
		}
		if stats := agentStats(*report); stats != "" {
			fmt.Fprintf(w, "Stats: %s\n", stats)
		}
		for _, warning := range report.Warnings {
			fmt.Fprintf(w, "Warning: %s\n", warning)
		}
		if text := strings.TrimSpace(agentReportBlock.ReplaceAllString(report.Text, "")); text != "" {
			fmt.Fprintf(w, "\n%s\n", text)
		}
	}
}
