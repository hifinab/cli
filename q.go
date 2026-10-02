package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// qOpenTTY opens the terminal for confirmations when stdin is a pipe. Tests
// replace it.
var qOpenTTY = func() (io.ReadWriteCloser, error) { return os.OpenFile("/dev/tty", os.O_RDWR, 0) }

type qOptions struct {
	print     bool
	explain   string
	noContext bool
	provider  string
	model     string
	yes       bool
	prompt    string
	action    string // setup, status, context, or help
}

func runQ(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Words are always the question; hi's own actions are --options before
	// it, so "hi q status of the log" asks instead of showing the status.
	options, err := parseQOptions(args)
	if err != nil {
		fmt.Fprintf(stderr, "hi: %v\n\n", err)
		printQUsage(stderr)
		return 2
	}
	if options.action != "" && options.action != "help" && (options.prompt != "" || options.explain != "") {
		fmt.Fprintf(stderr, "hi: --%s takes no question\n", options.action)
		return 2
	}
	switch options.action {
	case "setup":
		return exitCode(qSetupCommand(stdin, stdout), stderr)
	case "status":
		return exitCode(qStatusCommand(stdout), stderr)
	case "context":
		fmt.Fprintln(stdout, "Sent with each question, after redaction:")
		fmt.Fprintln(stdout)
		fmt.Fprint(stdout, gatherQContext("").render())
		return 0
	case "help":
		printQUsage(stdout)
		return 0
	}
	if options.prompt == "" && options.explain == "" {
		fmt.Fprintln(stderr, "hi q needs a question for now; the chat that opens without one comes in v0.20.0.")
		fmt.Fprintln(stderr)
		printQUsage(stderr)
		return 2
	}

	// A terminal on stdin answers the confirmation. A pipe on stdin is
	// context, and the confirmation comes from /dev/tty instead.
	var keys io.Reader
	var piped string
	var ttyOut io.Writer
	if isTerminal(stdin) {
		keys = stdin
		ttyOut = stdout
	} else {
		if qReadablePipe(stdin) {
			piped = readQPipe(stdin)
		}
		if tty, err := qOpenTTY(); err == nil {
			defer tty.Close()
			keys = tty
			ttyOut = tty
		}
	}

	choice, err := resolveQProvider(options.provider, options.model)
	if err != nil {
		fmt.Fprintf(stderr, "hi: %v\n", err)
		return 1
	}
	if choice.provider == nil {
		if keys == nil || options.print {
			fmt.Fprintln(stderr, "hi: no model is set up; run hi q --setup, or set OPENAI_API_KEY or HI_Q_BASE_URL and HI_Q_MODEL")
			return 1
		}
		fmt.Fprintln(stdout, "hi q needs a model first.")
		choice, err = runQSetup(newMenuUI(keys, stdout))
		if err != nil {
			return exitCode(err, stderr)
		}
	}

	session := qSession{
		options: options,
		choice:  choice,
		keys:    keys,
		ttyOut:  ttyOut,
		stdout:  stdout,
		stderr:  stderr,
		styled:  isTerminal(keys) && qIsTerminalWriter(stdout),
	}
	if options.print {
		session.styled = false
	}
	return session.run(piped)
}

func parseQOptions(args []string) (qOptions, error) {
	var options qOptions
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func() (string, error) {
			if _, after, ok := strings.Cut(arg, "="); ok {
				return after, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", arg)
			}
			i++
			return args[i], nil
		}
		var err error
		switch {
		case arg == "--":
			options.prompt = strings.Join(args[i+1:], " ")
			return options, nil
		case arg == "--setup" || arg == "--status" || arg == "--context":
			options.action = strings.TrimPrefix(arg, "--")
		case arg == "--help" || arg == "-h":
			options.action = "help"
		case arg == "--print" || arg == "-p":
			options.print = true
		case arg == "--no-context":
			options.noContext = true
		case arg == "--yes" || arg == "-y":
			options.yes = true
		case arg == "--explain" || strings.HasPrefix(arg, "--explain="):
			options.explain, err = value()
		case arg == "--provider" || strings.HasPrefix(arg, "--provider="):
			options.provider, err = value()
		case arg == "--model" || strings.HasPrefix(arg, "--model="):
			options.model, err = value()
		case strings.HasPrefix(arg, "-"):
			return options, fmt.Errorf("unknown option %s; put -- before a question that starts with a dash", arg)
		default:
			options.prompt = strings.Join(args[i:], " ")
			return options, nil
		}
		if err != nil {
			return options, err
		}
	}
	return options, nil
}

func printQUsage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  hi q <what you want to do>     propose a command to run, copy, or explain
  <cmd> 2>&1 | hi q <question>   ask about piped output
  hi q --explain '<command>'     explain a command without running it
  hi q --print <prompt>          print only the command, for scripts
  hi q --setup                   choose the model
  hi q --status                  show the model in use
  hi q --context                 show what is sent with each question

options:
  --provider openai|openrouter|anthropic|claude
                                       use this provider once
  --model <name>                       use this model once
  --no-context                         send only the prompt
  --yes                                run read-only commands without asking

Options go before the question; every word after the first one that
doesn't start with - is the question. Use -- before a question that starts
with a dash. Quote a question with * or ? in it, or the shell expands them.`)
}

// ---------------------------------------------------------------------------
// one question

type qSession struct {
	options qOptions
	choice  qChoice
	keys    io.Reader
	ttyOut  io.Writer
	stdout  io.Writer
	stderr  io.Writer
	styled  bool
}

func (s qSession) run(piped string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var contextText string
	if !s.options.noContext {
		contextText = gatherQContext(piped).render()
	} else if piped != "" {
		contextText = "piped input:\n" + redactQText(piped)
	}
	system := qSystemPrompt()
	var user string
	if s.options.explain != "" {
		user = qExplainRequest(s.options.explain, contextText)
	} else {
		user = qUserMessage(s.options.prompt, contextText)
	}

	reply, err := s.ask(ctx, system, user)
	if err != nil {
		return exitCode(err, s.stderr)
	}
	if s.options.explain != "" && reply.Kind == "command" {
		reply = qReply{Kind: "answer", Answer: firstNonEmpty(reply.Reason, reply.Command)}
	}
	if reply.Kind == "answer" {
		fmt.Fprintln(s.stdout, strings.TrimSpace(reply.Answer))
		return 0
	}

	home, _ := os.UserHomeDir()
	assessment := assessCommand(reply.Command, home)
	if modelRisk := parseQRisk(reply.Risk); modelRisk > assessment.risk {
		assessment.risk = modelRisk
		if modelRisk == qDangerous && len(assessment.reasons) == 0 {
			assessment.reasons = append(assessment.reasons, "the model marked it dangerous")
		}
	}

	if s.options.print {
		fmt.Fprintln(s.stdout, reply.Command)
		if assessment.risk == qDangerous {
			fmt.Fprintf(s.stderr, "hi: dangerous: %s\n", strings.Join(assessment.reasons, "; "))
		}
		return 0
	}

	s.showProposal(reply, assessment)
	if s.keys == nil {
		fmt.Fprintln(s.stdout, "No terminal to confirm in; copy the command, or use --print.")
		return 0
	}
	if s.options.yes && assessment.risk == qReadOnly {
		return s.execute(reply.Command, assessment.risk)
	}
	for {
		s.showKeys(assessment.risk)
		key := qReadKey(s.keys)
		switch key {
		case "enter":
			if assessment.risk == qDangerous {
				fmt.Fprint(s.stdout, s.style(colorRed, true).Render("Type yes to run it: "))
				answer, _ := readLine(s.keys)
				if strings.ToLower(strings.TrimSpace(answer)) != "yes" {
					fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("Not run."))
					return 0
				}
			}
			return s.execute(reply.Command, assessment.risk)
		case "c":
			s.copy(reply.Command)
			return 0
		case "?":
			explained, err := s.ask(ctx, system, qExplainRequest(reply.Command, contextText))
			if err != nil {
				fmt.Fprintf(s.stderr, "hi: %v\n", err)
				continue
			}
			fmt.Fprintln(s.stdout, qIndent(strings.TrimSpace(firstNonEmpty(explained.Answer, explained.Reason)), "  "))
			fmt.Fprintln(s.stdout)
		case "esc", "q", "n", "ctrl-c":
			fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("Not run."))
			return 0
		}
	}
}

func (s qSession) ask(ctx context.Context, system, user string) (qReply, error) {
	ctx, cancel := context.WithTimeout(ctx, qRequestTimeout)
	defer cancel()
	var reply qReply
	var err error
	work := func() { reply, err = s.choice.provider.ask(ctx, system, user) }
	if file, ok := s.stderr.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		(&styledUI{out: s.stderr}).busy("Asking "+s.choice.provider.label()+"…", work)
	} else {
		work()
	}
	if ctx.Err() != nil && errors.Is(ctx.Err(), context.Canceled) {
		return reply, errors.New("cancelled")
	}
	return reply, err
}

func (s qSession) style(color lipgloss.TerminalColor, bold bool) lipgloss.Style {
	if !s.styled {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(color).Bold(bold)
}

func (s qSession) showProposal(reply qReply, assessment qAssessment) {
	riskColor := map[qRisk]lipgloss.TerminalColor{qReadOnly: colorMint, qChanges: colorAmber, qDangerous: colorRed}[assessment.risk]
	if s.styled {
		fmt.Fprintln(s.stdout, lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(riskColor).
			Padding(0, 1).Foreground(colorText).Bold(true).
			Render(reply.Command))
	} else {
		fmt.Fprintln(s.stdout, qIndent(reply.Command, "  $ "))
	}
	if reply.Reason != "" {
		fmt.Fprintln(s.stdout, s.style(colorText, false).Render(qIndent(strings.TrimSpace(reply.Reason), "  ")))
	}
	for _, match := range assessment.matches {
		fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  "+match))
	}
	label := assessment.risk.String()
	if len(assessment.reasons) > 0 {
		label += ": " + strings.Join(assessment.reasons, "; ")
	}
	fmt.Fprintln(s.stdout, s.style(riskColor, true).Render("  "+label))
}

func (s qSession) showKeys(risk qRisk) {
	run := "enter run"
	if risk == qDangerous {
		run = "enter run (asks for yes)"
	}
	fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  "+run+" · c copy · ? explain · esc cancel"))
}

// execute runs the command in the user's own shell, in the current folder,
// and passes its exit status through.
func (s qSession) execute(command string, risk qRisk) int {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if _, err := exec.LookPath(shell); err != nil {
		shell = "/bin/sh"
	}
	process := exec.Command(shell, "-c", command)
	if file, ok := s.keys.(*os.File); ok {
		process.Stdin = file
	}
	process.Stdout = s.stdout
	process.Stderr = s.stderr
	// Ctrl-C while it runs is for the command, not for hi.
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	err := process.Run()
	status := 0
	var exitErr *exec.ExitError
	switch {
	case errors.As(err, &exitErr):
		status = exitErr.ExitCode()
		if waitStatus, ok := exitErr.Sys().(syscall.WaitStatus); ok && waitStatus.Signaled() {
			status = 128 + int(waitStatus.Signal())
		}
	case err != nil:
		fmt.Fprintf(s.stderr, "hi: %v\n", err)
		status = 1
	}
	if status != 0 {
		fmt.Fprintln(s.stderr, s.style(colorRed, false).Render(fmt.Sprintf("exit status %d", status)))
	}
	logQCommand(s.options.prompt, command, risk, status)
	return status
}

func (s qSession) copy(text string) {
	for _, tool := range [][]string{{"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}, {"pbcopy"}} {
		if tool[0] == "wl-copy" && os.Getenv("WAYLAND_DISPLAY") == "" {
			continue
		}
		if (tool[0] == "xclip" || tool[0] == "xsel") && os.Getenv("DISPLAY") == "" {
			continue
		}
		if _, err := exec.LookPath(tool[0]); err != nil {
			continue
		}
		command := exec.Command(tool[0], tool[1:]...)
		command.Stdin = strings.NewReader(text)
		if command.Run() == nil {
			fmt.Fprintln(s.stdout, s.style(colorMint, false).Render("Copied."))
			return
		}
	}
	// OSC 52 asks the terminal itself to copy, which also works over SSH.
	out := s.ttyOut
	if out == nil {
		out = s.stdout
	}
	fmt.Fprintf(out, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
	fmt.Fprintln(s.stdout, s.style(colorMint, false).Render("Sent to the terminal's clipboard (if it allows OSC 52)."))
}

// qReadKey reads one key in raw mode from a terminal, or one line otherwise.
func qReadKey(in io.Reader) string {
	file, ok := in.(*os.File)
	if ok && term.IsTerminal(int(file.Fd())) {
		state, err := term.MakeRaw(int(file.Fd()))
		if err == nil {
			defer term.Restore(int(file.Fd()), state)
			var buffer [8]byte
			for {
				n, err := file.Read(buffer[:])
				if err != nil || n == 0 {
					return "esc"
				}
				switch {
				case n == 1 && (buffer[0] == '\r' || buffer[0] == '\n'):
					return "enter"
				case n == 1 && buffer[0] == 0x1b:
					return "esc"
				case n == 1 && buffer[0] == 0x03:
					return "ctrl-c"
				case n == 1 && buffer[0] == 0x04:
					return "esc"
				case n == 1 && buffer[0] < 0x80:
					return strings.ToLower(string(buffer[0]))
				}
				// Arrow keys and other sequences are ignored.
			}
		}
	}
	line, err := readLine(in)
	line = strings.ToLower(strings.TrimSpace(line))
	switch {
	case line == "" && err != nil:
		return "esc"
	case line == "" || line == "y" || line == "yes" || line == "r" || line == "run":
		return "enter"
	}
	return line[:1]
}

func qReadablePipe(r io.Reader) bool {
	file, ok := r.(*os.File)
	if !ok {
		return r != nil
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeNamedPipe != 0 || info.Mode().IsRegular()
}

func qIsTerminalWriter(w io.Writer) bool {
	file, ok := w.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func qIndent(text, prefix string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = prefix + line
		} else {
			lines[i] = strings.Repeat(" ", len([]rune(prefix))) + line
		}
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// prompts

func qSystemPrompt() string {
	var usage bytes.Buffer
	printUsage(&usage)
	return `You are hi q, a terminal helper. The user describes what they want in plain words; you reply with one shell command for their shell and system, or a short answer when they only asked a question.

When they want something done, call the propose tool (or reply with JSON {"kind":"command",...} if you have no tools) with:
- command: the exact command. Prefer one line; use a short script only when one line would be unreadable.
- reason: one short sentence saying what it does, in plain words.
- risk: "read-only" if it changes nothing, "changes" if it creates, moves, or edits files, "dangerous" if it deletes data, needs root, or is hard to undo.
When they ask a question, reply with a short plain-text answer and no command.

Write commands well:
- Quote paths and variables. Put -- before file arguments where the command supports it.
- Use find -print0 with xargs -0, or find -exec, for names with spaces.
- Prefer mkdir -p, mv -n, and cp -n over overwriting. Never delete more than asked.
- Use flags that exist on the user's system: GNU or BSD core tools as stated in the context.
- Use the tools the context lists as installed (rg, fd, jq, ...) when they make the command simpler, otherwise standard tools.
- Use the files and folders in the context; don't invent names. If the request is ambiguous in a way that matters, answer with a question instead of guessing at a destructive reading.
- Don't add sudo unless the task truly needs root.
- Write for the user's shell (bash, zsh, or fish as stated).

The user's machine has hi, the team's CLI. Use it when it fits:
` + usage.String()
}

func qUserMessage(prompt, contextText string) string {
	if contextText == "" {
		return prompt
	}
	return prompt + "\n\n<context>\n" + contextText + "</context>"
}

func qExplainRequest(command, contextText string) string {
	request := "Explain this command part by part, briefly, as a plain-text answer. Say what it changes and anything surprising or risky. Don't propose a different command unless it is wrong.\n\n" + command
	if contextText == "" {
		return request
	}
	return request + "\n\n<context>\n" + contextText + "</context>"
}

// ---------------------------------------------------------------------------
// status and log

func qStatusCommand(stdout io.Writer) error {
	choice, err := resolveQProvider("", "")
	if err != nil {
		return err
	}
	if choice.provider == nil {
		fmt.Fprintln(stdout, "Model: none. Run hi q --setup.")
	} else {
		fmt.Fprintf(stdout, "Model:   %s\nFrom:    %s\n", choice.provider.label(), choice.source)
	}
	fmt.Fprintf(stdout, "Config:  %s\n", filepath.Join(qConfigDirectory(), "q.json"))
	if path, err := qLogPath(); err == nil {
		fmt.Fprintf(stdout, "Log:     %s\n", path)
	}
	fmt.Fprintln(stdout, "History: read from the shell's history file; shell integration comes in v0.20.0")
	return nil
}

func qLogPath() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "hi", "q", "log.jsonl"), nil
}

// logQCommand records a command that ran. The log stays on this machine and
// never holds the command's output.
func logQCommand(prompt, command string, risk qRisk, status int) {
	path, err := qLogPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	folder, _ := os.Getwd()
	entry, err := json.Marshal(map[string]any{
		"time":    time.Now().UTC().Format(time.RFC3339),
		"folder":  folder,
		"prompt":  redactQText(prompt),
		"command": redactQText(command),
		"risk":    risk.String(),
		"exit":    status,
	})
	if err != nil {
		return
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	file.Write(append(entry, '\n'))
}
