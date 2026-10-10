package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/slack-go/slack"
)

// hi team runs a virtual team of agents in one Slack channel: a lead
// (Hermes) that talks with the people and keeps the project's memory, and
// members (Claude Code, Codex, Hermes) it hands tasks to, each in a box of
// its own. A team is a folder; its containers are rebuilt from it. See
// docs/specs/ideas/hi_team.md.

var (
	teamNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)
	teamRolePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,15}$`)
	slackIDPattern  = regexp.MustCompile(`^[A-Z0-9]{6,20}$`)
)

// teamConfig is team.json. It holds no secrets.
type teamConfig struct {
	Name    string                `json:"name"`
	Purpose string                `json:"purpose"`
	Channel string                `json:"channel"`
	Owners  []string              `json:"owners"`
	Askers  []string              `json:"askers,omitempty"` // empty: everyone in the channel
	Members map[string]teamMember `json:"members"`
	Remote  string                `json:"remote,omitempty"` // where repo/ was cloned from
	Branch  string                `json:"branch"`           // the branch the lead merges into
	Created time.Time             `json:"created"`
}

// teamMember is one role: an agent, its model, and what its box may use.
type teamMember struct {
	Agent   string   `json:"agent"`
	Model   string   `json:"model,omitempty"`
	Bundle  string   `json:"bundle,omitempty"`
	Network string   `json:"network,omitempty"` // a member box's network preset; dev by default
	Allow   []string `json:"allow,omitempty"`
}

// teamSecrets is secrets.json, readable only by its owner.
type teamSecrets struct {
	SlackBotToken string `json:"slack_bot_token"`
	SlackAppToken string `json:"slack_app_token"`
}

func teamsDir() string {
	if dir := os.Getenv("HI_TEAMS_DIR"); dir != "" {
		return dir
	}
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "hi", "teams")
}

// A team's folder is wherever it was made, ./team-<name> by default.
// teamsDir holds a link to each, by name, so hi team <command> <name>
// finds it from anywhere.
func teamDir(name string) string {
	link := filepath.Join(teamsDir(), name)
	if target, err := filepath.EvalSymlinks(link); err == nil {
		return target
	}
	return link
}

// teamRegister links a team's name to its folder.
func teamRegister(name, folder string) error {
	if err := os.MkdirAll(teamsDir(), 0o700); err != nil {
		return err
	}
	link := filepath.Join(teamsDir(), name)
	if info, err := os.Lstat(link); err == nil {
		if info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s is in the way: it should be a link to team %s's folder", link, name)
		}
		os.Remove(link)
	}
	return os.Symlink(folder, link)
}

func teamUnitName(name string) string { return "hi-team-" + name + ".service" }

// defaultTeamMembers is the roster the wizard offers: the reviewer is a
// different agent from the coder, so the code is checked by someone other
// than its author.
func defaultTeamMembers() map[string]teamMember {
	lead := readHermesModelConfig(filepath.Join(hermesHome(), "config.yaml")).Default
	return map[string]teamMember{
		"lead":     {Agent: "hermes", Model: lead},
		"coder":    {Agent: "claude", Model: "opus"},
		"reviewer": {Agent: "codex"},
	}
}

func validateTeam(config teamConfig) error {
	if !teamNamePattern.MatchString(config.Name) {
		return fmt.Errorf("a team's name starts with a letter and has lowercase letters, digits, and dashes, up to 20: %q", config.Name)
	}
	if !slackIDPattern.MatchString(config.Channel) {
		return fmt.Errorf("the channel is a Slack channel ID, such as C07ABC123, not %q", config.Channel)
	}
	if len(config.Owners) == 0 {
		return errors.New("a team needs at least one owner")
	}
	for _, id := range append(append([]string{}, config.Owners...), config.Askers...) {
		if !slackIDPattern.MatchString(id) {
			return fmt.Errorf("people are Slack member IDs, such as U07ABC123, not %q", id)
		}
	}
	if config.Members["lead"].Agent != "hermes" {
		return errors.New("the lead is Hermes")
	}
	for role, member := range config.Members {
		if !teamRolePattern.MatchString(role) {
			return fmt.Errorf("a role is a short lowercase name, such as coder, not %q", role)
		}
		if !agentKinds[member.Agent] {
			return fmt.Errorf("%s: the agent is claude, codex, or hermes, not %q", role, member.Agent)
		}
		if member.Model != "" && !agentModelName.MatchString(member.Model) {
			return fmt.Errorf("%s: %q isn't a model name", role, member.Model)
		}
		if _, ok := boxPresets[member.Network]; member.Network != "" && !ok {
			return fmt.Errorf("%s: the network is locked, dev, or open, not %q", role, member.Network)
		}
	}
	coder, hasCoder := config.Members["coder"]
	reviewer, hasReviewer := config.Members["reviewer"]
	if hasCoder && hasReviewer && coder.Agent == reviewer.Agent && coder.Model == reviewer.Model {
		return errors.New("the reviewer must be a different agent or model from the coder")
	}
	return nil
}

func loadTeam(name string) (teamConfig, error) {
	var config teamConfig
	if !teamNamePattern.MatchString(name) {
		return config, fmt.Errorf("no team named %q", name)
	}
	data, err := os.ReadFile(filepath.Join(teamDir(name), "team.json"))
	if errors.Is(err, os.ErrNotExist) {
		if target, linkErr := os.Readlink(filepath.Join(teamsDir(), name)); linkErr == nil {
			return config, fmt.Errorf("team %s's folder %s is gone; if you moved it, run hi team up <its new folder>", name, target)
		}
		return config, fmt.Errorf("no team named %s; hi team ls lists them", name)
	}
	if err != nil {
		return config, err
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("team.json: %w", err)
	}
	return config, nil
}

func saveTeam(config teamConfig) error {
	data, _ := json.MarshalIndent(config, "", "  ")
	return os.WriteFile(filepath.Join(teamDir(config.Name), "team.json"), append(data, '\n'), 0o600)
}

func loadTeamSecrets(name string) (teamSecrets, error) {
	var secrets teamSecrets
	data, err := os.ReadFile(filepath.Join(teamDir(name), "secrets.json"))
	if err != nil {
		return secrets, err
	}
	return secrets, json.Unmarshal(data, &secrets)
}

func saveTeamSecrets(name string, secrets teamSecrets) error {
	data, _ := json.MarshalIndent(secrets, "", "  ")
	return os.WriteFile(filepath.Join(teamDir(name), "secrets.json"), append(data, '\n'), 0o600)
}

// ---------------------------------------------------------------------------
// the command

func runTeam(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printTeamUsage(stdout)
		return 0
	}
	command, rest := args[0], args[1:]
	// The lead's own commands, inside its box.
	if teamLeadCommands[command] {
		return exitCode(runTeamLeadCommand(command, rest, stdin, stdout), stderr)
	}
	var err error
	switch command {
	case "new":
		err = teamNew(rest, stdin, stdout)
	case "up":
		err = withTeamName(rest, func(name string) error {
			// A folder instead of a name: a team that was moved, or copied
			// from another machine.
			if strings.ContainsRune(name, '/') || name == "." {
				var adoptErr error
				if name, adoptErr = teamAdopt(name, stdout); adoptErr != nil {
					return adoptErr
				}
			}
			return teamUp(name, stdout)
		})
	case "down":
		err = withTeamName(rest, func(name string) error { return teamDown(name, stdout) })
	case "ls", "list":
		err = teamList(stdout)
	case "status":
		err = withTeamName(rest, func(name string) error { return teamStatus(name, stdout) })
	case "logs":
		err = teamLogs(rest, stdout, stderr)
	case "manifest":
		err = withTeamName(rest, func(name string) error {
			config, err := loadTeam(name)
			if err == nil {
				fmt.Fprintln(stdout, teamManifest(config))
			}
			return err
		})
	case "__serve":
		err = withTeamName(rest, func(name string) error { return teamServe(name, stdout, stderr) })
	default:
		fmt.Fprintf(stderr, "hi: unknown team command %q\n\n", command)
		printTeamUsage(stderr)
		return 2
	}
	return exitCode(err, stderr)
}

func withTeamName(args []string, action func(string) error) error {
	if len(args) != 1 {
		return usageError{"name the team: hi team <command> <name>"}
	}
	return action(args[0])
}

func printTeamUsage(w io.Writer) {
	fmt.Fprintln(w, `hi team runs a virtual team of agents in one Slack channel: a lead
(Hermes) that talks with the people in plain language and keeps the
project's memory, and members it hands tasks to (a coder, a reviewer),
each in a box of its own.

usage:
  hi team new <name>               a step-by-step setup: Slack app, channel, people, members, code,
                                   in a new folder ./team-<name>
      [--dir <folder>]             the team's folder somewhere else
      [--from <file.json>]         the answers from a file; the Slack tokens from
                                   HI_TEAM_SLACK_BOT_TOKEN and HI_TEAM_SLACK_APP_TOKEN
  hi team up <name>                start the team and keep it up, also after a reboot
  hi team up <folder>              the same for a team whose folder moved or was copied here
  hi team down <name>              stop it; its folder stays
  hi team ls                       teams on this machine
  hi team status <name>            whether it is up, its members, and its tasks
  hi team logs <name> [<task>]     the lead's log, or a task's
  hi team manifest <name>          the Slack app manifest again

A team is its folder. hi finds it by name through `+teamsDir()+`.`)
}

// ---------------------------------------------------------------------------
// the wizard

// teamSlackChecker checks tokens and the channel with Slack; tests replace
// it.
type teamSlackChecker interface {
	botIdentity(botToken string) (workspace, botUser string, err error)
	checkAppToken(appToken string) error
	channel(botToken, channel string) (name string, member bool, err error)
}

var teamSlack teamSlackChecker = liveTeamSlack{}

type liveTeamSlack struct{}

func (liveTeamSlack) botIdentity(botToken string) (string, string, error) {
	response, err := slack.New(botToken).AuthTest()
	if err != nil {
		return "", "", err
	}
	return response.Team, response.UserID, nil
}

func (liveTeamSlack) checkAppToken(appToken string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _, err := slack.New("", slack.OptionAppLevelToken(appToken)).StartSocketModeContext(ctx)
	return err
}

func (liveTeamSlack) channel(botToken, channel string) (string, bool, error) {
	info, err := slack.New(botToken).GetConversationInfo(&slack.GetConversationInfoInput{ChannelID: channel})
	if err != nil {
		return "", false, err
	}
	return info.Name, info.IsMember, nil
}

// teamAnswers is what --from reads: team.json's fields, and a repository
// to clone instead of a new one.
type teamAnswers struct {
	teamConfig
	Clone string `json:"clone,omitempty"`
}

func teamNew(args []string, stdin io.Reader, stdout io.Writer) error {
	var name, from, folder string
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--from" && i+1 < len(args):
			i++
			from = args[i]
		case strings.HasPrefix(args[i], "--from="):
			from = strings.TrimPrefix(args[i], "--from=")
		case args[i] == "--dir" && i+1 < len(args):
			i++
			folder = args[i]
		case strings.HasPrefix(args[i], "--dir="):
			folder = strings.TrimPrefix(args[i], "--dir=")
		case strings.HasPrefix(args[i], "-"):
			return usageError{"unknown option " + args[i]}
		case name == "":
			name = args[i]
		default:
			return usageError{"usage: hi team new <name> [--dir <folder>] [--from <file.json>]"}
		}
	}
	if name == "" {
		return usageError{"usage: hi team new <name> [--dir <folder>] [--from <file.json>]"}
	}
	if !teamNamePattern.MatchString(name) {
		return fmt.Errorf("a team's name starts with a letter and has lowercase letters, digits, and dashes, up to 20: %q", name)
	}
	if _, err := loadTeam(name); err == nil {
		return fmt.Errorf("team %s already exists in %s", name, teamDir(name))
	}
	dir, err := filepath.Abs(firstNonEmpty(folder, "team-"+name))
	if err != nil {
		return err
	}
	if fileExists(dir) {
		return fmt.Errorf("%s already exists; choose another folder with --dir", dir)
	}
	if err := hermesInstalled(); err != nil {
		return err
	}

	var answers teamAnswers
	var secrets teamSecrets
	if from != "" {
		answers, secrets, err = teamAnswersFromFile(name, from)
	} else {
		answers, secrets, err = teamWizard(name, dir, stdin, stdout)
	}
	if err != nil {
		return err
	}
	config := answers.teamConfig
	config.Name, config.Created = name, time.Now().UTC()
	if err := validateTeam(config); err != nil {
		return err
	}
	if err := teamCheckSlack(config, secrets, stdout); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := teamRegister(name, dir); err != nil {
		os.RemoveAll(dir)
		return err
	}
	if err := createTeamFolder(&config, secrets, answers.Clone, stdout); err != nil {
		os.RemoveAll(dir)
		os.Remove(filepath.Join(teamsDir(), name))
		return err
	}
	fmt.Fprintf(stdout, "\nTeam %s is ready in %s.\n", name, dir)
	for _, role := range teamRoles(config) {
		member := config.Members[role]
		fmt.Fprintf(stdout, "  %-10s %s\n", role, teamMemberLabel(member))
	}
	fmt.Fprintf(stdout, "\nStart it with:\n  hi team up %s\n", name)
	return nil
}

func teamAnswersFromFile(name, path string) (teamAnswers, teamSecrets, error) {
	var answers teamAnswers
	data, err := os.ReadFile(path)
	if err != nil {
		return answers, teamSecrets{}, err
	}
	if err := json.Unmarshal(data, &answers); err != nil {
		return answers, teamSecrets{}, fmt.Errorf("%s: %w", path, err)
	}
	if answers.Members == nil {
		answers.Members = defaultTeamMembers()
	}
	secrets := teamSecrets{SlackBotToken: os.Getenv("HI_TEAM_SLACK_BOT_TOKEN"), SlackAppToken: os.Getenv("HI_TEAM_SLACK_APP_TOKEN")}
	if secrets.SlackBotToken == "" || secrets.SlackAppToken == "" {
		return answers, secrets, errors.New("set HI_TEAM_SLACK_BOT_TOKEN and HI_TEAM_SLACK_APP_TOKEN to the Slack app's tokens")
	}
	return answers, secrets, nil
}

func teamWizard(name, dir string, stdin io.Reader, stdout io.Writer) (teamAnswers, teamSecrets, error) {
	var answers teamAnswers
	var secrets teamSecrets
	config := &answers.teamConfig
	config.Name = name
	var err error

	fmt.Fprintf(stdout, "Setting up team %s. Each step can be changed later in %s.\n\n", name, filepath.Join(dir, "team.json"))

	fmt.Fprintln(stdout, "1/6 Purpose")
	fmt.Fprintln(stdout, "In one sentence, what is this team for? The lead starts from it.")
	if config.Purpose, err = teamAsk(stdin, stdout, "Purpose", ""); err != nil {
		return answers, secrets, err
	}

	fmt.Fprintln(stdout, "\n2/6 Slack app")
	fmt.Fprintln(stdout, "Each team has a Slack app of its own. Create it from this manifest:")
	fmt.Fprintln(stdout, "  api.slack.com/apps → Create New App → From a manifest → paste it → Create,")
	fmt.Fprintln(stdout, "  then Install to Workspace.")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, teamManifest(*config))
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "The bot token is under OAuth & Permissions (xoxb-…). The app token is under")
	fmt.Fprintln(stdout, "Basic Information → App-Level Tokens: generate one with the connections:write scope (xapp-…).")
	if secrets.SlackBotToken, err = teamAskSecret(stdin, stdout, "Bot token"); err != nil {
		return answers, secrets, err
	}
	if secrets.SlackAppToken, err = teamAskSecret(stdin, stdout, "App token"); err != nil {
		return answers, secrets, err
	}

	fmt.Fprintln(stdout, "\n3/6 Channel")
	fmt.Fprintln(stdout, "The team's channel ID: open the channel, click its name, and copy the ID at the bottom (C…).")
	fmt.Fprintln(stdout, "Invite the app to it first: /invite @"+teamBotName(name))
	if config.Channel, err = teamAsk(stdin, stdout, "Channel ID", ""); err != nil {
		return answers, secrets, err
	}

	fmt.Fprintln(stdout, "\n4/6 People")
	fmt.Fprintln(stdout, "Owners approve what matters, such as merging and stopping the team. Give Slack member IDs,")
	fmt.Fprintln(stdout, "separated by commas: a person's profile → ⋯ → Copy member ID (U…).")
	owners, err := teamAsk(stdin, stdout, "Owners", "")
	if err != nil {
		return answers, secrets, err
	}
	config.Owners = splitList(owners)
	fmt.Fprintln(stdout, "Who else may ask the team for work? Enter lets everyone in the channel.")
	askers, err := teamAsk(stdin, stdout, "Others", "everyone in the channel")
	if err != nil {
		return answers, secrets, err
	}
	if askers != "everyone in the channel" {
		config.Askers = splitList(askers)
	}

	fmt.Fprintln(stdout, "\n5/6 Members")
	config.Members = defaultTeamMembers()
	for _, role := range teamRoles(*config) {
		member := config.Members[role]
		answer, err := teamAsk(stdin, stdout, fmt.Sprintf("%-8s (agent:model)", role), member.Agent+teamModelSuffix(member.Model))
		if err != nil {
			return answers, secrets, err
		}
		agent, model, _ := strings.Cut(answer, ":")
		member.Agent, member.Model = strings.TrimSpace(agent), strings.TrimSpace(model)
		config.Members[role] = member
	}

	fmt.Fprintln(stdout, "\n6/6 Code")
	fmt.Fprintln(stdout, "Enter starts a new git repository. Or give a repository to clone with this machine's git credentials.")
	clone, err := teamAsk(stdin, stdout, "Repository", "new")
	if err != nil {
		return answers, secrets, err
	}
	if clone != "new" {
		answers.Clone = clone
	}
	return answers, secrets, nil
}

func teamModelSuffix(model string) string {
	if model == "" {
		return ""
	}
	return ":" + model
}

func teamMemberLabel(member teamMember) string {
	label := agentDisplayName(member.Agent)
	if member.Model != "" {
		label += ", " + member.Model
	}
	if member.Bundle != "" {
		label += ", bundle " + member.Bundle
	}
	return label
}

// teamRoles lists the lead first, then the others by name.
func teamRoles(config teamConfig) []string {
	roles := []string{}
	for role := range config.Members {
		if role != "lead" {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	if _, ok := config.Members["lead"]; ok {
		roles = append([]string{"lead"}, roles...)
	}
	return roles
}

func splitList(text string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' }) {
		out = append(out, part)
	}
	return out
}

// teamAsk asks one question; Enter takes the fallback.
func teamAsk(stdin io.Reader, stdout io.Writer, question, fallback string) (string, error) {
	if fallback != "" {
		fmt.Fprintf(stdout, "%s [%s]: ", question, fallback)
	} else {
		fmt.Fprintf(stdout, "%s: ", question)
	}
	line, err := readLine(stdin)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if !isTerminal(stdin) {
		fmt.Fprintln(stdout)
	}
	if line = strings.TrimSpace(line); line != "" {
		return line, nil
	}
	if fallback != "" {
		return fallback, nil
	}
	if errors.Is(err, io.EOF) {
		return "", fmt.Errorf("no answer for %s", strings.ToLower(question))
	}
	return teamAsk(stdin, stdout, question, fallback)
}

// teamAskSecret reads a token without showing it.
func teamAskSecret(stdin io.Reader, stdout io.Writer, label string) (string, error) {
	fmt.Fprintf(stdout, "%s (paste it; it shows as *): ", label)
	var value string
	if file, ok := stdin.(*os.File); ok && isTerminal(stdin) {
		secret, err := readSecret(file, stdout)
		fmt.Fprintln(stdout)
		if err != nil {
			return "", err
		}
		value = string(secret)
	} else {
		line, err := readLine(stdin)
		fmt.Fprintln(stdout)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		value = line
	}
	if value = strings.TrimSpace(value); value == "" {
		return "", fmt.Errorf("no %s was entered", strings.ToLower(label))
	}
	return value, nil
}

// teamCheckSlack checks both tokens and that the bot is in the channel.
func teamCheckSlack(config teamConfig, secrets teamSecrets, stdout io.Writer) error {
	if !strings.HasPrefix(secrets.SlackBotToken, "xoxb-") {
		return errors.New("the bot token starts with xoxb-; it is under OAuth & Permissions")
	}
	if !strings.HasPrefix(secrets.SlackAppToken, "xapp-") {
		return errors.New("the app token starts with xapp-; generate one under Basic Information → App-Level Tokens")
	}
	workspace, _, err := teamSlack.botIdentity(secrets.SlackBotToken)
	if err != nil {
		return fmt.Errorf("Slack refused the bot token: %w", err)
	}
	if err := teamSlack.checkAppToken(secrets.SlackAppToken); err != nil {
		return fmt.Errorf("Slack refused the app token (it needs connections:write, and Socket Mode on): %w", err)
	}
	channel, member, err := teamSlack.channel(secrets.SlackBotToken, config.Channel)
	if err != nil {
		return fmt.Errorf("the bot can't see channel %s (invite it with /invite @%s): %w", config.Channel, teamBotName(config.Name), err)
	}
	if !member {
		return fmt.Errorf("the bot isn't in #%s yet; invite it there with /invite @%s", channel, teamBotName(config.Name))
	}
	fmt.Fprintf(stdout, "Slack: workspace %s, channel #%s.\n", workspace, channel)
	return nil
}

// createTeamFolder makes the folder, the repository, and the lead's first
// files.
func createTeamFolder(config *teamConfig, secrets teamSecrets, clone string, stdout io.Writer) error {
	dir := teamDir(config.Name)
	for _, sub := range []string{"lead", "specs", "tasks", "run", "logs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o700); err != nil {
			return err
		}
	}
	if err := saveTeamSecrets(config.Name, secrets); err != nil {
		return err
	}
	// In case the folder is inside a repository or a synced folder.
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("secrets.json\nrun/\n"), 0o600); err != nil {
		return err
	}
	repo := filepath.Join(dir, "repo")
	if clone != "" {
		fmt.Fprintf(stdout, "Cloning %s…\n", clone)
		if out, err := exec.Command("git", "clone", "-q", clone, repo).CombinedOutput(); err != nil {
			return fmt.Errorf("git clone: %s", qFirstLine(string(out), err.Error()))
		}
		config.Remote = clone
		if boxGit(repo, "rev-parse", "--verify", "-q", "HEAD") == "" {
			return errors.New("the repository has no commits yet; push a first commit, or let the team start a new one")
		}
	} else if err := newTeamRepo(*config, repo); err != nil {
		return err
	}
	config.Branch = firstNonEmpty(boxGit(repo, "branch", "--show-current"), "main")
	if err := saveTeam(*config); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "slack-manifest.json"), []byte(teamManifest(*config)+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "lead", "SOUL.md"), []byte(teamSoul(*config)), 0o600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "specs", "README.md"), []byte(teamSpecsReadme(*config)), 0o600)
}

func newTeamRepo(config teamConfig, repo string) error {
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return err
	}
	files := map[string]string{
		"README.md": "# " + config.Name + "\n\n" + config.Purpose + "\n",
		"AGENTS.md": teamAgentsFile,
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(repo, name), []byte(content), 0o644); err != nil {
			return err
		}
	}
	author := []string{"-c", "user.name=" + firstNonEmpty(boxGit(".", "config", "user.name"), "hi team "+config.Name),
		"-c", "user.email=" + firstNonEmpty(boxGit(".", "config", "user.email"), "team-"+config.Name+"@hi.invalid")}
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"}, append(author, "commit", "-q", "-m", "Start the "+config.Name+" team's project")} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %s", args[0], qFirstLine(string(out), err.Error()))
		}
	}
	return nil
}

const teamAgentsFile = `# Working on this project

This project is built by a hi team: a lead that talks with the people who
use it, and agents that each do one task at a time. The people may not read
code, so the code, its tests, and its docs must explain themselves.

- Work only on the task in your brief. If something else needs doing, say
  so in your report's follow-ups.
- Every change comes with tests for what the brief says must work. Run them
  before you finish, and say in your report whether they pass.
- Commit your work on your branch with a message that says what changed and
  why. Never push, and never change the main branch yourself: the lead
  merges after a review.
- Keep README.md current: what the project does, how to run it, how to
  test it.
`

// teamBotName is the bot's handle in Slack.
func teamBotName(name string) string { return "team-" + name }

// ---------------------------------------------------------------------------
// up, down, ls, status, logs

func teamUnit(config teamConfig, executable string) string {
	env := ""
	if dir := os.Getenv("HI_TEAMS_DIR"); dir != "" {
		env += "Environment=HI_TEAMS_DIR=" + dir + "\n"
	}
	// The agents, git, and podman are found as in the shell that ran hi team up.
	env += "Environment=PATH=" + os.Getenv("PATH") + "\n"
	return fmt.Sprintf(`[Unit]
Description=hi team %[1]s
After=network-online.target
Wants=network-online.target

[Service]
%[2]sExecStart=%[3]s team __serve %[1]s
Restart=always
RestartSec=10
TimeoutStopSec=60

[Install]
WantedBy=default.target
`, config.Name, env, executable)
}

// teamSystemctl runs systemctl --user; tests replace it.
var teamSystemctl = func(args ...string) (string, error) {
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func teamUnitPath(name string) string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", teamUnitName(name))
}

func teamUp(name string, stdout io.Writer) error {
	config, err := loadTeam(name)
	if err != nil {
		return err
	}
	if err := hermesInstalled(); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	unit := teamUnitPath(name)
	if err := os.MkdirAll(filepath.Dir(unit), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(unit, []byte(teamUnit(config, executable)), 0o644); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", teamUnitName(name)}, {"restart", teamUnitName(name)}} {
		if out, err := teamSystemctl(args...); err != nil {
			return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), qFirstLine(out, err.Error()))
		}
	}
	fmt.Fprintf(stdout, "Team %s is starting. It answers in its Slack channel in a minute or so.\n", name)
	fmt.Fprintf(stdout, "  hi team status %s   whether it is up\n  hi team logs %s     the lead's log\n", name, name)
	if !teamLingers() {
		fmt.Fprintf(stdout, "\nIt stops when you log out, and doesn't start at boot, until you run:\n  sudo loginctl enable-linger %s\n", currentUserName())
	}
	return nil
}

// teamLingers reports whether this user's services run without a login.
var teamLingers = func() bool {
	out, err := exec.Command("loginctl", "show-user", currentUserName(), "--property=Linger", "--value").Output()
	return err == nil && strings.TrimSpace(string(out)) == "yes"
}

// teamAdopt registers the team in a folder under its name: a team that
// was moved, or copied from another machine.
func teamAdopt(folder string, stdout io.Writer) (string, error) {
	dir, err := filepath.Abs(folder)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(filepath.Join(dir, "team.json"))
	if err != nil {
		return "", fmt.Errorf("%s isn't a team's folder: %w", dir, err)
	}
	var config teamConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return "", fmt.Errorf("team.json: %w", err)
	}
	if !teamNamePattern.MatchString(config.Name) {
		return "", fmt.Errorf("team.json has no valid name: %q", config.Name)
	}
	if current := teamDir(config.Name); current != dir && fileExists(filepath.Join(current, "team.json")) {
		return "", fmt.Errorf("another team named %s is in %s", config.Name, current)
	}
	if err := teamRegister(config.Name, dir); err != nil {
		return "", err
	}
	fmt.Fprintf(stdout, "Team %s is in %s.\n", config.Name, dir)
	return config.Name, nil
}

func teamDown(name string, stdout io.Writer) error {
	if _, err := loadTeam(name); err != nil {
		return err
	}
	if out, err := teamSystemctl("disable", "--now", teamUnitName(name)); err != nil && fileExists(teamUnitPath(name)) {
		return fmt.Errorf("systemctl --user disable --now %s: %s", teamUnitName(name), qFirstLine(out, err.Error()))
	}
	fmt.Fprintf(stdout, "Team %s is stopped. Its folder stays; hi team up %s starts it again.\n", name, name)
	return nil
}

func teamState(name string) string {
	out, _ := teamSystemctl("is-active", teamUnitName(name))
	if out == "" {
		return "down"
	}
	if out == "active" {
		return "up"
	}
	if out == "inactive" {
		return "down"
	}
	return out
}

func teamList(stdout io.Writer) error {
	entries, err := os.ReadDir(teamsDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	table := tabwriter.NewWriter(stdout, 0, 0, 3, ' ', 0)
	count := 0
	for _, entry := range entries {
		config, err := loadTeam(entry.Name())
		if err != nil {
			fmt.Fprintf(table, "%s\t%s\n", entry.Name(), err)
			continue
		}
		if count == 0 {
			fmt.Fprintln(table, "TEAM\tSTATE\tCHANNEL\tMEMBERS\tFOLDER")
		}
		count++
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", config.Name, teamState(config.Name), config.Channel, strings.Join(teamRoles(config), ", "), teamDir(config.Name))
	}
	table.Flush()
	if count == 0 {
		fmt.Fprintln(stdout, "No teams yet. hi team new <name> sets one up.")
	}
	return nil
}

func teamStatus(name string, stdout io.Writer) error {
	config, err := loadTeam(name)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Team %s: %s. Channel %s. %s\n\n", name, teamState(name), config.Channel, config.Purpose)
	for _, role := range teamRoles(config) {
		fmt.Fprintf(stdout, "  %-10s %s\n", role, teamMemberLabel(config.Members[role]))
	}
	tasks := loadTeamTasks(name)
	if len(tasks) == 0 {
		fmt.Fprintln(stdout, "\nNo tasks yet.")
		return nil
	}
	fmt.Fprintln(stdout)
	printTeamTasks(tasks, 15, stdout)
	return nil
}

func teamLogs(args []string, stdout, stderr io.Writer) error {
	if len(args) < 1 || len(args) > 2 {
		return usageError{"usage: hi team logs <name> [<task>]"}
	}
	if _, err := loadTeam(args[0]); err != nil {
		return err
	}
	if len(args) == 2 {
		task, err := loadTeamTask(args[0], args[1])
		if err != nil {
			return err
		}
		engine, _, err := detectBoxEngine()
		if err != nil {
			return err
		}
		return engine.interactive(nil, stdout, stderr, "logs", "hi-box-"+task.Box)
	}
	command := exec.Command("journalctl", "--user", "-u", teamUnitName(args[0]), "-n", "200", "--no-pager", "-o", "cat")
	command.Stdout, command.Stderr = stdout, stderr
	return command.Run()
}
