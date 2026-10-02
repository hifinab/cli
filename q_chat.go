package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// chat is hi q without a question: a prompt below the current line that
// keeps one conversation, with the same tools and confirmations. Questions
// typed here never pass through the shell, so they need no quotes.
func (s *qSession) chat() int {
	reader := newQLineReader(s.keys, s.stdout, s.styled)
	header := "hi q · " + s.choice.provider.label() + " · /help · Ctrl-D leaves"
	if len(s.turns) > 0 {
		header += fmt.Sprintf(" · continuing (%d messages)", len(s.turns))
	}
	fmt.Fprintln(s.stdout, s.style(colorDim, false).Render(header))
	for {
		line, err := reader.read()
		if err != nil {
			fmt.Fprintln(s.stdout)
			return 0
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "/"):
			if s.slash(line) {
				return 0
			}
			continue
		}
		s.options.prompt = line
		s.addUserTurn(line)
		s.respond()
		saveQConversation(s.turns)
		if s.handedOff {
			return 0
		}
		fmt.Fprintln(s.stdout)
	}
}

// slash runs a chat command and returns true to leave.
func (s *qSession) slash(line string) bool {
	command, _, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	switch command {
	case "exit", "quit", "q":
		return true
	case "clear", "new":
		s.turns = nil
		s.contextSent = false
		fmt.Fprintln(s.stdout, s.style(colorDim, false).Render("  New conversation."))
	case "context":
		fmt.Fprint(s.stdout, qIndent(strings.TrimRight(s.contextText, "\n"), "  ")+"\n")
	case "model":
		fmt.Fprintln(s.stdout, "  "+s.choice.provider.label()+s.style(colorDim, false).Render(" · change it with hi q --setup, or --model for one chat"))
	default:
		fmt.Fprintln(s.stdout, `  /clear     start a new conversation
  /context   what was sent about this folder
  /model     the model in use
  /exit      leave (or Ctrl-D)`)
	}
	return false
}

// qLineReader reads chat lines with editing and history on a terminal,
// and plain lines otherwise.
type qLineReader struct {
	file     *os.File
	terminal *term.Terminal
	in       io.Reader
	out      io.Writer
	prompt   string
}

func newQLineReader(in io.Reader, out io.Writer, styled bool) *qLineReader {
	prompt := "q› "
	if styled {
		prompt = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("q›") + " "
	}
	reader := &qLineReader{in: in, out: out, prompt: prompt}
	if file, ok := in.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		reader.file = file
		reader.terminal = term.NewTerminal(struct {
			io.Reader
			io.Writer
		}{file, out}, prompt)
	}
	return reader
}

func (r *qLineReader) read() (string, error) {
	if r.terminal == nil {
		fmt.Fprint(r.out, r.prompt)
		line, err := readLine(r.in)
		if line == "" && err != nil {
			return "", err
		}
		return line, nil
	}
	fd := int(r.file.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", err
	}
	defer term.Restore(fd, state)
	width, height, err := term.GetSize(fd)
	if err != nil || width <= 0 {
		width, height = 80, 24
	}
	r.terminal.SetSize(width, height)
	line, err := r.terminal.ReadLine()
	if errors.Is(err, term.ErrPasteIndicator) {
		err = nil
	}
	return line, err
}
