package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// listSelect is a list where only the cursor moves. Huh's select re-anchors
// its scroll position on the selected option, which makes short lists jump;
// here the list scrolls only when the cursor would leave the visible window.
type listSelect struct {
	title       string
	description string
	options     []string
	visible     []int // indexes into options that match the filter
	cursor      int   // position in visible
	offset      int   // first visible row
	rows        int   // how many rows fit
	filter      string
	filtering   bool
	// search shows a search field that takes every key typed, for long
	// lists; the list stays short and filters as you type.
	search  bool
	done    bool
	aborted bool
}

// searchRows is how many options a search list shows at once.
const searchRows = 15

func newSearchSelect(title, description string, options []string) *listSelect {
	model := &listSelect{title: title, description: description, options: options, rows: searchRows, search: true}
	model.applyFilter()
	return model
}

// matchesWords reports whether option contains every word of filter, in any
// order and case.
func matchesWords(option, filter string) bool {
	option = strings.ToLower(option)
	for _, word := range strings.Fields(strings.ToLower(filter)) {
		if !strings.Contains(option, word) {
			return false
		}
	}
	return true
}

func newListSelect(title, description string, options []string) *listSelect {
	model := &listSelect{title: title, description: description, options: options, rows: 12}
	model.applyFilter()
	return model
}

func (m *listSelect) Init() tea.Cmd { return nil }

func (m *listSelect) applyFilter() {
	m.visible = m.visible[:0]
	for i, option := range m.options {
		if matchesWords(option, m.filter) {
			m.visible = append(m.visible, i)
		}
	}
	m.cursor = min(m.cursor, max(len(m.visible)-1, 0))
	if m.search {
		m.cursor = 0
	}
	m.offset = 0
	m.keepCursorVisible()
}

func (m *listSelect) keepCursorVisible() {
	rows := m.visibleRows()
	switch {
	case m.cursor < m.offset:
		m.offset = m.cursor
	case m.cursor >= m.offset+rows:
		m.offset = m.cursor - rows + 1
	}
	m.offset = max(0, min(m.offset, len(m.visible)-rows))
}

func (m *listSelect) visibleRows() int {
	return max(1, min(len(m.visible), m.rows))
}

func (m *listSelect) move(delta int) {
	if len(m.visible) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.visible)-1, m.cursor+delta))
	m.keepCursorVisible()
}

func (m *listSelect) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Leave room for the title, description, hints, and the lines above.
		m.rows = max(5, msg.Height-8)
		if m.search {
			m.rows = max(3, min(searchRows, msg.Height-9))
		}
		m.keepCursorVisible()
	case tea.KeyMsg:
		key := msg.String()
		if m.search {
			return m.updateSearch(msg)
		}
		if m.filtering {
			switch key {
			case "enter":
				m.filtering = false
				if len(m.visible) == 1 {
					m.done = true
					return m, tea.Quit
				}
			case "esc":
				m.filtering = false
				m.filter = ""
				m.applyFilter()
			case "backspace":
				if m.filter != "" {
					m.filter = m.filter[:len(m.filter)-1]
					m.applyFilter()
				} else {
					m.filtering = false
				}
			case "up", "ctrl+p":
				m.move(-1)
			case "down", "ctrl+n":
				m.move(1)
			case "ctrl+c":
				m.aborted = true
				return m, tea.Quit
			default:
				if msg.Type == tea.KeyRunes || key == " " {
					m.filter += msg.String()
					m.applyFilter()
				}
			}
			return m, nil
		}
		switch key {
		case "up", "k", "ctrl+p":
			m.move(-1)
		case "down", "j", "ctrl+n", "tab":
			m.move(1)
		case "pgup":
			m.move(-m.visibleRows())
		case "pgdown":
			m.move(m.visibleRows())
		case "home", "g":
			m.move(-len(m.visible))
		case "end", "G":
			m.move(len(m.visible))
		case "/":
			m.filtering = true
		case "enter":
			if len(m.visible) > 0 {
				m.done = true
				return m, tea.Quit
			}
		case "esc", "q", "ctrl+c":
			m.aborted = true
			return m, tea.Quit
		default:
			// A number jumps to that option in short, unfiltered lists.
			if number, err := strconv.Atoi(key); err == nil && m.filter == "" && len(m.options) <= 9 &&
				number >= 1 && number <= len(m.visible) {
				m.cursor = number - 1
				m.keepCursorVisible()
			}
		}
	}
	return m, nil
}

// updateSearch handles keys in a search list: letters go to the field,
// arrows move, enter chooses, and esc clears the field or goes back.
func (m *listSelect) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key := msg.String(); key {
	case "up", "ctrl+p", "shift+tab":
		m.move(-1)
	case "down", "ctrl+n", "tab":
		m.move(1)
	case "pgup":
		m.move(-m.visibleRows())
	case "pgdown":
		m.move(m.visibleRows())
	case "enter":
		if len(m.visible) > 0 {
			m.done = true
			return m, tea.Quit
		}
	case "esc":
		if m.filter == "" {
			m.aborted = true
			return m, tea.Quit
		}
		m.filter = ""
		m.applyFilter()
	case "ctrl+c":
		m.aborted = true
		return m, tea.Quit
	case "backspace":
		if m.filter != "" {
			runes := []rune(m.filter)
			m.filter = string(runes[:len(runes)-1])
			m.applyFilter()
		}
	case "ctrl+u", "ctrl+w":
		m.filter = ""
		m.applyFilter()
	default:
		switch msg.Type {
		case tea.KeyRunes: // typed or pasted
			m.filter += string(msg.Runes)
			m.applyFilter()
		case tea.KeySpace:
			m.filter += " "
			m.applyFilter()
		}
	}
	return m, nil
}

func (m *listSelect) View() string {
	if m.done || m.aborted {
		return ""
	}
	title := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dim := lipgloss.NewStyle().Foreground(colorDim)
	selected := lipgloss.NewStyle().Foreground(colorMint).Bold(true)
	plain := lipgloss.NewStyle().Foreground(colorText)

	var lines []string
	if m.search {
		lines = append(lines, title.Render(m.title))
		field := m.filter + lipgloss.NewStyle().Foreground(colorMint).Render("▏")
		if m.filter == "" {
			field = lipgloss.NewStyle().Foreground(colorMint).Render("▏") + dim.Render("type words to search, for example gemma 4b")
		}
		count := fmt.Sprintf("%d of %d", len(m.visible), len(m.options))
		lines = append(lines, dim.Render("Search: ")+field+"  "+dim.Render(count))
	} else if m.filtering || m.filter != "" {
		cursor := ""
		if m.filtering {
			cursor = lipgloss.NewStyle().Foreground(colorMint).Render("▏")
		}
		lines = append(lines, title.Render(m.title)+"  "+dim.Render("/")+m.filter+cursor)
	} else {
		lines = append(lines, title.Render(m.title))
	}
	if m.description != "" && !m.search {
		lines = append(lines, dim.Render(m.description))
	}

	rows := m.visibleRows()
	if m.offset > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("  ↑ %d more", m.offset)))
	}
	if len(m.visible) == 0 {
		lines = append(lines, dim.Render("  nothing matches; backspace to change the search"))
	}
	for row := m.offset; row < m.offset+rows && row < len(m.visible); row++ {
		option := m.options[m.visible[row]]
		if row == m.cursor {
			lines = append(lines, selected.Render("❯ "+option))
		} else {
			lines = append(lines, plain.Render("  "+option))
		}
	}
	if below := len(m.visible) - (m.offset + rows); below > 0 {
		lines = append(lines, dim.Render(fmt.Sprintf("  ↓ %d more", below)))
	}

	body := lipgloss.NewStyle().
		Border(lipgloss.ThickBorder(), false, false, false, true).
		BorderForeground(colorAccent).PaddingLeft(1).
		Render(strings.Join(lines, "\n"))
	help := "↑/↓ move • enter choose • / filter • esc back"
	if m.search {
		help = "type to search • ↑/↓ move • enter choose • esc clear, then back"
	} else if m.filtering {
		help = "type to filter • enter done • esc clear"
	}
	return body + "\n" + dim.Render(help) + "\n"
}

// runListSelect shows the list and returns the chosen option's index.
func runListSelect(in io.Reader, out io.Writer, title, description string, options []string, search bool) (int, error) {
	model := newListSelect(title, description, options)
	if search {
		model = newSearchSelect(title, description, options)
	}
	result, err := tea.NewProgram(model, tea.WithInput(in), tea.WithOutput(out)).Run()
	if err != nil {
		return 0, err
	}
	final := result.(*listSelect)
	if final.aborted || !final.done || len(final.visible) == 0 {
		return 0, errMenuBack
	}
	return final.visible[final.cursor], nil
}
