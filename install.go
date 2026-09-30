package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/charmbracelet/huh"
)

//go:embed scripts/install.sh
var setupScript []byte

// installItem is one checkbox in the `hi install` menu. Its id is also the
// name used by `hi install <tool>` and `hi uninstall <tool>`, and the name the
// setup script switches on.
type installItem struct {
	id          string
	title       string
	description string
	// commands and packages decide whether the item counts as installed:
	// every command must be found, or every Debian package installed.
	commands []string
	packages []string
	// essential items start checked on a machine that lacks them.
	essential bool
	// needs lists items that must be present to install this one.
	needs []string
	// keeps says what removal leaves behind, shown before removing.
	keeps string
	// action items run every time they are checked and are never installed.
	action bool
}

var installItems = []installItem{
	{id: "terminal", title: "Terminal tools", description: "tmux sessions and the btop system monitor",
		packages: []string{"tmux", "btop"}, essential: true},
	{id: "uv", title: "uv", description: "Python versions, projects, and tools",
		commands: []string{"uv"}, essential: true},
	{id: "gh", title: "GitHub CLI", description: "gh, for repositories, pull requests, and sign-in",
		packages: []string{"gh"}, essential: true},
	{id: "node", title: "Node.js", description: "node and npm from Ubuntu",
		packages: []string{"nodejs", "npm"}},
	{id: "docker", title: "Docker", description: "Docker Engine, Buildx, and Compose",
		packages: []string{"docker-ce"}, keeps: "Docker keeps its images and volumes in /var/lib/docker."},
	{id: "claude", title: "Claude Code", description: "Anthropic's coding agent (claude)",
		commands: []string{"claude"}, keeps: "Claude Code keeps its settings in ~/.claude."},
	{id: "codex", title: "Codex CLI", description: "OpenAI's coding agent (codex)",
		commands: []string{"codex"}, keeps: "Codex keeps its settings and sign-in in ~/.codex."},
	{id: "omp", title: "omp", description: "oh-my-pi, a terminal coding agent",
		commands: []string{"omp"}, keeps: "omp keeps its settings in ~/.omp."},
	{id: "herdr", title: "herdr", description: "runs and watches several coding agents at once",
		commands: []string{"herdr"}, keeps: "herdr keeps its settings in ~/.config/herdr."},
	{id: "netbird", title: "NetBird", description: "private network between team machines (hi net)",
		packages: []string{"netbird"},
		keeps:    "Removing NetBird disconnects this machine from the team network, including SSH sessions that use it."},
	{id: "colab", title: "Colab CLI", description: "runs work on Google Colab (hi compute)",
		commands: []string{"colab"}, needs: []string{"uv"}},
	{id: "hf", title: "Hugging Face CLI", description: "hf, for models, datasets, and HF Jobs",
		commands: []string{"hf"}, keeps: "Your Hugging Face sign-in stays in ~/.cache/huggingface."},
	{id: "strix", title: "Strix Halo support", description: "AMD ROCm, GPU groups, and amd-debug-tools",
		packages: []string{"amdrocm10.0-gfx1151"},
		keeps:    "You stay in the render and video groups."},
	{id: "upgrade", title: "Update everything", description: "upgrade Ubuntu packages and reinstall checked tools",
		action: true},
}

func findInstallItem(id string) (installItem, bool) {
	for _, item := range installItems {
		if item.id == id {
			return item, true
		}
	}
	return installItem{}, false
}

// itemInstalled reports whether an item is present on this machine. Tests
// replace it.
var itemInstalled = func(item installItem) bool {
	if item.action {
		return false
	}
	for _, name := range item.packages {
		if !packageInstalled(name) {
			return false
		}
	}
	for _, name := range item.commands {
		if !toolAvailable(name) && !localCommand(name) {
			return false
		}
	}
	return true
}

// localCommand also finds symlinked commands in ~/.local/bin, which
// toolAvailable skips; ~/.local/bin is often not on PATH yet after a first
// install.
func localCommand(name string) bool {
	current, err := user.Current()
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(current.HomeDir, ".local", "bin", name))
	return err == nil && info.Mode().IsRegular()
}

// strixHardware reports whether this machine has a Strix Halo GPU (Radeon
// 8060S or 8050S). Tests replace it.
var strixHardware = func() bool {
	if runtime.GOARCH != "amd64" {
		return false
	}
	devices, _ := filepath.Glob("/sys/bus/pci/devices/*")
	for _, device := range devices {
		vendor, _ := os.ReadFile(filepath.Join(device, "vendor"))
		id, _ := os.ReadFile(filepath.Join(device, "device"))
		if strings.TrimSpace(string(vendor)) == "0x1002" && strings.TrimSpace(string(id)) == "0x1586" {
			return true
		}
	}
	return false
}

// installPlan is what one run of the setup script does.
type installPlan struct {
	install []string
	remove  []string
	upgrade bool
}

func (p installPlan) empty() bool {
	return len(p.install) == 0 && len(p.remove) == 0 && !p.upgrade
}

// planFromMenu turns the checked ids into changes. Checked tools that are
// missing get installed, unchecked tools that are present get removed, and
// checked tools that are present stay as they are unless "Update everything"
// is checked too.
func planFromMenu(checked []string, installed map[string]bool) (installPlan, error) {
	isChecked := make(map[string]bool)
	for _, id := range checked {
		isChecked[id] = true
	}
	plan := installPlan{upgrade: isChecked["upgrade"]}
	for _, item := range installItems {
		switch {
		case item.action:
		case isChecked[item.id] && (!installed[item.id] || plan.upgrade):
			plan.install = append(plan.install, item.id)
		case !isChecked[item.id] && installed[item.id]:
			plan.remove = append(plan.remove, item.id)
		}
	}
	return plan, checkNeeds(plan.install, func(id string) bool {
		return isChecked[id]
	})
}

func checkNeeds(install []string, present func(string) bool) error {
	for _, id := range install {
		item, _ := findInstallItem(id)
		for _, need := range item.needs {
			if !present(need) {
				other, _ := findInstallItem(need)
				return fmt.Errorf("%s needs %s; include %s as well", item.title, other.title, other.title)
			}
		}
	}
	return nil
}

// orderedIDs validates tool names from the command line and returns them in
// menu order without duplicates.
func orderedIDs(names []string, allowAction bool) ([]string, error) {
	wanted := make(map[string]bool)
	for _, name := range names {
		item, ok := findInstallItem(name)
		if !ok || (item.action && !allowAction) {
			return nil, fmt.Errorf("unknown tool %q; run `hi install --list` to see them", name)
		}
		wanted[name] = true
	}
	var ids []string
	for _, item := range installItems {
		if wanted[item.id] {
			ids = append(ids, item.id)
		}
	}
	return ids, nil
}

func runInstall(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var names []string
	all, list := false, false
	for _, arg := range args {
		switch arg {
		case "--all":
			all = true
		case "--list":
			list = true
		case "-h", "--help":
			printInstallUsage(stdout)
			return 0
		default:
			if strings.HasPrefix(arg, "-") {
				printInstallUsage(stderr)
				return 2
			}
			names = append(names, arg)
		}
	}
	if list {
		if all || len(names) > 0 {
			printInstallUsage(stderr)
			return 2
		}
		printInstallList(stdout)
		return 0
	}

	var err error
	switch {
	// Without tools, or with only the old `strix` profile, show the menu.
	case !all && (len(names) == 0 || (len(names) == 1 && names[0] == "strix")) && interactive(stdin, stdout):
		err = installMenu(stdin, stdout, stderr, len(names) == 1)
	case !all && len(names) == 0:
		fmt.Fprintln(stderr, "hi: the install menu needs a terminal; name the tools instead, as in `hi install claude gh`, or use `hi install --all`")
		printInstallList(stderr)
		return 2
	default:
		err = installNamed(names, all, stdin, stdout, stderr)
	}
	return exitCode(err, stderr)
}

func runUninstall(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: hi uninstall <tool>...")
		printInstallList(stderr)
		return 2
	}
	ids, err := orderedIDs(args, false)
	if err != nil {
		return exitCode(err, stderr)
	}
	plan := installPlan{remove: ids}
	if interactive(stdin, stdout) {
		ok, err := newMenuUI(stdin, stdout).confirm("Remove these tools?", planSummary(plan), true)
		if err != nil || !ok {
			return 0
		}
	}
	return exitCode(runSetup(plan, "", stdin, stdout, stderr), stderr)
}

func printInstallUsage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  hi install                 Choose tools to install or remove from a menu
  hi install <tool>...       Install or reinstall the named tools
  hi install --all [tool...] Install every tool (Strix Halo support only when named)
  hi install --list          List the tools and whether they are installed
  hi uninstall <tool>...     Remove the named tools`)
}

func printInstallList(w io.Writer) {
	for _, item := range installItems {
		status := ""
		if item.action {
			status = "action"
		} else if itemInstalled(item) {
			status = "installed"
		}
		fmt.Fprintf(w, "  %-9s %-19s %-10s %s\n", item.id, item.title, status, item.description)
	}
}

func interactive(stdin io.Reader, stdout io.Writer) bool {
	file, ok := stdout.(*os.File)
	return isTerminal(stdin) && ok && isTerminal(file)
}

func checkInstallHost() error {
	if runtime.GOOS != "linux" {
		return errors.New("the installer supports Linux only")
	}
	if os.Geteuid() == 0 {
		return errors.New("run this command as your regular user; it uses sudo when needed")
	}
	return nil
}

func installNamed(names []string, all bool, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := checkInstallHost(); err != nil {
		return err
	}
	if all {
		for _, item := range installItems {
			if !item.action && item.id != "strix" {
				names = append(names, item.id)
			}
		}
	}
	ids, err := orderedIDs(names, true)
	if err != nil {
		return err
	}
	plan := installPlan{}
	for _, id := range ids {
		if id == "upgrade" {
			plan.upgrade = true
		} else {
			plan.install = append(plan.install, id)
		}
	}
	requested := make(map[string]bool)
	for _, id := range plan.install {
		requested[id] = true
	}
	if err := checkNeeds(plan.install, func(id string) bool {
		item, _ := findInstallItem(id)
		return requested[id] || itemInstalled(item)
	}); err != nil {
		return err
	}
	return runSetup(plan, "", stdin, stdout, stderr)
}

func installMenu(stdin io.Reader, stdout, stderr io.Writer, strix bool) error {
	if err := checkInstallHost(); err != nil {
		return err
	}
	ui := &styledUI{in: stdin, out: stdout}

	installed := make(map[string]bool)
	var hardware bool
	ui.busy("Checking what is installed…", func() {
		for _, item := range installItems {
			installed[item.id] = itemInstalled(item)
		}
		hardware = strixHardware()
	})

	titleWidth, descriptionWidth := 0, 0
	for _, item := range installItems {
		titleWidth = max(titleWidth, len(item.title))
		descriptionWidth = max(descriptionWidth, len(item.description))
	}
	var options []huh.Option[string]
	for _, item := range installItems {
		if item.id == "strix" && runtime.GOARCH != "amd64" {
			continue
		}
		label := fmt.Sprintf("%-*s  %-*s", titleWidth, item.title, descriptionWidth, item.description)
		if installed[item.id] {
			label += "  installed"
		}
		label = strings.TrimRight(label, " ")
		checked := installed[item.id] || item.essential
		if item.id == "strix" {
			checked = installed[item.id] || hardware || strix
		}
		options = append(options, huh.NewOption(label, item.id).Selected(checked))
	}

	var checked []string
	field := huh.NewMultiSelect[string]().
		Title("What should this machine have?").
		Description("Space toggles, enter continues. Unchecking an installed tool removes it.").
		Options(options...).
		Height(len(options) + 4).
		Value(&checked).
		Validate(func(ids []string) error {
			_, err := planFromMenu(ids, installed)
			return err
		})
	theme := hiTheme()
	theme.Focused.SelectedPrefix = theme.Focused.SelectedPrefix.SetString("[x] ")
	theme.Focused.UnselectedPrefix = theme.Focused.UnselectedPrefix.SetString("[ ] ")
	err := huh.NewForm(huh.NewGroup(field)).
		WithTheme(theme).
		WithInput(stdin).
		WithOutput(stdout).
		WithShowHelp(true).
		Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return nil
	}
	if err != nil {
		return err
	}

	plan, err := planFromMenu(checked, installed)
	if err != nil {
		return err
	}
	if plan.empty() {
		ui.note("Nothing to change.")
		return nil
	}

	hostname := ""
	if len(plan.install) > 0 {
		current, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("read current hostname: %w", err)
		}
		hostname, err = ui.input("Hostname (Enter keeps "+current+")", current, func(value string) error {
			if value != "" && !validHostname(value) {
				return errors.New("use dot-separated letters, digits, or hyphens (maximum 64 characters)")
			}
			return nil
		})
		if err != nil {
			return nil
		}
		if hostname == current {
			hostname = ""
		}
	}

	ok, err := ui.confirm("Go ahead?", planSummary(plan), len(plan.remove) > 0)
	if err != nil || !ok {
		return nil
	}
	return runSetup(plan, hostname, stdin, stdout, stderr)
}

func planSummary(plan installPlan) string {
	titles := func(ids []string) string {
		var names []string
		for _, id := range ids {
			item, _ := findInstallItem(id)
			names = append(names, item.title)
		}
		return strings.Join(names, ", ")
	}
	var lines []string
	if len(plan.install) > 0 {
		lines = append(lines, "Install: "+titles(plan.install))
	}
	if len(plan.remove) > 0 {
		lines = append(lines, "Remove:  "+titles(plan.remove))
	}
	if plan.upgrade {
		lines = append(lines, "Upgrade: Ubuntu packages")
	}
	for _, id := range plan.remove {
		if item, _ := findInstallItem(id); item.keeps != "" {
			lines = append(lines, item.keeps)
		}
	}
	return strings.Join(lines, "\n")
}

// runSetup runs the embedded setup script for a plan. stdin is passed on so
// sudo and apt can ask questions.
func runSetup(plan installPlan, hostname string, stdin io.Reader, stdout, stderr io.Writer) error {
	if err := checkInstallHost(); err != nil {
		return err
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		return errors.New("bash is required")
	}

	script, err := os.CreateTemp("", "hi-install-*.sh")
	if err != nil {
		return fmt.Errorf("create temporary setup script: %w", err)
	}
	path := script.Name()
	defer os.Remove(path)

	if err := script.Chmod(0o700); err != nil {
		script.Close()
		return fmt.Errorf("secure temporary setup script: %w", err)
	}
	if _, err := script.Write(setupScript); err != nil {
		script.Close()
		return fmt.Errorf("write temporary setup script: %w", err)
	}
	if err := script.Close(); err != nil {
		return fmt.Errorf("close temporary setup script: %w", err)
	}

	upgrade := "0"
	if plan.upgrade {
		upgrade = "1"
	}
	cmd := exec.Command(bash, path, hostname,
		strings.Join(plan.install, " "), strings.Join(plan.remove, " "), upgrade)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("setup failed with exit code %d", exitErr.ExitCode())
		}
		return fmt.Errorf("start setup: %w", err)
	}

	for _, id := range plan.install {
		if id == "strix" {
			if result := verifyStrix(stdout); result.failures > 0 {
				return fmt.Errorf("installation completed with %d failed report check(s)", result.failures)
			}
		}
	}
	return nil
}
