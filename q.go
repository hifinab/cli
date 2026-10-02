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

const qMaxFixes = 3

type qOptions struct {
	print     bool
	explain   string
	noContext bool
	provider  string
	model     string
	yes       bool
	resume    bool
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
	chat := options.prompt == "" && options.explain == ""
	if chat && options.print {
		fmt.Fprintln(stderr, "hi: --print needs a question")
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
	if chat && keys == nil {
		fmt.Fprintln(stderr, "hi: the chat needs a terminal; ask a question instead: hi q <question>")
		return 2
	}

	choice, err := resolveQProvider(options.provider, options.model)
	if err != nil {
		fmt.Fprintf(stderr, "hi: %v\n", err)
		return 1
	}
	if choice.provider == nil {
		if keys == nil || options.print {
			fmt.Fprintln(stderr, "hi: no model is set up; run hi q --setup, or set OPENAI_API_KEY, OPENROUTER_API_KEY, or HI_Q_BASE_URL and HI_Q_MODEL")
			return 1
		}
		fmt.Fprintln(stdout, "hi q needs a model first.")
		choice, err = runQSetup(newMenuUI(keys, stdout))
		if err != nil {
			return exitCode(err, stderr)
		}
	}

	session := &qSession{
		options: options,
		choice:  choice,
		keys:    keys,
		ttyOut:  ttyOut,
		stdout:  stdout,
		stderr:  stderr,
		styled:  isTerminal(keys) && qIsTerminalWriter(stdout),
		link:    currentQShellLink(),
	}
	if options.print {
		session.styled = false
	}
	if options.resume {
		session.turns = loadQConversation()
	}
	if !options.noContext {
		session.contextText = gatherQContext(piped).render()
	} else if piped != "" {
		session.contextText = "piped input:\n" + redactQText(piped)
	}
	if chat {
		return session.chat()
	}
	if options.explain != "" {
		return session.explainOnly()
	}
	return session.once()
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
		case arg == "--continue" || arg == "-c":
			options.resume = true
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
  hi q                           chat: ask, run, and follow up; Ctrl-D leaves
  hi q <what you want to do>     propose a command to run, copy, or explain
  hi q -c [question]             continue the last conversation
  <cmd> 2>&1 | hi q <question>   ask about piped output
  hi q --explain '<command>'     explain a command without running it
  hi q --print <question>        print only the command, for scripts
  hi q --setup                   choose the model and set up the shell
  hi q --status                  show the model in use
  hi q --context                 show what is sent with each question

options:
  --provider openai|openrouter|anthropic|claude
                                       use this provider once
  --model <name>                       use this model once
  --no-context                         send only the question
  --yes                                run read-only commands without asking

Options go before the question; every word from the first one that doesn't
start with - is the question. Use -- before a question that starts with a
dash. Without shell integration, quote a question that has ( ) * ? or ' in
it, or type it in the chat (hi q) instead.`)
}

// ---------------------------------------------------------------------------
// a session: one question, or a chat

type qSession struct {
	options     qOptions
	choice      qChoice
	keys        io.Reader
	ttyOut      io.Writer
	stdout      io.Writer
	stderr      io.Writer
	styled      bool
	link        *qShellLink
	turns       []qTurn
	contextText string
	contextSent bool
	handedOff   bool
}

// once answers one question and handles the proposed command.
func (s *qSession) once() int {
	s.addUserTurn(s.options.prompt)
	status := s.respond()
	saveQConversation(s.turns)
	return status
}

// explainOnly explains a command without the conversation or tools.
func (s *qSession) explainOnly() int {
	reply, err := s.askOnce(qExplainRequest(s.options.explain, s.contextText))
	if err != nil {
		return exitCode(err, s.stderr)
	}
	fmt.Fprintln(s.stdout, s.markdown(firstNonEmpty(reply.Answer, reply.Reason, reply.Command)))
	return 0
}

// markdown renders an answer's Markdown as terminal styles, and leaves it
// as written when the output is not a terminal.
func (s *qSession) markdown(text string) string {
	text = strings.TrimSpace(text)
	if !s.styled {
		return text
	}
	return qMarkdown{color: true}.render(text)
}

func (s *qSession) addUserTurn(text string) {
	if !s.contextSent && s.contextText != "" {
		text = qUserMessage(text, s.contextText)
		s.contextSent = true
	}
	s.turns = append(s.turns, qTurn{Role: "user", Text: text})
}

// respond lets the model look around and reply, then handles the reply,
// offering a fix when a command fails.
func (s *qSession) respond() int {
	for fixes := 0; ; fixes++ {
		step, err := s.converse()
		if err != nil {
			return exitCode(err, s.stderr)
		}
		status, failed := s.handle(step)
		if !failed || fixes >= qMaxFixes || s.keys == nil || s.handedOff {
			return status
		}
		fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  enter ask for a fix · esc stop"))
		if qReadKey(s.keys) != "enter" {
			return status
		}
		s.turns = append(s.turns, qTurn{Role: "user", Text: "That failed. Look at the error and propose a fix, or explain what is wrong."})
	}
}

// converse runs model steps until the model replies, running the lookup
// tools it asks for along the way.
func (s *qSession) converse() (qStep, error) {
	for lookups := 0; ; lookups++ {
		var step qStep
		var err error
		s.busy("Asking "+s.choice.provider.label()+"…", func(ctx context.Context) {
			step, err = s.choice.provider.step(ctx, qSystemPrompt(), s.turns, lookups < qMaxLookups)
		})
		if err != nil {
			return step, err
		}
		if len(step.calls) == 0 {
			switch step.reply.Kind {
			case "command":
				if step.proposal.ID == "" {
					args, _ := json.Marshal(step.reply)
					step.proposal = qToolCall{ID: qNewCallID(), Name: "propose", Args: args}
				}
				s.turns = append(s.turns, qTurn{Role: "assistant", Calls: []qToolCall{step.proposal}})
			default:
				s.turns = append(s.turns, qTurn{Role: "assistant", Text: step.reply.Answer})
			}
			return step, nil
		}
		s.turns = append(s.turns, qTurn{Role: "assistant", Calls: step.calls})
		for _, call := range step.calls {
			var result string
			s.withInterrupt(func(ctx context.Context) { result = s.runTool(ctx, call) })
			s.turns = append(s.turns, qTurn{Role: "tool", CallID: call.ID, Tool: call.Name, Text: result})
		}
	}
}

// handle shows a reply. For a command it waits for the user's choice and
// records the outcome as the proposal's result. failed is true when the
// command ran and exited non-zero.
func (s *qSession) handle(step qStep) (status int, failed bool) {
	reply := step.reply
	if reply.Kind == "answer" {
		fmt.Fprintln(s.stdout, s.markdown(reply.Answer))
		return 0, false
	}
	outcome := func(text string) {
		s.turns = append(s.turns, qTurn{Role: "tool", CallID: step.proposal.ID, Tool: "propose", Text: text})
	}
	command := reply.Command
	home, _ := os.UserHomeDir()
	assess := func() qAssessment {
		assessment := assessCommand(command, home)
		if modelRisk := parseQRisk(reply.Risk); modelRisk > assessment.risk {
			assessment.risk = modelRisk
			if modelRisk == qDangerous && len(assessment.reasons) == 0 {
				assessment.reasons = append(assessment.reasons, "the model marked it dangerous")
			}
		}
		return assessment
	}
	assessment := assess()

	if s.options.print {
		fmt.Fprintln(s.stdout, command)
		if assessment.risk == qDangerous {
			fmt.Fprintf(s.stderr, "hi: dangerous: %s\n", strings.Join(assessment.reasons, "; "))
		}
		outcome("The command was printed for the user.")
		return 0, false
	}

	s.showProposal(command, reply.Reason, assessment)
	if s.keys == nil {
		fmt.Fprintln(s.stdout, "No terminal to confirm in; copy the command, or use --print.")
		outcome("No terminal to confirm in; not run.")
		return 0, false
	}
	if s.options.yes && assessment.risk == qReadOnly {
		return s.runProposal(command, assessment.risk, outcome)
	}
	for {
		s.showKeys(assessment.risk)
		switch qReadKey(s.keys) {
		case "enter":
			if assessment.risk == qDangerous {
				fmt.Fprint(s.stdout, s.style(colorRed, true).Render("  Type yes to run it: "))
				answer, _ := readLine(s.keys)
				if strings.ToLower(strings.TrimSpace(answer)) != "yes" {
					fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  Not run."))
					outcome("The user chose not to run it.")
					return 0, false
				}
			}
			return s.runProposal(command, assessment.risk, outcome)
		case "e":
			edited, handed := s.edit(command)
			if handed {
				outcome("The user took the command to the shell's prompt to edit and run themselves.")
				return 0, false
			}
			if edited != "" && edited != command {
				command = edited
				reply.Reason = "Edited by you."
				reply.Risk = ""
				assessment = assess()
				s.showProposal(command, reply.Reason, assessment)
			}
		case "c":
			s.copy(command)
			outcome("The user copied it to run themselves.")
			return 0, false
		case "?":
			explained, err := s.askOnce(qExplainRequest(command, s.contextText))
			if err != nil {
				fmt.Fprintf(s.stderr, "hi: %v\n", err)
				continue
			}
			fmt.Fprintln(s.stdout, qIndent(s.markdown(firstNonEmpty(explained.Answer, explained.Reason)), "  "))
			fmt.Fprintln(s.stdout)
		case "esc", "q", "n", "ctrl-c":
			fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  Not run."))
			outcome("The user chose not to run it.")
			return 0, false
		}
	}
}

func (s *qSession) runProposal(command string, risk qRisk, outcome func(string)) (int, bool) {
	if s.link != nil && qNeedsShell(command) {
		if err := s.link.handOff("run", command); err == nil {
			s.handedOff = true
			fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  Running it in your shell, since it changes the shell itself."))
			logQCommand(s.options.prompt, command, risk, -1)
			outcome("Handed to the user's shell to run, because it changes the shell's state.")
			return 0, false
		}
	}
	status, tail := s.execute(command, risk)
	text := fmt.Sprintf("The user ran it. Exit status %d.", status)
	if strings.TrimSpace(tail) != "" {
		text += "\nEnd of its error output:\n" + redactQText(tail)
	}
	outcome(text)
	return status, status != 0
}

// askOnce sends one message outside the conversation, without tools.
func (s *qSession) askOnce(user string) (qReply, error) {
	var reply qReply
	var err error
	s.busy("Asking "+s.choice.provider.label()+"…", func(ctx context.Context) {
		reply, err = qAskOnce(ctx, s.choice.provider, qSystemPrompt(), user)
	})
	return reply, err
}

// busy runs work with a spinner on a terminal; Ctrl-C cancels it.
func (s *qSession) busy(label string, work func(ctx context.Context)) {
	s.withInterrupt(func(ctx context.Context) {
		ctx, cancel := context.WithTimeout(ctx, qRequestTimeout)
		defer cancel()
		if file, ok := s.stderr.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			(&styledUI{out: s.stderr}).busy(label, func() { work(ctx) })
		} else {
			work(ctx)
		}
	})
}

func (s *qSession) withInterrupt(work func(ctx context.Context)) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	work(ctx)
}

func (s *qSession) note(text string) {
	out := s.stdout
	if s.options.print {
		out = s.stderr
	}
	fmt.Fprintln(out, s.style(colorDim, false).Render("  · "+text))
}

func (s *qSession) style(color lipgloss.TerminalColor, bold bool) lipgloss.Style {
	if !s.styled {
		return lipgloss.NewStyle()
	}
	return lipgloss.NewStyle().Foreground(color).Bold(bold)
}

func (s *qSession) showProposal(command, reason string, assessment qAssessment) {
	riskColor := map[qRisk]lipgloss.TerminalColor{qReadOnly: colorMint, qChanges: colorAmber, qDangerous: colorRed}[assessment.risk]
	if s.styled {
		fmt.Fprintln(s.stdout, lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(riskColor).
			Padding(0, 1).Foreground(colorText).Bold(true).
			Render(command))
	} else {
		fmt.Fprintln(s.stdout, qIndent(command, "  $ "))
	}
	if reason != "" {
		fmt.Fprintln(s.stdout, qIndent(s.markdown(reason), "  "))
	}
	for _, match := range assessment.matches {
		fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  "+match))
	}
	if s.link == nil && qNeedsShell(command) {
		fmt.Fprintln(s.stdout, s.style(colorAmber, false).Render("  hi runs commands in a new shell, so this can't change your current one; copy it, or set up the shell with hi q --setup"))
	}
	label := assessment.risk.String()
	if len(assessment.reasons) > 0 {
		label += ": " + strings.Join(assessment.reasons, "; ")
	}
	fmt.Fprintln(s.stdout, s.style(riskColor, true).Render("  "+label))
}

func (s *qSession) showKeys(risk qRisk) {
	run := "enter run"
	if risk == qDangerous {
		run = "enter run (asks for yes)"
	}
	fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  "+run+" · e edit · c copy · ? explain · esc cancel"))
}

// execute runs the command in the user's own shell, in the current folder,
// and passes its exit status through. Standard output stays on the
// terminal; the end of standard error is kept for a fix.
func (s *qSession) execute(command string, risk qRisk) (int, string) {
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
	tail := &qTail{limit: 4 << 10}
	process.Stdout = s.stdout
	process.Stderr = io.MultiWriter(s.stderr, tail)
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
	if s.link != nil {
		s.link.recordRan(command)
	}
	logQCommand(s.options.prompt, command, risk, status)
	return status, tail.String()
}

// qTail keeps the last bytes written to it.
type qTail struct {
	limit int
	data  []byte
}

func (t *qTail) Write(p []byte) (int, error) {
	t.data = append(t.data, p...)
	if len(t.data) > t.limit {
		t.data = t.data[len(t.data)-t.limit:]
	}
	return len(p), nil
}

func (t *qTail) String() string { return string(t.data) }

// edit puts the command on the shell's prompt when zsh integration can,
// and opens it in an editor otherwise. handed is true when the shell takes
// it from here.
func (s *qSession) edit(command string) (edited string, handed bool) {
	if s.link != nil && s.link.shell == "zsh" {
		if s.link.handOff("edit", command) == nil {
			s.handedOff = true
			fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  It is on your prompt to edit."))
			return "", true
		}
	}
	editor := firstNonEmpty(os.Getenv("VISUAL"), os.Getenv("EDITOR"))
	if editor == "" {
		for _, candidate := range []string{"editor", "nano", "vim", "vi"} {
			if _, err := exec.LookPath(candidate); err == nil {
				editor = candidate
				break
			}
		}
	}
	if editor == "" {
		fmt.Fprintln(s.stderr, "hi: no editor found; set EDITOR")
		return "", false
	}
	file, err := os.CreateTemp("", "hi-q-*.sh")
	if err != nil {
		return "", false
	}
	defer os.Remove(file.Name())
	file.WriteString(command + "\n")
	file.Close()
	process := exec.Command("/bin/sh", "-c", editor+` "$1"`, "sh", file.Name())
	if tty, ok := s.keys.(*os.File); ok {
		process.Stdin = tty
	}
	process.Stdout = s.stdout
	process.Stderr = s.stderr
	if err := process.Run(); err != nil {
		fmt.Fprintf(s.stderr, "hi: %s: %v\n", editor, err)
		return "", false
	}
	data, err := os.ReadFile(file.Name())
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(string(data)), false
}

func (s *qSession) copy(text string) {
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
			fmt.Fprintln(s.stdout, s.style(colorMint, false).Render("  Copied."))
			return
		}
	}
	// OSC 52 asks the terminal itself to copy, which also works over SSH.
	out := s.ttyOut
	if out == nil {
		out = s.stdout
	}
	fmt.Fprintf(out, "\x1b]52;c;%s\a", base64.StdEncoding.EncodeToString([]byte(text)))
	fmt.Fprintln(s.stdout, s.style(colorMint, false).Render("  Sent to the terminal's clipboard (if it allows OSC 52)."))
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
	return `You are hi q, a terminal helper. The user describes what they want in plain words; you reply with one shell command for their shell and system, or a short answer when they only asked a question. In a conversation, earlier commands and their results are in the history.

When they want something done, call the propose tool with:
- command: the exact command. Prefer one line; use a short script only when one line would be unreadable.
- reason: one short sentence saying what it does, in plain words.
- risk: "read-only" if it changes nothing, "changes" if it creates, moves, or edits files, "dangerous" if it deletes data, needs root, or is hard to undo.
When they ask a question, reply with a short answer and no command.

Answers show in a terminal, which renders only this Markdown: **bold**, *italics*, inline code in backticks, fenced code blocks, "- " lists, numbered lists, and # headings. Don't use tables or HTML. Keep answers short; a few lines is usually enough.

Look before you propose when it matters: list files to get real names, read a config or log, check a flag with help, or run a read-only command such as wc -l or git status. Don't look when the context already answers it; each look takes time. Never look more than a few times for one request.

Write commands well:
- Quote paths and variables. Put -- before file arguments where the command supports it.
- Use find -print0 with xargs -0, or find -exec, for names with spaces.
- Prefer mkdir -p, mv -n, and cp -n over overwriting. Never delete more than asked.
- Use flags that exist on the user's system: GNU or BSD core tools as stated in the context.
- Use the tools the context lists as installed (rg, fd, jq, ...) when they make the command simpler, otherwise standard tools.
- Use real file and folder names; don't invent them. If the request is ambiguous in a way that matters, ask instead of guessing at a destructive reading.
- Don't add sudo unless the task truly needs root.
- Write for the user's shell (bash, zsh, or fish as stated).
- If a command failed, read its error and fix the cause; don't repeat it unchanged.

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
// status, log, and the saved conversation

func qStatusCommand(stdout io.Writer) error {
	choice, err := resolveQProvider("", "")
	if err != nil {
		return err
	}
	if choice.provider == nil {
		fmt.Fprintln(stdout, "Model:   none. Run hi q --setup.")
	} else {
		fmt.Fprintf(stdout, "Model:   %s\nFrom:    %s\n", choice.provider.label(), choice.source)
	}
	fmt.Fprintf(stdout, "Config:  %s\n", filepath.Join(qConfigDirectory(), "q.json"))
	if path, err := qStatePath("log.jsonl"); err == nil {
		fmt.Fprintf(stdout, "Log:     %s\n", path)
	}
	switch link := currentQShellLink(); {
	case link != nil:
		fmt.Fprintf(stdout, "Shell:   %s integration is on in this shell\n", link.shell)
	case qShellInstalled() != "":
		fmt.Fprintf(stdout, "Shell:   set up in %s; open a new terminal to use it\n", qShellInstalled())
	default:
		fmt.Fprintln(stdout, "Shell:   not set up; run hi q --setup")
	}
	return nil
}

func qStatePath(name string) (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "hi", "q", name), nil
}

// logQCommand records a command that ran. The log stays on this machine and
// never holds the command's output. status is -1 when the shell ran it.
func logQCommand(prompt, command string, risk qRisk, status int) {
	path, err := qStatePath("log.jsonl")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	folder, _ := os.Getwd()
	fields := map[string]any{
		"time":    time.Now().UTC().Format(time.RFC3339),
		"folder":  folder,
		"prompt":  redactQText(prompt),
		"command": redactQText(command),
		"risk":    risk.String(),
	}
	if status >= 0 {
		fields["exit"] = status
	} else {
		fields["ran_in"] = "shell"
	}
	entry, err := json.Marshal(fields)
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

// saveQConversation keeps the last conversation for hi q -c. Only the last
// one is kept, readable only by the user.
func saveQConversation(turns []qTurn) {
	path, err := qStatePath("last.json")
	if err != nil || len(turns) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	data, err := json.Marshal(map[string]any{"saved": time.Now().UTC().Format(time.RFC3339), "turns": turns})
	if err != nil {
		return
	}
	os.WriteFile(path, data, 0o600)
}

func loadQConversation() []qTurn {
	path, err := qStatePath("last.json")
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var saved struct {
		Turns []qTurn `json:"turns"`
	}
	if json.Unmarshal(data, &saved) != nil {
		return nil
	}
	return saved.Turns
}
