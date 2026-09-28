package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// menuUI is how the guided menu asks questions and shows results. The styled
// version runs in terminals; the line version serves tests and pipes.
type menuUI interface {
	header(listed []listedInstance)
	choose(title string, options []string, filter bool) (int, error)
	input(title, fallback string, validate func(string) error) (string, error)
	confirm(title, card string, warning bool) (bool, error)
	command(line string)
	note(text string)
	failure(err error)
	busy(label string, work func())
}

// ---------------------------------------------------------------------------
// line UI

type lineUI struct {
	in  io.Reader
	out io.Writer
}

func (u lineUI) header(listed []listedInstance) {
	fmt.Fprintln(u.out)
	if len(listed) == 0 {
		return
	}
	fmt.Fprintln(u.out, "Running:")
	for _, item := range listed {
		fmt.Fprintf(u.out, "  %s/%s  %s  %s\n", item.provider, item.instance.name, item.instance.hardware, limitText(item))
	}
	fmt.Fprintln(u.out)
}

func (u lineUI) choose(title string, options []string, filter bool) (int, error) {
	return menuChoice(u.in, u.out, title, options)
}

func (u lineUI) input(title, fallback string, validate func(string) error) (string, error) {
	for {
		answer, err := ask(u.in, u.out, title, fallback)
		if err != nil {
			return "", err
		}
		if validate == nil {
			return answer, nil
		}
		if problem := validate(answer); problem != nil {
			fmt.Fprintln(u.out, problem.Error())
			continue
		}
		return answer, nil
	}
}

func (u lineUI) confirm(title, card string, warning bool) (bool, error) {
	if card != "" {
		fmt.Fprintln(u.out, card)
	}
	fmt.Fprintf(u.out, "%s [y/N] ", title)
	answer, err := readLine(u.in)
	if strings.TrimSpace(answer) == "" && err != nil {
		return false, errMenuBack
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "y" || answer == "yes", nil
}

func (u lineUI) command(line string)        { fmt.Fprintf(u.out, "$ %s\n", line) }
func (u lineUI) note(text string)           { fmt.Fprintln(u.out, text) }
func (u lineUI) failure(err error)          { fmt.Fprintf(u.out, "hi: %v\n", err) }
func (u lineUI) busy(_ string, work func()) { work() }

// ---------------------------------------------------------------------------
// styled UI

var (
	colorAccent = lipgloss.AdaptiveColor{Light: "#4f56e6", Dark: "#7c83ff"}
	colorMint   = lipgloss.AdaptiveColor{Light: "#0f9f73", Dark: "#4fd1a5"}
	colorAmber  = lipgloss.AdaptiveColor{Light: "#b7791f", Dark: "#f0b45b"}
	colorRed    = lipgloss.AdaptiveColor{Light: "#d0384b", Dark: "#f07178"}
	colorText   = lipgloss.AdaptiveColor{Light: "235", Dark: "252"}
	colorDim    = lipgloss.AdaptiveColor{Light: "245", Dark: "243"}
	colorLine   = lipgloss.AdaptiveColor{Light: "250", Dark: "238"}
)

type styledUI struct {
	in        io.Reader
	out       io.Writer
	bannerOut bool
}

func newMenuUI(stdin io.Reader, stdout io.Writer) menuUI {
	if isTerminal(stdin) {
		if file, ok := stdout.(*os.File); ok && isTerminal(file) {
			return &styledUI{in: stdin, out: stdout}
		}
	}
	return lineUI{in: stdin, out: stdout}
}

func hiTheme() *huh.Theme {
	t := huh.ThemeCharm()
	t.Focused.Base = t.Focused.Base.BorderForeground(colorAccent)
	t.Focused.Title = t.Focused.Title.Foreground(colorAccent).Bold(true)
	t.Focused.Description = t.Focused.Description.Foreground(colorDim)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(colorMint).SetString("❯ ")
	t.Focused.NextIndicator = t.Focused.NextIndicator.Foreground(colorAccent)
	t.Focused.PrevIndicator = t.Focused.PrevIndicator.Foreground(colorAccent)
	t.Focused.Option = t.Focused.Option.Foreground(colorText)
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(colorMint).Bold(true)
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(colorRed)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(colorRed)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(lipgloss.Color("#ffffff")).Background(colorAccent).Bold(true)
	t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(colorText).Background(lipgloss.AdaptiveColor{Light: "252", Dark: "237"})
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(colorMint)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(colorAccent)
	t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(colorDim)
	t.Focused.Card = t.Focused.Base
	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Card = t.Blurred.Base
	t.Group.Title = t.Focused.Title
	t.Group.Description = t.Focused.Description
	return t
}

func (u *styledUI) run(field huh.Field) error {
	err := huh.NewForm(huh.NewGroup(field)).
		WithTheme(hiTheme()).
		WithInput(u.in).
		WithOutput(u.out).
		WithShowHelp(true).
		Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return errMenuBack
	}
	return err
}

func (u *styledUI) header(listed []listedInstance) {
	if !u.bannerOut {
		badge := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#ffffff")).Background(colorAccent).Padding(0, 1)
		tagline := lipgloss.NewStyle().Foreground(colorDim)
		fmt.Fprintf(u.out, "\n%s %s\n", badge.Render("hi compute"), tagline.Render("rent GPUs on Colab and Hugging Face"))
		u.bannerOut = true
	}
	fmt.Fprintln(u.out)
	if len(listed) == 0 {
		fmt.Fprintln(u.out, lipgloss.NewStyle().Foreground(colorDim).PaddingLeft(1).Render("Nothing is running."))
		fmt.Fprintln(u.out)
		return
	}

	name := lipgloss.NewStyle().Bold(true).Foreground(colorText)
	dim := lipgloss.NewStyle().Foreground(colorDim)
	var rows []string
	nameWidth, hardwareWidth := 0, 0
	for _, item := range listed {
		nameWidth = max(nameWidth, len(item.instance.name))
		hardwareWidth = max(hardwareWidth, len(item.instance.hardware))
	}
	for _, item := range listed {
		limit := limitText(item)
		limitStyle := lipgloss.NewStyle().Foreground(colorMint)
		if strings.Contains(limit, "no") {
			limitStyle = lipgloss.NewStyle().Foreground(colorAmber)
		}
		rows = append(rows, fmt.Sprintf("%s  %s  %s  %s",
			name.Render(fmt.Sprintf("%-*s", nameWidth, item.instance.name)),
			providerBadge(item.provider),
			dim.Render(fmt.Sprintf("%-*s", hardwareWidth, item.instance.hardware)),
			limitStyle.Render(limit)))
	}
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(colorLine).
		Padding(0, 1)
	title := lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("Running")
	fmt.Fprintln(u.out, box.Render(title+"\n"+strings.Join(rows, "\n")))
	fmt.Fprintln(u.out)
}

func providerBadge(provider string) string {
	color := colorAccent
	if provider == "colab" {
		color = colorAmber
	}
	return lipgloss.NewStyle().Foreground(color).Render(fmt.Sprintf("%-5s", provider))
}

func (u *styledUI) choose(title string, options []string, filter bool) (int, error) {
	choice := 0
	huhOptions := make([]huh.Option[int], len(options))
	for i, option := range options {
		huhOptions[i] = huh.NewOption(option, i)
	}
	// Filtering is always available with "/"; Filtering(true) would start
	// in filter mode instead.
	field := huh.NewSelect[int]().Title(title).Options(huhOptions...).Value(&choice).
		Height(min(len(options)+2, 16))
	if filter {
		field = field.Description("Press / and type to filter, for example /a10")
	}
	if err := u.run(field); err != nil {
		return 0, err
	}
	if !strings.HasSuffix(title, "?") {
		u.answered(title, strings.Join(strings.Fields(options[choice]), " "))
	}
	return choice, nil
}

// answered leaves a one-line record of an answer, since a finished question
// disappears from the screen.
func (u *styledUI) answered(title, value string) {
	if value == "" {
		value = "(none)"
	}
	fmt.Fprintf(u.out, "%s %s %s\n",
		lipgloss.NewStyle().Foreground(colorMint).Render("✔"),
		lipgloss.NewStyle().Foreground(colorDim).Render(title),
		lipgloss.NewStyle().Foreground(colorText).Bold(true).Render(value))
}

func (u *styledUI) input(title, fallback string, validate func(string) error) (string, error) {
	value := ""
	field := huh.NewInput().Title(title).Value(&value).Placeholder(fallback)
	if validate != nil {
		field = field.Validate(func(answer string) error {
			if strings.TrimSpace(answer) == "" && fallback != "" {
				answer = fallback
			}
			return validate(strings.TrimSpace(answer))
		})
	}
	if err := u.run(field); err != nil {
		return "", err
	}
	if value = strings.TrimSpace(value); value == "" {
		value = fallback
	}
	u.answered(title, value)
	return value, nil
}

func (u *styledUI) confirm(title, card string, warning bool) (bool, error) {
	if card != "" {
		border := colorLine
		if warning {
			border = colorAmber
		}
		fmt.Fprintln(u.out, lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).BorderForeground(border).
			Padding(0, 1).Render(card))
	}
	answer := false
	field := huh.NewConfirm().Title(title).Affirmative("Yes").Negative("No").Value(&answer)
	if err := u.run(field); err != nil {
		return false, err
	}
	reply := "No"
	if answer {
		reply = "Yes"
	}
	u.answered(title, reply)
	return answer, nil
}

func (u *styledUI) command(line string) {
	prompt := lipgloss.NewStyle().Foreground(colorMint).Render("$")
	fmt.Fprintf(u.out, "%s %s\n", prompt, lipgloss.NewStyle().Foreground(colorAccent).Render(line))
}

func (u *styledUI) note(text string) {
	fmt.Fprintln(u.out, lipgloss.NewStyle().Foreground(colorDim).Render(text))
}

func (u *styledUI) failure(err error) {
	fmt.Fprintln(u.out, lipgloss.NewStyle().Foreground(colorRed).Render("✗ "+err.Error()))
}

// busy shows a spinner while slow work, such as a provider API call, runs.
func (u *styledUI) busy(label string, work func()) {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	style := lipgloss.NewStyle().Foreground(colorAccent)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				fmt.Fprint(u.out, "\r\033[K")
				return
			case <-time.After(80 * time.Millisecond):
				fmt.Fprintf(u.out, "\r%s %s", style.Render(frames[i%len(frames)]), label)
			}
		}
	}()
	work()
	close(done)
	wg.Wait()
}

// limitText is how long an instance has left, for the running lists.
func limitText(item listedInstance) string {
	now := computeNow()
	switch {
	case item.record != nil && item.record.Deadline.IsZero():
		return "no time limit"
	case item.record != nil:
		return "stops in " + formatDuration(item.record.Deadline.Sub(now))
	case item.instance.managed && item.instance.deadline.IsZero():
		return "no time limit"
	case item.instance.managed:
		return "stops in " + formatDuration(item.instance.deadline.Sub(now))
	}
	return "not started by hi"
}
