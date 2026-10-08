package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// The selector: hi skill (or hi skills) in a terminal. Type to search
// skills.sh, browse suggestions, see a skill's details and audits, and
// pick several to install. Every action is also a command, so nothing
// needs the selector.

// skillItem is one line in the selector: an installed skill or a skill on
// skills.sh.
type skillItem struct {
	installed bool
	row       skillRow // installed
	hit       skillHit // from skills.sh or the suggestions
	why       string   // a suggestion's line
}

func (i skillItem) key() string {
	if i.installed {
		return "installed:" + i.row.Name
	}
	return i.hit.Source + "/" + i.hit.SkillID
}

// skillSelectorActions is what the selector asks for when it ends.
type skillSelectorAction struct {
	kind  string     // install, update, remove, or "" to quit
	hits  []skillHit // install
	name  string     // update or remove
	scope bool       // global
}

// skillSelectorServices are the lookups the selector makes; tests replace
// them.
type skillSelectorServices struct {
	search  func(query string) ([]skillHit, error)
	audits  func(source string, skills []string) (map[string]map[string]skillAudit, error)
	page    func(source, skill string) (map[string]string, error)
	resolve func(source skillSource) (string, error)
}

func defaultSkillServices() skillSelectorServices {
	return skillSelectorServices{
		search:  func(query string) ([]skillHit, error) { return searchSkillsSh(query, 30) },
		audits:  fetchSkillAudits,
		page:    fetchSkillPage,
		resolve: resolveSkillCommit,
	}
}

type skillSelector struct {
	services  skillSelectorServices
	global    bool
	base      string
	lock      *skillLock
	installed []skillRow
	newer     map[string]string // installed skill → newer commit

	query     string
	searchID  int
	searching bool
	results   []skillHit
	searchErr string

	audits  map[string]map[string]map[string]skillAudit // source → skill → partner
	asked   map[string]bool                             // sources whose audits were asked for
	pages   map[string]map[string]string                // source/skill → frontmatter
	pageErr map[string]string

	cursor  int // -1 is the search box
	offset  int
	picked  map[string]skillHit
	message string
	width   int
	height  int
	action  skillSelectorAction
}

type skillSearchTickMsg struct{ id int }
type skillSearchMsg struct {
	id   int
	hits []skillHit
	err  error
}
type skillAuditMsg struct {
	source string
	audits map[string]map[string]skillAudit
}
type skillPageMsg struct {
	key  string
	meta map[string]string
	err  error
}
type skillNewerMsg struct{ name, commit string }

func newSkillSelector(global bool, services skillSelectorServices) (*skillSelector, error) {
	m := &skillSelector{services: services, cursor: -1, picked: map[string]skillHit{}, newer: map[string]string{},
		audits: map[string]map[string]map[string]skillAudit{}, asked: map[string]bool{},
		pages: map[string]map[string]string{}, pageErr: map[string]string{}, width: 100, height: 30}
	return m, m.load(global)
}

// load reads what's installed here, or in the home folder.
func (m *skillSelector) load(global bool) error {
	base, err := skillBase(global)
	if err != nil {
		return err
	}
	lock, err := readSkillLock(global)
	if err != nil {
		return err
	}
	m.global, m.base, m.lock = global, base, lock
	m.installed = installedSkillRows(base, lock)
	m.newer = map[string]string{}
	m.cursor, m.offset = -1, 0
	return nil
}

func (m *skillSelector) Init() tea.Cmd {
	return tea.Batch(m.checkNewer(), m.fetchAudits(m.listedHits()))
}

// checkNewer asks each installed skill's source for its newest commit.
func (m *skillSelector) checkNewer() tea.Cmd {
	var commands []tea.Cmd
	for _, name := range m.lock.names() {
		entry, _ := m.lock.entry(name)
		source, err := entry.skillSource(filepath.Dir(m.lock.path))
		if err != nil || source.Kind == "local" || entry.Commit == "" {
			continue
		}
		name, resolve := name, m.services.resolve
		commands = append(commands, func() tea.Msg {
			commit, err := resolve(source)
			if err != nil {
				return nil
			}
			return skillNewerMsg{name: name, commit: commit}
		})
	}
	return tea.Batch(commands...)
}

// fetchAudits asks for the audits of sources not asked for yet.
func (m *skillSelector) fetchAudits(hits []skillHit) tea.Cmd {
	bySource := map[string][]string{}
	for _, hit := range hits {
		if hit.Source != "hi" && !m.asked[hit.Source] {
			bySource[hit.Source] = append(bySource[hit.Source], hit.SkillID)
		}
	}
	var commands []tea.Cmd
	for source, skills := range bySource {
		m.asked[source] = true
		source, skills, audits := source, skills, m.services.audits
		commands = append(commands, func() tea.Msg {
			found, err := audits(source, skills)
			if err != nil {
				return nil
			}
			return skillAuditMsg{source: source, audits: found}
		})
	}
	return tea.Batch(commands...)
}

// listedHits are the skills.sh skills shown: search results, or the
// suggestions before a search.
func (m *skillSelector) listedHits() []skillHit {
	if len(strings.TrimSpace(m.query)) >= 2 {
		return m.results
	}
	var hits []skillHit
	// The hi skill comes first when it isn't here.
	if hiSkillVersion(m.base) == "" {
		hits = append(hits, skillHit{ID: "hi", Source: "hi", SkillID: "hi", Name: "hi"})
	}
	for _, suggestion := range skillSuggestions {
		hits = append(hits, skillHit{ID: suggestion.Source + "/" + suggestion.Skill, Source: suggestion.Source,
			SkillID: suggestion.Skill, Name: suggestion.Skill})
	}
	return hits
}

func (m *skillSelector) items() []skillItem {
	var items []skillItem
	for _, row := range m.installed {
		items = append(items, skillItem{installed: true, row: row})
	}
	whys := map[string]string{"hi/hi": "Teach agents to use hi safely (built in)"}
	for _, suggestion := range skillSuggestions {
		whys[suggestion.Source+"/"+suggestion.Skill] = suggestion.Why
	}
	for _, hit := range m.listedHits() {
		item := skillItem{hit: hit}
		if len(strings.TrimSpace(m.query)) < 2 {
			item.why = whys[hit.Source+"/"+hit.SkillID]
		}
		items = append(items, item)
	}
	return items
}

func (m *skillSelector) current() (skillItem, bool) {
	items := m.items()
	if m.cursor < 0 || m.cursor >= len(items) {
		return skillItem{}, false
	}
	return items[m.cursor], true
}

// fetchPage reads the highlighted skill's SKILL.md for its details.
func (m *skillSelector) fetchPage() tea.Cmd {
	item, ok := m.current()
	if !ok || item.installed {
		return nil
	}
	key := item.key()
	if _, ok := m.pages[key]; ok {
		return nil
	}
	if _, ok := m.pageErr[key]; ok {
		return nil
	}
	if item.hit.Source == "hi" {
		m.pages[key] = map[string]string{"description": "Teaches Claude Code, Codex, and other agents to use hi: ask before spending, set limits, stop what they start. It comes with hi " + version + ".", "license": "hi's"}
		return nil
	}
	m.pages[key] = nil // asked
	source, skill, page := item.hit.Source, item.hit.SkillID, m.services.page
	return func() tea.Msg {
		meta, err := page(source, skill)
		return skillPageMsg{key: key, meta: meta, err: err}
	}
}

func (m *skillSelector) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = message.Width, message.Height
	case skillSearchTickMsg:
		if message.id != m.searchID || len(strings.TrimSpace(m.query)) < 2 {
			return m, nil
		}
		query, id, search := m.query, m.searchID, m.services.search
		m.searching = true
		return m, func() tea.Msg {
			hits, err := search(query)
			return skillSearchMsg{id: id, hits: hits, err: err}
		}
	case skillSearchMsg:
		if message.id != m.searchID {
			return m, nil
		}
		m.searching, m.results, m.searchErr = false, message.hits, ""
		if message.err != nil {
			m.results, m.searchErr = nil, message.err.Error()
		}
		m.clampCursor()
		return m, tea.Batch(m.fetchAudits(m.results), m.fetchPage())
	case skillAuditMsg:
		m.audits[message.source] = message.audits
	case skillPageMsg:
		if message.err != nil {
			delete(m.pages, message.key)
			m.pageErr[message.key] = message.err.Error()
		} else {
			m.pages[message.key] = message.meta
		}
	case skillNewerMsg:
		for _, row := range m.installed {
			if row.Name == message.name && row.Commit != "" && row.Commit != message.commit {
				m.newer[message.name] = message.commit
			}
		}
	case tea.KeyMsg:
		return m.key(message)
	}
	return m, nil
}

func (m *skillSelector) clampCursor() {
	count := len(m.items())
	if m.cursor >= count {
		m.cursor = count - 1
	}
	if m.cursor < -1 {
		m.cursor = -1
	}
}

func (m *skillSelector) typed(text string) tea.Cmd {
	m.query += text
	m.cursor = -1
	return m.searchSoon()
}

func (m *skillSelector) searchSoon() tea.Cmd {
	m.searchID++
	m.message = ""
	if len(strings.TrimSpace(m.query)) < 2 {
		m.results, m.searchErr, m.searching = nil, "", false
		return nil
	}
	id := m.searchID
	return tea.Tick(300*time.Millisecond, func(time.Time) tea.Msg { return skillSearchTickMsg{id: id} })
}

func (m *skillSelector) key(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch message.Type {
	case tea.KeyCtrlC, tea.KeyEsc:
		m.action = skillSelectorAction{}
		return m, tea.Quit
	case tea.KeyUp:
		if m.cursor > -1 {
			m.cursor--
		}
		return m, m.fetchPage()
	case tea.KeyDown, tea.KeyTab:
		if m.cursor < len(m.items())-1 {
			m.cursor++
		}
		return m, m.fetchPage()
	case tea.KeyBackspace:
		if m.query != "" {
			runes := []rune(m.query)
			m.query = string(runes[:len(runes)-1])
			m.cursor = -1
			return m, m.searchSoon()
		}
		return m, nil
	case tea.KeyEnter:
		return m.enter()
	case tea.KeySpace:
		if m.cursor < 0 {
			return m, m.typed(" ")
		}
		if item, ok := m.current(); ok && !item.installed {
			if _, picked := m.picked[item.key()]; picked {
				delete(m.picked, item.key())
			} else {
				m.picked[item.key()] = item.hit
			}
		}
		return m, nil
	case tea.KeyRunes:
		text := string(message.Runes)
		if m.cursor >= 0 {
			item, ok := m.current()
			switch text {
			case "u":
				if ok && item.installed {
					return m.finish(skillSelectorAction{kind: "update", name: item.row.Name})
				}
				return m, nil
			case "x":
				if ok && item.installed {
					return m.finish(skillSelectorAction{kind: "remove", name: item.row.Name})
				}
				return m, nil
			case "g":
				if err := m.load(!m.global); err != nil {
					m.message = err.Error()
				}
				return m, tea.Batch(m.checkNewer())
			}
		}
		return m, m.typed(text)
	}
	return m, nil
}

// enter installs the picked skills, or the highlighted one when nothing
// is picked; in the search box it moves to the list.
func (m *skillSelector) enter() (tea.Model, tea.Cmd) {
	if len(m.picked) > 0 {
		var hits []skillHit
		for _, hit := range m.picked {
			hits = append(hits, hit)
		}
		sort.Slice(hits, func(i, j int) bool { return hits[i].Source+hits[i].SkillID < hits[j].Source+hits[j].SkillID })
		return m.finish(skillSelectorAction{kind: "install", hits: hits})
	}
	if m.cursor < 0 {
		if len(m.items()) > 0 {
			m.cursor = 0
		}
		return m, m.fetchPage()
	}
	if item, ok := m.current(); ok && !item.installed {
		return m.finish(skillSelectorAction{kind: "install", hits: []skillHit{item.hit}})
	}
	return m, nil
}

func (m *skillSelector) finish(action skillSelectorAction) (tea.Model, tea.Cmd) {
	action.scope = m.global
	m.action = action
	return m, tea.Quit
}

// ---------------------------------------------------------------------------
// drawing

func (m *skillSelector) View() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dim := lipgloss.NewStyle().Foreground(colorDim)
	mint := lipgloss.NewStyle().Foreground(colorMint)
	width := max(m.width, 60)
	where, other := m.base, "everywhere"
	if m.global {
		where, other = "every project (~/.agents/skills)", "here"
	} else if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(where, home) {
		where = "~" + strings.TrimPrefix(where, home)
	}
	where = truncate(where, width-30)
	header := " Skills for " + where
	fmt.Fprintf(&b, "%s%s\n", title.Render(header), dim.Render(padLeft("g: "+other, width-1-len([]rune(header)))))
	cursorMark := " "
	if m.cursor < 0 {
		cursorMark = mint.Bold(true).Render("❯")
	}
	status := ""
	switch {
	case skillLookupsOff():
		status = "  search is off (DO_NOT_TRACK)"
	case m.searching:
		status = "  searching…"
	case m.searchErr != "":
		status = "  " + truncate(m.searchErr, width-40)
	}
	query := m.query + mint.Render("▏")
	if m.query == "" && m.cursor < 0 {
		query = mint.Render("▏") + dim.Render("type words to search, for example pdf")
	}
	fmt.Fprintf(&b, "%s %s %s%s\n\n", cursorMark, title.Render("Search skills.sh ›"), query, dim.Render(status))

	items := m.items()
	// Room for the header, two section titles, the details, and the help.
	rows := max(m.height-18, 4)
	if m.cursor >= m.offset+rows {
		m.offset = m.cursor - rows + 1
	}
	if m.cursor >= 0 && m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor < 0 {
		m.offset = 0
	}
	heading := ""
	for i := m.offset; i < len(items) && i < m.offset+rows; i++ {
		item := items[i]
		section := "Installed"
		if !item.installed {
			section = "Suggestions"
			if len(strings.TrimSpace(m.query)) >= 2 {
				section = fmt.Sprintf("Results for %q", m.query)
			}
		}
		if section != heading {
			fmt.Fprintf(&b, " %s\n", dim.Bold(true).Render(section))
			heading = section
		}
		mark := "  "
		if i == m.cursor {
			mark = " " + mint.Bold(true).Render("❯")
		}
		b.WriteString(mark + " " + m.itemLine(item, width-4, i == m.cursor) + "\n")
	}
	if len(items) == len(m.installed) && len(strings.TrimSpace(m.query)) >= 2 && !m.searching && m.searchErr == "" {
		fmt.Fprintf(&b, " %s\n   %s\n", dim.Bold(true).Render(fmt.Sprintf("Results for %q", m.query)), dim.Render("nothing found"))
	}
	b.WriteString("\n" + m.details(width))
	if m.message != "" {
		b.WriteString(" " + lipgloss.NewStyle().Foreground(colorAmber).Render(m.message) + "\n")
	}
	picked := ""
	if len(m.picked) > 0 {
		picked = " · " + mint.Bold(true).Render(fmt.Sprintf("%d picked", len(m.picked)))
	}
	fmt.Fprintf(&b, "\n %s%s\n %s\n", dim.Render("type: search · ↓↑: move · space: pick · enter: install"), picked,
		dim.Render("u: update · x: remove · g: here/everywhere · esc: quit"))
	return b.String()
}

func padLeft(text string, width int) string {
	if pad := width - len([]rune(text)); pad > 0 {
		return strings.Repeat(" ", pad) + text
	}
	return "  " + text
}

func (m *skillSelector) itemLine(item skillItem, width int, highlighted bool) string {
	text := lipgloss.NewStyle().Foreground(colorText)
	dim := lipgloss.NewStyle().Foreground(colorDim)
	mint := lipgloss.NewStyle().Foreground(colorMint)
	name := text
	if highlighted {
		name = mint.Bold(true)
	}
	// Each column is padded before it is colored, so the columns line up.
	col := func(value string, size int) string { return fmt.Sprintf("%-*s", size, truncate(value, size)) }
	if item.installed {
		status := item.row.Status
		statusStyle := dim
		if commit, ok := m.newer[item.row.Name]; ok {
			status = "newer commit " + shortCommit(commit) + " · u updates"
			statusStyle = lipgloss.NewStyle().Foreground(colorAmber)
		}
		commit := firstNonEmpty(shortCommit(item.row.Commit), "")
		rest := max(width-2-23-33-9, 0)
		return mint.Render("✓") + " " + name.Render(col(item.row.Name, 22)) + " " + dim.Render(col(item.row.Source, 32)) + " " +
			dim.Render(col(commit, 8)) + " " + statusStyle.Render(truncate(status, rest))
	}
	box := dim.Render("◻")
	if _, ok := m.picked[item.key()]; ok {
		box = mint.Bold(true).Render("◼")
	}
	source := item.hit.Source
	if item.hit.Source == "hi" {
		source = "built in"
	}
	sourceCell := dim.Render(col(source, 34))
	if item.hit.verified() && item.hit.Source != "hi" {
		source = truncate(source, 32)
		sourceCell = dim.Render(source) + " " + mint.Render("✓") + strings.Repeat(" ", max(34-len([]rune(source))-2, 0))
	}
	installs := ""
	if item.hit.Installs > 0 {
		installs = formatInstalls(item.hit.Installs)
	}
	rest := max(width-2-23-35-8, 0)
	var tail string
	switch {
	case item.why != "":
		installs = ""
		tail = dim.Render(truncate(item.why, rest))
	case m.asked[item.hit.Source]:
		tail = skillAuditColored(m.audits[item.hit.Source][item.hit.SkillID], rest)
	}
	return box + " " + name.Render(col(item.hit.SkillID, 22)) + " " + sourceCell + " " + dim.Render(fmt.Sprintf("%6s", installs)) + "  " + tail
}

// skillAuditColored is skillAuditShort with each verdict colored by risk.
func skillAuditColored(audits map[string]skillAudit, width int) string {
	plain := skillAuditShort(audits)
	if len([]rune(plain)) > width {
		return lipgloss.NewStyle().Foreground(colorDim).Render(truncate(plain, width))
	}
	var parts []string
	for _, word := range strings.Fields(plain) {
		parts = append(parts, lipgloss.NewStyle().Foreground(skillRiskColor(word)).Render(word))
	}
	return strings.Join(parts, " ")
}

func skillRiskColor(risk string) lipgloss.TerminalColor {
	switch risk {
	case "safe", "low":
		return colorMint
	case "medium":
		return colorAmber
	case "high", "critical":
		return colorRed
	}
	return colorDim
}

// details describes the highlighted skill.
func (m *skillSelector) details(width int) string {
	item, ok := m.current()
	if !ok {
		return " Type to search skills.sh, or ↓ to browse.\n"
	}
	var b strings.Builder
	if item.installed {
		fmt.Fprintf(&b, " %s\n", skillDetailsTitle(item.row.Name, item.row.Source, width))
		dir := skillInstallDir(m.base, item.row.Name)
		if item.row.Name == "hi" {
			fmt.Fprintf(&b, " Teaches agents to use hi; it comes with hi %s.\n", version)
			return b.String()
		}
		if data, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err == nil {
			meta := parseSkillFrontmatter(data)
			fmt.Fprintf(&b, " %s\n", truncate(clipText(meta["description"], 2*width), width*2))
		}
		fmt.Fprintf(&b, " %s\n", truncate(item.row.Status, width-2))
		return b.String()
	}
	fmt.Fprintf(&b, " %s\n", skillDetailsTitle(item.hit.SkillID, item.hit.Source, width))
	key := item.key()
	meta := m.pages[key]
	switch {
	case meta != nil:
		lines := wrapText(meta["description"], width-2)
		if len(lines) > 3 {
			lines = append(lines[:2], clipText(strings.Join(lines[2:], " "), width-2))
		}
		for _, line := range lines {
			fmt.Fprintf(&b, " %s\n", line)
		}
		facts := []string{"License: " + firstNonEmpty(meta["license"], "not stated")}
		if meta["allowed-tools"] != "" {
			facts = append(facts, "tools: "+meta["allowed-tools"])
		}
		fmt.Fprintf(&b, " %s\n", truncate(strings.Join(facts, " · "), width-2))
	case m.pageErr[key] != "":
		fmt.Fprintf(&b, " %s\n", truncate(m.pageErr[key], width-2))
	default:
		fmt.Fprintf(&b, " Reading its SKILL.md…\n")
	}
	if m.asked[item.hit.Source] {
		summary := truncate(skillAuditSummary(m.audits[item.hit.Source][item.hit.SkillID]), width-10)
		color := colorMint
		for _, word := range strings.Fields(summary) {
			if word == "medium" && color == colorMint {
				color = colorAmber
			}
			if word == "high" || word == "critical" {
				color = colorRed
			}
		}
		fmt.Fprintf(&b, " %s %s\n", lipgloss.NewStyle().Foreground(colorDim).Render("Audits:"), lipgloss.NewStyle().Foreground(color).Render(summary))
	}
	return b.String()
}

// skillDetailsTitle is the rule above the details: ── name · source ────
func skillDetailsTitle(name, source string, width int) string {
	rule := lipgloss.NewStyle().Foreground(colorLine)
	return rule.Render("──") + " " + lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(name) +
		lipgloss.NewStyle().Foreground(colorDim).Render(" · "+source) + " " +
		rule.Render(strings.Repeat("─", max(width-len([]rune(name))-len([]rune(source))-9, 3)))
}

func wrapText(text string, width int) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && len([]rune(line))+1+len([]rune(word)) > width {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// ---------------------------------------------------------------------------
// running it

func runSkillSelector(global bool, stdin io.Reader, stdout, stderr io.Writer) error {
	model, err := newSkillSelector(global, defaultSkillServices())
	if err != nil {
		return err
	}
	if _, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithInput(stdin), tea.WithOutput(stdout)).Run(); err != nil {
		return err
	}
	return runSkillSelectorAction(model.action, stdin, stdout)
}

// runSkillSelectorAction does what the selector asked for, with the usual
// questions, outside the full screen.
func runSkillSelectorAction(action skillSelectorAction, stdin io.Reader, stdout io.Writer) error {
	options := skillOptions{global: action.scope}
	switch action.kind {
	case "update":
		options.words = []string{action.name}
		return skillUpdate(options, stdin, stdout)
	case "remove":
		options.words = []string{action.name}
		return skillRemove(options, stdin, stdout)
	case "install":
		bySource := map[string][]string{}
		var sources []string
		for _, hit := range action.hits {
			if bySource[hit.Source] == nil {
				sources = append(sources, hit.Source)
			}
			bySource[hit.Source] = append(bySource[hit.Source], hit.SkillID)
		}
		if len(bySource["hi"]) > 0 {
			if err := writeHiSkill(action.scope, false, stdout); err != nil {
				return err
			}
			delete(bySource, "hi")
			sources = slicesDelete(sources, "hi")
		}
		var plans []skillPlan
		defer func() {
			for _, plan := range plans {
				plan.prepared.cleanup()
			}
		}()
		for _, name := range sources {
			fmt.Fprintf(stdout, "Fetching %s…\n", name)
			source, err := parseSkillSource(name)
			if err != nil {
				return err
			}
			prepared, err := prepareSkillSource(source)
			if err != nil {
				return err
			}
			plans = append(plans, skillPlan{prepared: prepared})
			chosen, err := chooseSkills(prepared, skillOptions{skills: bySource[name]}, stdin, stdout)
			if err != nil {
				return err
			}
			plans[len(plans)-1].chosen = chosen
		}
		if len(plans) == 0 {
			return nil
		}
		fmt.Fprintln(stdout)
		return installSkillPlans(plans, options, stdin, stdout)
	}
	return nil
}

func slicesDelete(list []string, value string) []string {
	var kept []string
	for _, item := range list {
		if item != value {
			kept = append(kept, item)
		}
	}
	return kept
}
