package main

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func press(m *listSelect, keys ...string) {
	for _, key := range keys {
		var msg tea.KeyMsg
		switch key {
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
		}
		m.Update(msg)
	}
}

func TestListSelectKeepsShortListsStill(t *testing.T) {
	options := []string{"one", "two", "three", "four", "five", "six", "seven", "eight"}
	m := newListSelect("What?", "", options)
	m.Update(tea.WindowSizeMsg{Height: 40})
	for i := 0; i < len(options); i++ {
		if m.offset != 0 {
			t.Fatalf("the list scrolled to %d with the cursor on %q", m.offset, options[m.cursor])
		}
		view := m.View()
		if !strings.Contains(view, "  one") && m.cursor != 0 {
			t.Fatalf("the first option left the screen with the cursor on %d:\n%s", m.cursor, view)
		}
		press(m, "down")
	}
	if m.cursor != len(options)-1 {
		t.Fatalf("cursor = %d, want the last option", m.cursor)
	}
}

func TestListSelectScrollsLongListsOnlyAtTheEdge(t *testing.T) {
	var options []string
	for i := 1; i <= 30; i++ {
		options = append(options, fmt.Sprintf("option-%02d", i))
	}
	m := newListSelect("Hardware", "", options)
	m.Update(tea.WindowSizeMsg{Height: 18}) // 10 rows
	press(m, "down", "down", "down", "down", "down", "down", "down", "down", "down")
	if m.offset != 0 {
		t.Fatalf("scrolled to %d before the cursor reached the edge", m.offset)
	}
	press(m, "down")
	if m.offset != 1 {
		t.Fatalf("offset = %d after passing the edge, want 1", m.offset)
	}
	if view := m.View(); !strings.Contains(view, "↑ 1 more") || !strings.Contains(view, "↓ 19 more") {
		t.Fatalf("scroll hints missing:\n%s", view)
	}
	press(m, "up")
	if m.offset != 1 {
		t.Fatalf("moving up inside the window scrolled to %d", m.offset)
	}
}

func TestListSelectFiltersAndChooses(t *testing.T) {
	m := newListSelect("Hardware", "", []string{"cpu-basic", "t4-small", "a10g-small", "a10g-large", "a100-large"})
	press(m, "/", "a", "1", "0", "g")
	if len(m.visible) != 2 || m.options[m.visible[0]] != "a10g-small" {
		t.Fatalf("filter a10g matched %v", m.visible)
	}
	press(m, "enter", "down", "enter")
	if !m.done || m.options[m.visible[m.cursor]] != "a10g-large" {
		t.Fatalf("chose %q, done = %v", m.options[m.visible[m.cursor]], m.done)
	}

	m = newListSelect("Hardware", "", []string{"cpu-basic", "t4-small", "a100-large"})
	press(m, "/", "t", "4", "enter")
	if !m.done || m.options[m.visible[m.cursor]] != "t4-small" {
		t.Fatal("enter with a single match did not choose it")
	}

	m = newListSelect("What?", "", []string{"a", "b", "c"})
	press(m, "3")
	if m.cursor != 2 {
		t.Fatalf("the number key did not jump; cursor = %d", m.cursor)
	}
	press(m, "esc")
	if !m.aborted {
		t.Fatal("esc did not go back")
	}
}
