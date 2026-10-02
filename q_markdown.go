package main

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// qMarkdown renders the small part of Markdown that models use in answers
// as terminal styles: bold, italics, inline code, fenced code blocks,
// headings, lists, quotes, rules, and links. A full renderer would pull in
// a large dependency for little gain. With color off, the markers are
// still removed, which is what tests check.
type qMarkdown struct {
	color bool
}

var (
	qMarkdownFence   = regexp.MustCompile("^(\\s*)(```|~~~)")
	qMarkdownHeading = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	qMarkdownBullet  = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	qMarkdownRule    = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
	qMarkdownQuote   = regexp.MustCompile(`^\s*>\s?(.*)$`)
	qMarkdownBold    = regexp.MustCompile(`\*\*([^*\n]+?)\*\*|__([^_\n]+?)__`)
	qMarkdownItalic  = regexp.MustCompile(`(^|[^\w*])\*([^*\s][^*\n]*?)\*([^\w*]|$)`)
	qMarkdownLink    = regexp.MustCompile(`\[([^\]\n]+)\]\(([^)\s]+)\)`)
)

func (m qMarkdown) style(color lipgloss.TerminalColor, bold, italic bool) lipgloss.Style {
	if !m.color {
		return lipgloss.NewStyle()
	}
	style := lipgloss.NewStyle().Bold(bold).Italic(italic)
	if color != nil {
		style = style.Foreground(color)
	}
	return style
}

func (m qMarkdown) render(text string) string {
	code := m.style(colorMint, false, false)
	heading := m.style(colorAccent, true, false)
	dim := m.style(colorDim, false, false)
	var out []string
	inFence := false
	fenceIndent := ""
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if match := qMarkdownFence.FindStringSubmatch(line); match != nil {
			if !inFence {
				inFence, fenceIndent = true, match[1]
			} else {
				inFence = false
			}
			continue
		}
		if inFence {
			// Keep the block's own indentation, and indent it two more so
			// it stands apart from the text around it.
			out = append(out, fenceIndent+"  "+code.Render(strings.TrimPrefix(line, fenceIndent)))
			continue
		}
		switch {
		case qMarkdownRule.MatchString(line):
			out = append(out, dim.Render(strings.Repeat("─", 40)))
		case qMarkdownHeading.MatchString(line):
			out = append(out, heading.Render(m.inline(qMarkdownHeading.FindStringSubmatch(line)[1], true)))
		case qMarkdownBullet.MatchString(line):
			match := qMarkdownBullet.FindStringSubmatch(line)
			out = append(out, match[1]+dim.Render("•")+" "+m.inline(match[2], true))
		case qMarkdownQuote.MatchString(line):
			out = append(out, dim.Render("│ ")+m.inline(qMarkdownQuote.FindStringSubmatch(line)[1], true))
		default:
			out = append(out, m.inline(line, true))
		}
	}
	return strings.Join(out, "\n")
}

// inline styles code spans, bold, italics, and links within one line.
// Text inside backticks is left as written.
func (m qMarkdown) inline(line string, emphasis bool) string {
	code := m.style(colorMint, false, false)
	bold := m.style(nil, true, false)
	italic := m.style(nil, false, true)
	dim := m.style(colorDim, false, false)
	parts := strings.Split(line, "`")
	if len(parts)%2 == 0 {
		// An unclosed backtick is text.
		parts[len(parts)-2] += "`" + parts[len(parts)-1]
		parts = parts[:len(parts)-1]
	}
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 1 {
			b.WriteString(code.Render(part))
			continue
		}
		part = qMarkdownLink.ReplaceAllStringFunc(part, func(link string) string {
			match := qMarkdownLink.FindStringSubmatch(link)
			if match[1] == match[2] {
				return match[2]
			}
			return match[1] + " " + dim.Render("("+match[2]+")")
		})
		if emphasis {
			part = qMarkdownBold.ReplaceAllStringFunc(part, func(text string) string {
				match := qMarkdownBold.FindStringSubmatch(text)
				return bold.Render(match[1] + match[2])
			})
			part = qMarkdownItalic.ReplaceAllStringFunc(part, func(text string) string {
				match := qMarkdownItalic.FindStringSubmatch(text)
				return match[1] + italic.Render(match[2]) + match[3]
			})
		}
		b.WriteString(part)
	}
	return b.String()
}
