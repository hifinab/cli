package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ---------------------------------------------------------------------------
// where the dashboard gets its data

// liveSource reads snapshots and acts on them. Only the admin source can
// approve and deny; users may stop their own instances.
type liveSource interface {
	snapshot() (liveSnapshot, error)
	approve(id string) error
	deny(id, reason string) error
	stop(name string) error
	canDecide() bool
}

// adminLiveSource reads through the server's admin socket, on the server box.
type adminLiveSource struct {
	dir   string
	actor string
}

func (a adminLiveSource) snapshot() (liveSnapshot, error) {
	var snapshot liveSnapshot
	err := adminCall(a.dir, http.MethodGet, "/admin/live", nil, &snapshot)
	return snapshot, err
}

func (a adminLiveSource) approve(id string) error {
	return adminCall(a.dir, http.MethodPost, "/admin/requests/"+id+"/approve", map[string]string{"as": a.actor}, nil)
}

func (a adminLiveSource) deny(id, reason string) error {
	return adminCall(a.dir, http.MethodPost, "/admin/requests/"+id+"/deny", map[string]string{"as": a.actor, "reason": reason}, nil)
}

func (a adminLiveSource) stop(name string) error {
	return adminCall(a.dir, http.MethodPost, "/admin/stop", map[string]any{"as": a.actor, "name": name}, nil)
}

func (adminLiveSource) canDecide() bool { return true }

// clientLiveSource reads through this device: a viewer sees everything, a
// user their own part.
type clientLiveSource struct{ client *serverClient }

func (c clientLiveSource) snapshot() (liveSnapshot, error) {
	var snapshot liveSnapshot
	err := c.client.call(http.MethodGet, "/v1/live", nil, &snapshot)
	return snapshot, err
}

func (clientLiveSource) approve(string) error {
	return errors.New("approve from Slack or the server box")
}

func (clientLiveSource) deny(string, string) error {
	return errors.New("deny from Slack or the server box")
}

func (c clientLiveSource) stop(name string) error {
	return c.client.call(http.MethodPost, "/v1/instances/"+url.PathEscape(name)+"/stop", nil, nil)
}

func (clientLiveSource) canDecide() bool { return false }

// ---------------------------------------------------------------------------
// rendering

type liveOptions struct {
	wall      bool
	fullNames bool
	reasons   bool
	// title names the view, such as "hi compute · live".
	title string
	// decide shows the approve and deny keys.
	decide bool
}

type liveState struct {
	width, height int
	cursor        int
	stale         bool
	lastOK        time.Time
	prompt        string
	message       string
	rotate        int
}

var (
	liveTitle   = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	liveDim     = lipgloss.NewStyle().Foreground(colorDim)
	liveText    = lipgloss.NewStyle().Foreground(colorText)
	liveBold    = lipgloss.NewStyle().Bold(true).Foreground(colorText)
	liveMint    = lipgloss.NewStyle().Foreground(colorMint)
	liveAmber   = lipgloss.NewStyle().Foreground(colorAmber)
	liveRed     = lipgloss.NewStyle().Foreground(colorRed)
	liveHeading = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
	liveCursor  = lipgloss.NewStyle().Bold(true).Foreground(colorAccent)
)

// displayName shows a person's initials on a wall, their name elsewhere.
func (o liveOptions) displayName(name string) string {
	if !o.wall || o.fullNames {
		return name
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
	var initials strings.Builder
	for _, part := range parts {
		initials.WriteString(strings.ToUpper(part[:1]))
	}
	if len(parts) == 1 {
		initials.WriteString(".")
	}
	return initials.String()
}

func (o liveOptions) showReasons() bool { return !o.wall || o.reasons }

// bar draws progress towards a limit: mint, amber from 80%, red at the end.
func bar(done, total time.Duration, width int) string {
	if total <= 0 {
		return strings.Repeat("·", width)
	}
	share := float64(done) / float64(total)
	filled := int(share*float64(width) + 0.5)
	filled = max(0, min(width, filled))
	text := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	switch {
	case share >= 1:
		return liveRed.Render(text)
	case share >= slackWarnAt:
		return liveAmber.Render(text)
	}
	return liveMint.Render(text)
}

func pad(text string, width int) string {
	if visible := lipgloss.Width(text); visible < width {
		return text + strings.Repeat(" ", width-visible)
	}
	return text
}

func truncate(text string, width int) string {
	if width <= 1 || lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// renderLive draws the whole screen for one snapshot.
func renderLive(snapshot liveSnapshot, options liveOptions, state liveState) string {
	width := max(state.width, 60)
	now := snapshot.Now
	if now.IsZero() {
		now = time.Now()
	}
	var lines []string
	add := func(line string) { lines = append(lines, truncate(line, width)) }

	title := options.title
	if title == "" {
		title = "hi compute · live"
	}
	clock := now.Local().Format("Mon 2 Jan 15:04:05")
	add(liveTitle.Render(title) + strings.Repeat(" ", max(1, width-lipgloss.Width(title)-len(clock))) + liveDim.Render(clock))
	stats := fmt.Sprintf("%d running   %s/h now   %s today   %s this month",
		len(snapshot.Running), formatDollars(snapshot.RatePerHour), formatDollars(snapshot.Today), formatDollars(snapshot.Month))
	if snapshot.MonthBudget > 0 {
		stats += " of " + formatDollars(snapshot.MonthBudget)
	}
	add(liveBold.Render(stats))
	add(liveDim.Render(strings.Repeat("─", width)))

	selectable := 0
	cursorMark := func() string {
		mark := "  "
		if !options.wall && state.cursor == selectable {
			mark = liveCursor.Render("▸ ")
		}
		selectable++
		return mark
	}

	var running []string
	if len(snapshot.Running) == 0 {
		running = append(running, liveDim.Render("  Nothing is running."))
	}
	for _, instance := range snapshot.Running {
		who := options.displayName(instance.User)
		if instance.Agent != "" {
			who += " via " + instance.Agent
		}
		up := now.Sub(instance.Started)
		row := fmt.Sprintf("%s%s %s %s %s %s  %s  %s %s", cursorMark(),
			pad(liveBold.Render(truncate(instance.Name, 18)), 18), pad(who, 14), pad(liveDim.Render(instance.Group), 9),
			pad(instance.Provider+" "+truncate(instance.Hardware, 20), 30), pad(formatDuration(up), 6),
			pad(formatDollars(instance.Cost), 7), bar(up, instance.Deadline.Sub(instance.Started), 10),
			liveDim.Render("stops "+instance.Deadline.Local().Format("15:04")))
		running = append(running, row)
		var detail []string
		if options.showReasons() && instance.Reason != "" {
			detail = append(detail, instance.Reason)
		}
		if len(instance.Doing) > 0 {
			detail = append(detail, liveMint.Render(strings.Join(instance.Doing, " · ")))
		}
		if isCommunityHardware(instance.Hardware) {
			detail = append(detail, liveAmber.Render("community cloud"))
		}
		if len(detail) > 0 {
			running = append(running, "    "+liveDim.Render(strings.Join(detail, "   ")))
		}
	}

	var waiting []string
	for _, request := range snapshot.Waiting {
		what := request.Hardware
		if request.Kind == "extend" {
			what = request.Name + " +" + formatDuration(time.Duration(request.Seconds)*time.Second)
		} else {
			what += " " + formatDuration(time.Duration(request.Seconds)*time.Second)
		}
		state := "waiting " + formatDuration(now.Sub(request.Created))
		if request.State == "starting" {
			state = liveMint.Render("starting")
		}
		row := fmt.Sprintf("%s%s %s %s %s", cursorMark(), pad(options.displayName(request.User), 10),
			pad(liveDim.Render(request.Group), 9), pad(what, 22), state)
		if request.Over {
			row += liveAmber.Render("  over budget")
		}
		waiting = append(waiting, row)
		if options.showReasons() && request.Reason != "" {
			waiting = append(waiting, "    "+liveDim.Render(truncate(request.Reason, width-6)))
		}
	}
	if len(waiting) == 0 {
		waiting = append(waiting, liveDim.Render("  Nothing is waiting."))
	}

	var budgets []string
	for _, budget := range snapshot.Budgets {
		share := time.Duration(0)
		if budget.Budget > 0 {
			share = time.Duration(budget.Spend / budget.Budget * float64(time.Hour))
		}
		budgets = append(budgets, fmt.Sprintf("  %s %s  %s of %s", pad(options.displayName(budget.Group), 10),
			bar(share, time.Hour, 16), formatDollars(budget.Spend), formatDollars(budget.Budget)))
	}

	var activity []string
	for _, event := range snapshot.Activity {
		text := event.Text
		if options.wall && !options.fullNames && event.User != "" && strings.HasPrefix(text, event.User+" ") {
			text = options.displayName(event.User) + strings.TrimPrefix(text, event.User)
		}
		activity = append(activity, fmt.Sprintf("  %s  %s", liveDim.Render(event.Time.Local().Format("15:04")), text))
	}

	section := func(heading string, body []string) []string {
		return append([]string{liveHeading.Render(heading)}, body...)
	}
	lines = append(lines, section("Running", running)...)
	lines = append(lines, "")

	// What else fits: everything in a tall window; on a wall, the rest
	// takes turns every 20 seconds.
	rest := [][]string{section("Waiting", waiting)}
	if len(budgets) > 0 {
		rest = append(rest, section("Budgets this month", budgets))
	}
	if len(activity) > 0 {
		rest = append(rest, section("Activity", activity))
	}
	footer := []string{}
	if state.stale {
		footer = append(footer, liveAmber.Render(fmt.Sprintf("Reconnecting… last update %s", state.lastOK.Local().Format("15:04:05"))))
	}
	if state.prompt != "" {
		footer = append(footer, liveBold.Render(state.prompt))
	} else if state.message != "" {
		footer = append(footer, liveText.Render(state.message))
	}
	if !options.wall {
		help := "↑↓ select · s stop · q quit"
		if options.decide {
			help = "↑↓ select · a approve · d deny · s stop · q quit"
		}
		footer = append(footer, liveDim.Render(help))
	}

	room := state.height - len(lines) - len(footer)
	if state.height <= 0 {
		room = 1 << 20
	}
	total := 0
	for _, block := range rest {
		total += len(block) + 1
	}
	if total <= room || !options.wall {
		for _, block := range rest {
			if room <= 1 {
				break
			}
			if len(block) > room-1 {
				block = block[:room-1]
			}
			lines = append(lines, block...)
			lines = append(lines, "")
			room -= len(block) + 1
		}
	} else if len(rest) > 0 {
		block := rest[state.rotate%len(rest)]
		if len(block) > room {
			block = block[:max(room, 0)]
		}
		lines = append(lines, block...)
	}
	for _, line := range footer {
		lines = append(lines, truncate(line, width))
	}
	screen := strings.Join(lines, "\n")
	if state.stale {
		return lipgloss.NewStyle().Faint(true).Render(screen)
	}
	return screen
}

// liveItems is what the cursor can select: running instances, then waiting
// requests.
func liveItems(snapshot liveSnapshot) (running []string, waiting []string) {
	for _, instance := range snapshot.Running {
		running = append(running, instance.Name)
	}
	for _, request := range snapshot.Waiting {
		waiting = append(waiting, request.ID)
	}
	return running, waiting
}

// ---------------------------------------------------------------------------
// the program

type liveModel struct {
	source  liveSource
	options liveOptions
	state   liveState

	snapshot liveSnapshot
	have     bool
	// mode is "", "stop" (confirming a stop), or "deny" (typing a reason).
	mode   string
	target string
	input  string
	quit   bool
}

type liveTickMsg time.Time
type liveDataMsg struct {
	snapshot liveSnapshot
	err      error
}
type liveDoneMsg struct{ text string }

func (m *liveModel) fetch() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := m.source.snapshot()
		return liveDataMsg{snapshot: snapshot, err: err}
	}
}

func liveTick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return liveTickMsg(t) })
}

func (m *liveModel) Init() tea.Cmd { return tea.Batch(m.fetch(), liveTick()) }

func (m *liveModel) act(work func() error, done string) tea.Cmd {
	return func() tea.Msg {
		if err := work(); err != nil {
			return liveDoneMsg{text: "hi: " + err.Error()}
		}
		return liveDoneMsg{text: done}
	}
}

func (m *liveModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.state.width, m.state.height = message.Width, message.Height
	case liveTickMsg:
		if time.Now().Unix()%20 == 0 {
			m.state.rotate++
		}
		return m, tea.Batch(m.fetch(), liveTick())
	case liveDataMsg:
		if message.err != nil {
			m.state.stale = m.have
			if !m.have {
				m.state.message = "hi: " + message.err.Error()
			}
			return m, nil
		}
		m.snapshot, m.have, m.state.stale, m.state.lastOK = message.snapshot, true, false, time.Now()
		running, waiting := liveItems(m.snapshot)
		m.state.cursor = min(m.state.cursor, max(len(running)+len(waiting)-1, 0))
	case liveDoneMsg:
		m.state.message = message.text
		return m, m.fetch()
	case tea.KeyMsg:
		return m.key(message)
	}
	return m, nil
}

func (m *liveModel) key(message tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := message.String()
	if key == "ctrl+c" || (m.mode == "" && key == "q") {
		m.quit = true
		return m, tea.Quit
	}
	if m.options.wall {
		return m, nil
	}
	switch m.mode {
	case "stop":
		m.mode, m.state.prompt = "", ""
		if key == "y" {
			name := m.target
			return m, m.act(func() error { return m.source.stop(name) }, "Stopped "+name+".")
		}
		m.state.message = "Nothing was stopped."
		return m, nil
	case "deny":
		switch message.Type {
		case tea.KeyEnter:
			id, reason := m.target, strings.TrimSpace(m.input)
			m.mode, m.state.prompt, m.input = "", "", ""
			return m, m.act(func() error { return m.source.deny(id, reason) }, "Denied "+id+".")
		case tea.KeyEsc:
			m.mode, m.state.prompt, m.input = "", "", ""
		case tea.KeyBackspace:
			if len(m.input) > 0 {
				m.input = m.input[:len(m.input)-1]
			}
		case tea.KeySpace:
			m.input += " "
		case tea.KeyRunes:
			m.input += string(message.Runes)
		}
		if m.mode == "deny" {
			m.state.prompt = "Why deny " + m.target + "? (Enter sends, Esc cancels) " + m.input
		}
		return m, nil
	}
	running, waiting := liveItems(m.snapshot)
	count := len(running) + len(waiting)
	selected := func() (string, bool) {
		if m.state.cursor < len(running) {
			return running[m.state.cursor], true
		}
		if index := m.state.cursor - len(running); index < len(waiting) {
			return waiting[index], false
		}
		return "", false
	}
	switch key {
	case "up", "k":
		m.state.cursor = max(m.state.cursor-1, 0)
	case "down", "j":
		m.state.cursor = min(m.state.cursor+1, max(count-1, 0))
	case "s":
		if name, isRunning := selected(); isRunning && name != "" {
			m.mode, m.target = "stop", name
			m.state.prompt = "Stop " + name + "? It is terminated with everything on its disk. [y/N]"
		}
	case "a":
		if id, isRunning := selected(); !isRunning && id != "" {
			if !m.source.canDecide() {
				m.state.message = "Approve from Slack or the server box."
				return m, nil
			}
			return m, m.act(func() error { return m.source.approve(id) }, "Approved "+id+".")
		}
	case "d":
		if id, isRunning := selected(); !isRunning && id != "" {
			if !m.source.canDecide() {
				m.state.message = "Deny from Slack or the server box."
				return m, nil
			}
			m.mode, m.target, m.input = "deny", id, ""
			m.state.prompt = "Why deny " + id + "? (Enter sends, Esc cancels) "
		}
	}
	return m, nil
}

func (m *liveModel) View() string {
	if !m.have {
		if m.state.message != "" {
			return m.state.message + "\n"
		}
		return "Connecting…\n"
	}
	return renderLive(m.snapshot, m.options, m.state)
}

// runLive shows the dashboard until q, or prints one frame with --once.
func runLive(source liveSource, options liveOptions, once bool, width int, stdin io.Reader, stdout io.Writer) error {
	if once {
		snapshot, err := source.snapshot()
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, renderLive(snapshot, options, liveState{width: width}))
		return nil
	}
	if !isTerminal(stdin) {
		return usageError{"the live dashboard needs a terminal; use --once to print it once"}
	}
	model := &liveModel{source: source, options: options}
	_, err := tea.NewProgram(model, tea.WithAltScreen(), tea.WithInput(stdin), tea.WithOutput(stdout)).Run()
	return err
}

// ---------------------------------------------------------------------------
// commands

func serverLiveCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags, dirFlag := serverFlags("live", stderr)
	wall := flags.Bool("wall", false, "read-only, for a shared screen")
	names := flags.String("names", "initials", "on a wall: initials or full")
	reasons := flags.Bool("reasons", false, "on a wall: show why each machine was requested")
	as := flags.String("as", currentUserName(), "who is acting")
	once := flags.Bool("once", false, "print one frame and exit")
	width := flags.Int("width", 100, "with --once: the width to draw")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 0 || (*names != "initials" && *names != "full") {
		return usageError{"usage: hi server live [--wall [--names initials|full] [--reasons]] [--once]"}
	}
	options := liveOptions{wall: *wall, fullNames: *names == "full", reasons: *reasons}
	// On the server box, read through the admin socket; anywhere else, through
	// this device's connection, which must be a viewer to see everything.
	var source liveSource
	if dir, err := serverDirectory(*dirFlag); err == nil {
		if _, err := adminClient(dir); err == nil {
			source = adminLiveSource{dir: *dirFlag, actor: *as}
		}
	}
	if source == nil {
		client, err := connectedClient()
		if err != nil {
			return fmt.Errorf("run this on the server box, or on a device connected as a viewer: %w", err)
		}
		source = clientLiveSource{client: client}
	}
	options.decide = source.canDecide()
	if !*wall && !options.decide {
		options.title = "hi compute · live (stop only; approve in Slack)"
	}
	return runLive(source, options, *once, *width, stdin, stdout)
}

func computeLiveCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	flags := &flagSet{newComputeFlags("live", stderr)}
	once := flags.Bool("once", false, "print one frame and exit")
	width := flags.Int("width", 100, "with --once: the width to draw")
	positional, err := flags.parse(args)
	if err != nil {
		return err
	}
	if len(positional) != 0 {
		return usageError{"usage: hi compute live [--once]"}
	}
	client, err := connectedClient()
	if err != nil {
		return err
	}
	return runLive(clientLiveSource{client: client}, liveOptions{title: "hi compute · your machines"}, *once, *width, stdin, stdout)
}

// connectedClient is this device's signed connection to its hi server.
func connectedClient() (*serverClient, error) {
	connection, err := loadServerConnection()
	if err != nil {
		return nil, err
	}
	if connection == nil {
		return nil, errors.New("not connected to a hi server; see `hi connect`")
	}
	key, err := loadDeviceKey(false)
	if err != nil {
		return nil, err
	}
	return newServerClient(connection.URL, key), nil
}

// reportActivity tells the server that a managed instance is being used
// through ssh, a tunnel, logs, or serve, and returns a function that says
// it has ended. Only the command and instance names are sent.
func reportActivity(provider computeProvider, name, command string) func() {
	managed, ok := provider.(*managedProvider)
	if !ok {
		return func() {}
	}
	send := func(open bool) {
		client, err := managed.serverClient()
		if err != nil {
			return
		}
		quick := *client
		quick.http = &http.Client{Timeout: 3 * time.Second}
		_ = quick.call(http.MethodPost, "/v1/activity", apiActivity{Instance: name, Command: command, Open: open}, nil)
	}
	send(true)
	return func() { send(false) }
}
