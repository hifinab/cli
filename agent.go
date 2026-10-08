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
	"time"
)

// hi agent hands a task to a coding agent in a box, waits for it, and
// prints the same report whichever agent did the work. Without a task it
// opens an interactive session in a box. See docs/specs/approved/hi_agent.md.

// agentKinds are the agents hi agent runs, in the order it picks them.
var (
	agentKinds = map[string]bool{"claude": true, "codex": true}
	agentOrder = []string{"claude", "codex"}
)

// agentHosts are the hosts an agent needs beyond its box's network preset.
// Claude Code needs none: its requests go through the proxy's token
// listener.
var agentHosts = map[string][]string{
	// Codex with a ChatGPT sign-in, or an API key.
	"codex": {"chatgpt.com", "ab.chatgpt.com", "auth.openai.com", "api.openai.com"},
}

// agentResultDir is where the agent leaves its result, in the box's home
// folder.
const agentResultDir = ".hi-agent"

// agentContract is added to every task, so every agent ends with the same
// block.
const agentContract = `When you are finished, end your final message with exactly this block and nothing after it:

<<<REPORT
{"status": "complete|partial|blocked", "summary": "2-4 sentences on what you did", "tests": "pass|fail|not_run", "follow_ups": ["anything the caller should know or do next"]}
REPORT>>>`

var agentReportBlock = regexp.MustCompile(`(?s)<<<REPORT\s*(.*?)\s*REPORT>>>`)

// agentReport is what a run with a task ends with.
type agentReport struct {
	Name         string          `json:"name"`
	Agent        string          `json:"agent"`
	Status       string          `json:"status"` // running, done, or failed
	ExitCode     int             `json:"exit_code"`
	SessionID    string          `json:"session_id,omitempty"`
	TaskFile     string          `json:"task_file,omitempty"`
	Bundles      []string        `json:"bundles,omitempty"`
	Skills       []string        `json:"skills,omitempty"`
	Image        string          `json:"image,omitempty"`
	Text         string          `json:"text"`
	Report       json.RawMessage `json:"report"`
	Branch       string          `json:"branch,omitempty"`
	Folder       string          `json:"folder,omitempty"`
	ChangedFiles []string        `json:"changed_files"`
	Tokens       *agentTokens    `json:"tokens,omitempty"`
	Seconds      int             `json:"seconds,omitempty"`
	Steps        int             `json:"steps,omitempty"`
	Model        string          `json:"model,omitempty"`
	CostUSD      float64         `json:"cost_usd,omitempty"` // Claude Code's figure at API prices
	Warnings     []string        `json:"warnings"`
}

type agentTokens struct {
	Input  int `json:"input"` // with cached input
	Cached int `json:"cached"`
	Output int `json:"output"`
}

func runAgent(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printAgentUsage(stdout)
		return 0
	}
	kind, rest := "", args
	switch {
	case args[0] == "wait" || args[0] == "token":
		kind, rest = args[0], args[1:]
	case agentKinds[args[0]]:
		kind, rest = args[0], args[1:]
	}
	options, err := parseBoxOptions("agent", rest)
	if err != nil {
		fmt.Fprintf(stderr, "hi: %v\n\n", err)
		printAgentUsage(stderr)
		return 2
	}
	switch kind {
	case "wait":
		return exitCode(agentWait(options, stdout, stderr), stderr)
	case "token":
		return exitCode(agentTokenCommand(options, stdin, stdout), stderr)
	}
	return exitCode(startAgent(kind, options, stdin, stdout, stderr), stderr)
}

func printAgentUsage(w io.Writer) {
	fmt.Fprintln(w, `hi agent hands a task to a coding agent in a box (see hi box help) and
waits for its report: what it said, whether it worked, and what changed.

usage:
  hi agent [claude|codex] "<task>"  run a task on a new git worktree (outside git, in the folder) and wait for the report;
                                    without an agent, the first one installed and signed in
  hi agent [claude|codex] <brief.md>
                                    the task is the file's contents; - reads it from stdin
  hi agent claude|codex             an interactive session in a box
  hi agent wait <name> [--json]     wait for a run and print its report
  hi agent token claude             store a long-lived Claude token from claude setup-token

options:
  --detach               start the run and return; hi agent wait <name> gets the report
  --json                 the report as JSON on stdout
  --task-file <path>     the task from a file whose name doesn't end in .md
  --model <model>        the agent's model, such as opus or gpt-6.1-sol; without it, the model in your
                         own Claude Code or Codex settings, else opus for Claude Code
  --bundle a,b           attach bundles: their skills, and an image with what they need (hi bundle ls)
  --name, --network, --allow, --gpu, --data, --here, --image, --memory
                         the box's options, as for hi box

The agent works without permission prompts, on the branch hi-box/<name>.
While it works, hi box attach <name> follows it; hi box diff, stop, and rm
work on it as on any box.`)
}

func startAgent(kind string, options boxOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	task, err := agentTask(&options, stdin)
	if err != nil {
		return err
	}
	if task == "" && (options.detach || options.json) {
		return usageError{"--detach and --json need a task: hi agent [claude|codex] \"<task>\""}
	}
	// With --json, stdout holds only the report.
	info := stdout
	if options.json {
		info = stderr
	}
	if kind, err = applyFrontMatter(kind, &options, stdin, info); err != nil {
		return err
	}
	if kind == "" {
		if task == "" {
			return usageError{"name the agent for an interactive session: hi agent claude, or hi agent codex"}
		}
		if kind, err = chooseAgent(); err != nil {
			return err
		}
	} else if err := agentReady(kind); err != nil {
		return err
	}
	if options.model == "" {
		options.model = defaultAgentModel(kind)
	}
	if options.model != "" && !agentModelName.MatchString(options.model) {
		return fmt.Errorf("%q isn't a model name", options.model)
	}
	options.words = nil
	if task != "" {
		options.words = []string{task}
	}
	if options.taskFile != "" {
		fmt.Fprintf(info, "Task from %s.\n", options.taskFile)
	}
	meta, err := startBox(kind, options, stdin, info, stderr)
	if err != nil || !meta.Background {
		return err
	}
	if options.detach {
		if options.json {
			return printAgentJSON(agentReport{Name: meta.Name, Agent: meta.Agent, Status: "running", Branch: meta.Branch,
				Report: json.RawMessage("null"), Warnings: []string{}}, stdout)
		}
		fmt.Fprintf(info, "%s is working in the background.\n  hi agent wait %s   wait for its report\n  hi box attach %s   follow it\n", meta.Name, meta.Name, meta.Name)
		return nil
	}
	return finishAgent(meta, options.json, info, stdout)
}

// agentMaxTask is the largest task file hi agent reads.
const agentMaxTask = 1 << 20

// agentTask is the task: the words, or a file's contents when the only
// word ends in .md or --task-file names it, or stdin when the only word is
// -. A file's absolute path goes in options.taskFile for the report.
func agentTask(options *boxOptions, stdin io.Reader) (string, error) {
	words := options.words
	switch {
	case options.taskFile != "":
		if len(words) > 0 {
			return "", usageError{"give the task as --task-file or as words, not both"}
		}
	case len(words) == 1 && words[0] == "-":
		data, err := io.ReadAll(io.LimitReader(stdin, agentMaxTask+1))
		if err != nil {
			return "", fmt.Errorf("reading the task from stdin: %w", err)
		}
		task, err := checkAgentTask("the task on stdin", data)
		if err != nil {
			return "", err
		}
		return briefTask(options, task, "the task on stdin")
	case len(words) == 1 && strings.HasSuffix(strings.ToLower(words[0]), ".md"):
		options.taskFile = words[0]
	default:
		return strings.TrimSpace(strings.Join(words, " ")), nil
	}
	path, err := filepath.Abs(options.taskFile)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("there is no task file %s; a single word ending in .md is read as a task file", options.taskFile)
	}
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a folder, not a task file", options.taskFile)
	}
	if info.Size() > agentMaxTask {
		return "", fmt.Errorf("%s is %s; a task file can be at most 1 MB", options.taskFile, formatDataSize(info.Size()))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	options.taskFile = path
	task, err := checkAgentTask(options.taskFile, data)
	if err != nil {
		return "", err
	}
	return briefTask(options, task, options.taskFile)
}

// briefTask takes a brief's front matter off for applyFrontMatter.
func briefTask(options *boxOptions, task, source string) (string, error) {
	front, body, err := splitFrontMatter(task, source)
	if err != nil {
		return "", err
	}
	if front != nil && body == "" {
		return "", fmt.Errorf("%s has front matter but no task", source)
	}
	options.fromBrief = front
	return body, nil
}

func checkAgentTask(source string, data []byte) (string, error) {
	if len(data) > agentMaxTask {
		return "", fmt.Errorf("%s is over 1 MB; a task can be at most 1 MB", source)
	}
	task := strings.TrimSpace(string(data))
	if task == "" {
		return "", fmt.Errorf("%s is empty", source)
	}
	return task, nil
}

// chooseAgent is the first agent that is installed and signed in.
func chooseAgent() (string, error) {
	var problems []string
	for _, kind := range agentOrder {
		err := agentReady(kind)
		if err == nil {
			return kind, nil
		}
		problems = append(problems, err.Error())
	}
	return "", fmt.Errorf("no agent is ready: %s", strings.Join(problems, "; "))
}

// agentReady checks that an agent is installed and signed in on this
// machine, before a box is made for it.
func agentReady(kind string) error {
	switch kind {
	case "claude":
		if _, err := boxHostBinary("claude"); err != nil {
			return errors.New("Claude Code is not installed on this machine; install it with hi install claude")
		}
		_, err := boxClaudeSecret()
		return err
	case "codex":
		if _, err := boxHostBinary("codex"); err != nil {
			return errors.New("Codex is not installed on this machine; install it with hi install codex")
		}
		if !fileExists(codexAuthPath()) {
			return errors.New("Codex is not signed in on this machine; run codex once and sign in")
		}
		return nil
	}
	return fmt.Errorf("unknown agent %q", kind)
}

func codexAuthPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(firstNonEmpty(os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex")), "auth.json")
}

// syncCodexAuth copies a sign-in that Codex refreshed in the box back to
// this machine. A refresh token works once, so when a box refreshes the
// sign-in, the machine's copy stops working unless it gets the new one.
func syncCodexAuth(meta boxMeta) {
	if meta.Agent != "codex" {
		return
	}
	boxed, err := os.ReadFile(boxStateFile(meta.Name, "home", ".codex", "auth.json"))
	if err != nil {
		return
	}
	host, _ := os.ReadFile(codexAuthPath())
	if !codexLastRefresh(boxed).After(codexLastRefresh(host)) {
		return
	}
	temp := codexAuthPath() + ".hi-box"
	if os.WriteFile(temp, boxed, 0o600) != nil || os.Rename(temp, codexAuthPath()) != nil {
		os.Remove(temp)
	}
}

func codexLastRefresh(data []byte) time.Time {
	var auth struct {
		LastRefresh time.Time `json:"last_refresh"`
	}
	json.Unmarshal(data, &auth)
	return auth.LastRefresh
}

// agentBoxSetup prepares the box's home folder and environment for the
// agent, adds its mounts, and returns the box's command. A task gets the
// report contract, and the agent leaves its result in agentResultDir.
func agentBoxSetup(kind, prompt string, meta boxMeta, homeDir string, run *[]string, env map[string]string) ([]string, error) {
	results := "/box/home/" + agentResultDir
	if prompt != "" {
		// The task goes to the agent on stdin from a file, so its length
		// isn't limited by the command line.
		if err := os.MkdirAll(filepath.Join(homeDir, agentResultDir), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(homeDir, agentResultDir, "task.md"), []byte(prompt+"\n\n"+agentContract+"\n"), 0o600); err != nil {
			return nil, err
		}
	}
	switch kind {
	case "claude":
		binary, err := boxHostBinary("claude")
		if err != nil {
			return nil, errors.New("Claude Code is not installed on this machine; install it with hi install claude")
		}
		*run = append(*run, "-v", binary+":/usr/local/bin/claude:ro")
		env["CLAUDE_CODE_OAUTH_TOKEN"] = boxClaudePlacehold
		env["ANTHROPIC_BASE_URL"] = fmt.Sprintf("http://%s:%d", meta.ProxyIP, boxInjectPort)
		env["DISABLE_AUTOUPDATER"] = "1"
		env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] = "1"
		// Claude Code wants a temporary folder of its own; /tmp may hold
		// root-owned folders created for the box's mounts.
		env["CLAUDE_CODE_TMPDIR"] = "/box/home/.tmp"
		if err := os.MkdirAll(filepath.Join(homeDir, ".tmp"), 0o700); err != nil {
			return nil, err
		}
		state := map[string]any{
			"hasCompletedOnboarding":        true,
			"bypassPermissionsModeAccepted": true,
			"projects":                      map[string]any{meta.Workdir: map[string]any{"hasTrustDialogAccepted": true}},
		}
		data, _ := json.Marshal(state)
		if err := os.WriteFile(filepath.Join(homeDir, ".claude.json"), data, 0o600); err != nil {
			return nil, err
		}
		model := ""
		if meta.Model != "" {
			model = " --model " + shellQuote(meta.Model)
		}
		if prompt == "" {
			command := []string{"claude", "--dangerously-skip-permissions"}
			if meta.Model != "" {
				command = append(command, "--model", meta.Model)
			}
			return command, nil
		}
		// The JSON result goes to a file, and its final text to the log, so
		// hi box attach still shows the answer. Images without jq log the
		// JSON.
		script := `claude -p --output-format json --dangerously-skip-permissions` + model + ` < ` + results + `/task.md > ` + results + `/result.json; status=$?; ` +
			`if command -v jq >/dev/null 2>&1; then jq -r '.result // empty' ` + results + `/result.json; else cat ` + results + `/result.json; fi; exit $status`
		return []string{"sh", "-c", script}, nil
	case "codex":
		binary, err := boxHostBinary("codex")
		if err != nil {
			return nil, errors.New("Codex is not installed on this machine; install it with hi install codex")
		}
		*run = append(*run, "-v", filepath.Dir(filepath.Dir(binary))+":/opt/codex:ro")
		auth, err := os.ReadFile(codexAuthPath())
		if err != nil {
			return nil, errors.New("Codex is not signed in on this machine; run codex once and sign in")
		}
		if err := os.MkdirAll(filepath.Join(homeDir, ".codex"), 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(homeDir, ".codex", "auth.json"), auth, 0o600); err != nil {
			return nil, err
		}
		model := ""
		if meta.Model != "" {
			model = " -m " + shellQuote(meta.Model)
		}
		if prompt == "" {
			command := []string{"codex", "--dangerously-bypass-approvals-and-sandbox"}
			if meta.Model != "" {
				command = append(command, "-m", meta.Model)
			}
			return command, nil
		}
		return []string{"sh", "-c", "exec codex exec --dangerously-bypass-approvals-and-sandbox --skip-git-repo-check" + model + " -o " +
			results + "/last.txt - < " + results + "/task.md"}, nil
	}
	return nil, fmt.Errorf("unknown agent %q", kind)
}

// boxClaudeSecret is the file the proxy reads Claude's token from: one
// stored by hi agent token, or Claude Code's own credentials.
func boxClaudeSecret() (string, error) {
	if path := boxTokenPath(); fileExists(path) {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	path := filepath.Join(firstNonEmpty(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude")), ".credentials.json")
	if !fileExists(path) {
		return "", errors.New("Claude Code is not signed in on this machine; run claude and sign in, or store a token with hi agent token claude")
	}
	return path, nil
}

func boxTokenPath() string { return filepath.Join(qConfigDirectory(), "box-claude-token") }

// agentTokenCommand stores a long-lived token from claude setup-token,
// which the proxy prefers to the host's own sign-in, since that expires.
func agentTokenCommand(options boxOptions, stdin io.Reader, stdout io.Writer) error {
	if len(options.words) != 1 || options.words[0] != "claude" {
		return usageError{"usage: hi agent token claude"}
	}
	fmt.Fprintln(stdout, "Run claude setup-token in another terminal and paste the token it prints.")
	token, err := readOptionalKey("Claude", stdin, stdout, false)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(token, "sk-ant-") {
		return errors.New("that doesn't look like a token from claude setup-token (sk-ant-…)")
	}
	if err := os.MkdirAll(qConfigDirectory(), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(boxTokenPath(), []byte(token+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Stored in %s. Agents' proxies use it from the next run on; it never enters a box.\n", boxTokenPath())
	return nil
}

// ---------------------------------------------------------------------------
// waiting and the report

func agentWait(options boxOptions, stdout, stderr io.Writer) error {
	if len(options.words) != 1 {
		return usageError{"usage: hi agent wait <name> [--json]"}
	}
	meta, err := loadBoxMeta(options.words[0])
	if err != nil {
		return err
	}
	if !agentKinds[meta.Agent] || !meta.Background {
		return fmt.Errorf("%s is not an agent run with a task; hi box attach %s shows it", meta.Name, meta.Name)
	}
	info := stdout
	if options.json {
		info = stderr
	}
	return finishAgent(meta, options.json, info, stdout)
}

// finishAgent waits for the agent's box to stop, then prints its report.
// Ctrl+C ends hi, not the agent.
func finishAgent(meta boxMeta, asJSON bool, info, stdout io.Writer) error {
	engine := boxEngine{name: meta.Engine}
	var err error
	if engine.bin, err = boxLookPath(meta.Engine); err != nil {
		return fmt.Errorf("%s made this box but is not installed now", meta.Engine)
	}
	container := "hi-box-" + meta.Name
	if engine.state(container) == "running" {
		fmt.Fprintf(info, "%s is working in %s. hi box attach %s follows it; Ctrl+C stops waiting, not the agent.\n", agentDisplayName(meta.Agent), meta.Name, meta.Name)
		// The status line goes away before anything else is printed,
		// including after Ctrl+C.
		interrupts := make(chan os.Signal, 1)
		signal.Notify(interrupts, os.Interrupt)
		defer signal.Stop(interrupts)
		stop, waited := make(chan struct{}), make(chan struct{})
		status := showAgentStatus(info, meta, stop)
		go func() {
			engine.interactive(nil, io.Discard, io.Discard, "wait", container)
			close(waited)
		}()
		select {
		case <-waited:
			close(stop)
			<-status
		case <-interrupts:
			close(stop)
			<-status
			return exitStatusError{code: 130, message: fmt.Sprintf("stopped waiting; %s keeps working. hi agent wait %s waits again", meta.Name, meta.Name)}
		}
	}
	engine.output("stop", "-t", "2", container+"-proxy")
	syncCodexAuth(meta)
	report := collectAgentReport(engine, meta)
	if asJSON {
		if err := printAgentJSON(report, stdout); err != nil {
			return err
		}
	} else {
		printAgentReport(report, info)
	}
	if report.Status == "failed" {
		return exitStatusError{code: 1, message: fmt.Sprintf("%s failed (exit status %d); hi box attach %s shows its output", meta.Name, report.ExitCode, meta.Name)}
	}
	return nil
}

func agentDisplayName(kind string) string {
	if kind == "claude" {
		return "Claude Code"
	}
	return "Codex"
}

// collectAgentReport builds the report from the box: the agent's result
// files and log, the container's exit status, and git.
func collectAgentReport(engine boxEngine, meta boxMeta) agentReport {
	report := agentReport{Name: meta.Name, Agent: meta.Agent, Branch: meta.Branch, TaskFile: meta.TaskFile, Status: "done", Warnings: []string{},
		Bundles: meta.Bundles, Skills: meta.Skills}
	if len(meta.Bundles) > 0 {
		report.Image = meta.Image
	}
	container := "hi-box-" + meta.Name
	switch state := engine.state(container); state {
	case "running":
		report.Status = "running"
	case "":
		report.Warnings = append(report.Warnings, "the box's container is gone")
	default:
		out, _ := engine.output("container", "inspect", "--format", "{{.State.ExitCode}}", container)
		report.ExitCode, _ = strconv.Atoi(strings.TrimSpace(out))
	}
	results := boxStateFile(meta.Name, "home", agentResultDir)
	failed := false
	switch meta.Agent {
	case "claude":
		var result struct {
			Result    string  `json:"result"`
			SessionID string  `json:"session_id"`
			IsError   bool    `json:"is_error"`
			Cost      float64 `json:"total_cost_usd"`
			Usage     struct {
				Input       int `json:"input_tokens"`
				CacheRead   int `json:"cache_read_input_tokens"`
				CacheCreate int `json:"cache_creation_input_tokens"`
				Output      int `json:"output_tokens"`
			} `json:"usage"`
		}
		if data, err := os.ReadFile(filepath.Join(results, "result.json")); err == nil && json.Unmarshal(data, &result) == nil {
			report.Text, report.SessionID, failed, report.CostUSD = result.Result, result.SessionID, result.IsError, result.Cost
			if usage := result.Usage; usage.Input+usage.Output > 0 {
				report.Tokens = &agentTokens{Input: usage.Input + usage.CacheRead + usage.CacheCreate, Cached: usage.CacheRead, Output: usage.Output}
			}
		}
	case "codex":
		if data, err := os.ReadFile(filepath.Join(results, "last.txt")); err == nil {
			report.Text = string(data)
		}
		var logs bytes.Buffer
		boxCommand(nil, &logs, &logs, engine.bin, "logs", container)
		if match := regexp.MustCompile(`(?m)^session id: (\S+)`).FindStringSubmatch(logs.String()); match != nil {
			report.SessionID = match[1]
		}
	}
	report.Text, report.Report = splitAgentReport(report.Text)
	// The session log gives the steps and the model, and Codex's tokens.
	progress := newAgentProgressReader(meta.Agent, boxStateFile(meta.Name, "home")).poll()
	report.Steps, report.Model = progress.steps, progress.model
	if report.Tokens == nil && progress.in+progress.out > 0 {
		report.Tokens = &agentTokens{Input: progress.in, Cached: progress.cached, Output: progress.out}
	}
	if report.Status == "running" {
		return report
	}
	report.Seconds = containerSeconds(engine, container)
	if report.ExitCode != 0 || failed || strings.TrimSpace(report.Text) == "" && string(report.Report) == "null" {
		report.Status = "failed"
	}
	if string(report.Report) == "null" && report.Status == "done" {
		report.Warnings = append(report.Warnings, "the agent did not end with a report block")
	}
	report.ChangedFiles = agentChangedFiles(meta)
	if !meta.Worktree {
		report.Folder = meta.Root
		changes, ok, complete := folderChanges(meta.Name, meta.Root)
		for _, change := range changes {
			report.ChangedFiles = append(report.ChangedFiles, change.Path)
		}
		if ok && !complete {
			report.Warnings = append(report.Warnings, "the folder has too many files to list every change")
		}
	}
	return report
}

// splitAgentReport takes the last report block out of the agent's final
// message. A block that isn't JSON stays in the text.
func splitAgentReport(text string) (string, json.RawMessage) {
	matches := agentReportBlock.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return strings.TrimSpace(text), json.RawMessage("null")
	}
	last := matches[len(matches)-1]
	block := strings.TrimSpace(text[last[2]:last[3]])
	var compact bytes.Buffer
	if json.Compact(&compact, []byte(block)) != nil {
		return strings.TrimSpace(text), json.RawMessage("null")
	}
	return strings.TrimSpace(text[:last[0]] + text[last[1]:]), json.RawMessage(compact.Bytes())
}

// agentChangedFiles is every file changed since the box started:
// committed, uncommitted, and new.
func agentChangedFiles(meta boxMeta) []string {
	files := []string{}
	if !meta.Worktree || meta.Base == "" || !fileExists(meta.Workdir) {
		return files
	}
	seen := map[string]bool{}
	changed := boxGit(meta.Workdir, "diff", "--name-only", meta.Base)
	for _, path := range append(strings.Split(changed, "\n"), boxUntracked(meta.Workdir)...) {
		if path != "" && !seen[path] {
			seen[path] = true
			files = append(files, path)
		}
	}
	sort.Strings(files)
	return files
}

func printAgentJSON(report agentReport, stdout io.Writer) error {
	if report.ChangedFiles == nil {
		report.ChangedFiles = []string{}
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(stdout, string(data))
	return err
}

func printAgentReport(report agentReport, w io.Writer) {
	if report.Text != "" {
		fmt.Fprintf(w, "\n%s\n", report.Text)
	}
	var contract struct {
		Status    string   `json:"status"`
		FollowUps []string `json:"follow_ups"`
	}
	json.Unmarshal(report.Report, &contract)
	if contract.Status == "partial" || contract.Status == "blocked" {
		fmt.Fprintf(w, "\nThe agent says its work is %s.\n", contract.Status)
	}
	if len(contract.FollowUps) > 0 {
		fmt.Fprintln(w, "\nFollow-ups")
		for _, item := range contract.FollowUps {
			fmt.Fprintf(w, "  - %s\n", item)
		}
	}
	for _, warning := range report.Warnings {
		fmt.Fprintf(w, "\nNote: %s.\n", warning)
	}
	if stats := agentStats(report); stats != "" {
		fmt.Fprintf(w, "\n%s\n", stats)
	}
	if report.Folder != "" {
		if len(report.ChangedFiles) == 0 {
			fmt.Fprintf(w, "\nNo files changed in %s. hi box rm %s removes the box.\n", report.Folder, report.Name)
			return
		}
		fmt.Fprintf(w, "\nChanged in %s:\n", report.Folder)
		for _, path := range report.ChangedFiles {
			fmt.Fprintf(w, "  %s\n", path)
		}
		fmt.Fprintf(w, "hi box diff %s shows what was added, changed, or removed; hi box rm %s removes the box and keeps the files.\n", report.Name, report.Name)
		return
	}
	if report.Branch == "" {
		return
	}
	if len(report.ChangedFiles) == 0 {
		fmt.Fprintf(w, "\nNo files changed. hi box rm %s removes the box.\n", report.Name)
		return
	}
	fmt.Fprintf(w, "\nChanged on %s:\n", report.Branch)
	for _, path := range report.ChangedFiles {
		fmt.Fprintf(w, "  %s\n", path)
	}
	fmt.Fprintf(w, "Review with hi box diff %s, keep with git merge %s, then hi box rm %s.\n", report.Name, report.Branch, report.Name)
}

// agentFrontMatter is what a task file's front matter may set: options the
// command line also takes. Flags win.
type agentFrontMatter struct {
	Agent   string
	Model   string
	Bundles []string
	Network string
	Allow   []string
	Data    bool
	GPU     bool
}

// splitFrontMatter takes the front matter off a brief: lines of key: value
// between two --- lines at the top. Lists are [a, b] or a, b.
func splitFrontMatter(task, source string) (*agentFrontMatter, string, error) {
	rest, ok := strings.CutPrefix(task, "---\n")
	if !ok {
		return nil, task, nil
	}
	head, body, found := strings.Cut(rest, "\n---\n")
	if !found {
		if head, found = strings.CutSuffix(rest, "\n---"); !found {
			return nil, task, nil
		}
		body = ""
	}
	front := &agentFrontMatter{}
	for _, line := range strings.Split(head, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, "", fmt.Errorf("%s: front matter line %q isn't key: value", source, line)
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		list := func() []string {
			var items []string
			for _, item := range strings.Split(strings.Trim(value, "[]"), ",") {
				if item = strings.Trim(strings.TrimSpace(item), `"'`); item != "" {
					items = append(items, item)
				}
			}
			return items
		}
		flag := func() (bool, error) {
			switch strings.ToLower(value) {
			case "true", "yes":
				return true, nil
			case "false", "no":
				return false, nil
			}
			return false, fmt.Errorf("%s: %s is true or false, not %q", source, key, value)
		}
		var err error
		switch key {
		case "agent":
			front.Agent = strings.Trim(value, `"'`)
			if !agentKinds[front.Agent] {
				return nil, "", fmt.Errorf("%s: agent is claude or codex, not %q", source, front.Agent)
			}
		case "model":
			front.Model = strings.Trim(value, `"'`)
			if !agentModelName.MatchString(front.Model) {
				return nil, "", fmt.Errorf("%s: %q isn't a model name", source, front.Model)
			}
		case "bundles", "bundle":
			front.Bundles = list()
		case "network":
			front.Network = strings.Trim(value, `"'`)
			if _, ok := boxPresets[front.Network]; !ok {
				return nil, "", fmt.Errorf("%s: network is locked, dev, or open, not %q", source, front.Network)
			}
		case "allow":
			front.Allow = list()
		case "data":
			front.Data, err = flag()
		case "gpu":
			front.GPU, err = flag()
		default:
			return nil, "", fmt.Errorf("%s: front matter can set agent, model, bundles, network, allow, data, and gpu, not %s", source, key)
		}
		if err != nil {
			return nil, "", err
		}
	}
	return front, strings.TrimSpace(body), nil
}

// applyFrontMatter uses a brief's front matter where no flag was given.
// A brief can come from anyone, including another agent, so what widens
// the box (network, allow, data, gpu) is asked about, and left out without
// a terminal.
func applyFrontMatter(kind string, options *boxOptions, stdin io.Reader, stdout io.Writer) (string, error) {
	front := options.fromBrief
	if front == nil {
		return kind, nil
	}
	if kind == "" {
		kind = front.Agent
	}
	if len(options.bundles) == 0 {
		options.bundles = front.Bundles
	}
	if options.model == "" {
		options.model = front.Model
	}
	var asks, flags []string
	var apply []func()
	if front.Network != "" && options.network == "" && front.Network != "dev" {
		asks, flags = append(asks, "network "+front.Network), append(flags, "--network "+front.Network)
		apply = append(apply, func() { options.network = front.Network })
	} else if front.Network == "dev" && options.network == "" {
		options.network = "dev"
	}
	for _, host := range front.Allow {
		if !containsString(options.allow, host) {
			asks, flags = append(asks, host), append(flags, "--allow "+host)
			apply = append(apply, func() { options.allow = append(options.allow, host) })
		}
	}
	if front.Data && !options.data {
		asks, flags = append(asks, "the team's data"), append(flags, "--data")
		apply = append(apply, func() { options.data = true })
	}
	if front.GPU && !options.gpu {
		asks, flags = append(asks, "the GPU"), append(flags, "--gpu")
		apply = append(apply, func() { options.gpu = true })
	}
	if len(asks) == 0 {
		return kind, nil
	}
	source := firstNonEmpty(options.taskFile, "the task")
	if !isTerminal(stdin) {
		fmt.Fprintf(stdout, "Ignoring %s from %s's front matter; add %s to allow it.\n", strings.Join(asks, ", "), source, strings.Join(flags, " "))
		return kind, nil
	}
	fmt.Fprintf(stdout, "%s asks for %s. Allow it for this box? [y/N] ", source, strings.Join(asks, ", "))
	if answer := readSkillAnswer(stdin); answer == "y" || answer == "yes" {
		for _, set := range apply {
			set()
		}
	} else {
		fmt.Fprintln(stdout, "Starting without them.")
	}
	return kind, nil
}

// agentStats is the report's closing line: how long the run took, its
// steps, tokens, model, and, for Claude Code, its cost at API prices.
func agentStats(report agentReport) string {
	var parts []string
	if report.Seconds > 0 {
		parts = append(parts, formatAgentElapsed(time.Duration(report.Seconds)*time.Second))
	}
	if report.Steps > 0 {
		parts = append(parts, plural(report.Steps, "step"))
	}
	if tokens := report.Tokens; tokens != nil {
		in := formatTokenCount(tokens.Input) + " tokens in"
		if tokens.Cached > 0 {
			in += " (" + formatTokenCount(tokens.Cached) + " cached)"
		}
		parts = append(parts, in, formatTokenCount(tokens.Output)+" out")
	}
	if report.Model != "" {
		parts = append(parts, report.Model)
	}
	if report.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f at API prices", report.CostUSD))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Took " + strings.Join(parts, " · ")
}

// containerSeconds is how long a finished container ran.
func containerSeconds(engine boxEngine, container string) int {
	out, err := engine.output("container", "inspect", "--format", "{{.State.StartedAt}}|{{.State.FinishedAt}}", container)
	if err != nil {
		return 0
	}
	times := strings.SplitN(strings.TrimSpace(out), "|", 2)
	if len(times) != 2 {
		return 0
	}
	parse := func(text string) time.Time {
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999 -0700 MST"} {
			if t, err := time.Parse(layout, text); err == nil {
				return t
			}
		}
		return time.Time{}
	}
	started, finished := parse(times[0]), parse(times[1])
	if started.IsZero() || finished.Before(started) {
		return 0
	}
	return int(finished.Sub(started).Round(time.Second).Seconds())
}

// agentModelName is what --model and a brief's model: accept: a name or
// alias such as opus, claude-opus-5-5, opus[1m], or gpt-6.1-sol.
var agentModelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,99}$`)

// defaultAgentModel is the model a box's agent uses without --model: the
// one in your own settings, so a box behaves like the agent on this
// machine. Claude Code in a box can't see which plan you're on, since only
// a placeholder token is there, so it would pick its plan-less default;
// opus stands in for that. Codex keeps its own default.
func defaultAgentModel(kind string) string {
	home, _ := os.UserHomeDir()
	switch kind {
	case "claude":
		var settings struct {
			Model string `json:"model"`
		}
		configDir := firstNonEmpty(os.Getenv("CLAUDE_CONFIG_DIR"), filepath.Join(home, ".claude"))
		if data, err := os.ReadFile(filepath.Join(configDir, "settings.json")); err == nil && json.Unmarshal(data, &settings) == nil && agentModelName.MatchString(settings.Model) {
			return settings.Model
		}
		if model := os.Getenv("ANTHROPIC_MODEL"); agentModelName.MatchString(model) {
			return model
		}
		return "opus"
	case "codex":
		data, err := os.ReadFile(filepath.Join(firstNonEmpty(os.Getenv("CODEX_HOME"), filepath.Join(home, ".codex")), "config.toml"))
		if err != nil {
			return ""
		}
		// Only the top-level model, before the first [table].
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") {
				break
			}
			if match := codexConfigModel.FindStringSubmatch(line); match != nil && agentModelName.MatchString(match[1]) {
				return match[1]
			}
		}
	}
	return ""
}

var codexConfigModel = regexp.MustCompile(`^model\s*=\s*["']([^"']+)["']`)
