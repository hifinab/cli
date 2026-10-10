package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// hi team __serve runs one team in the foreground, under systemd: the
// lead's box with Hermes' Slack gateway, its proxy, and the broker. When
// the lead stops, so does serve, and systemd starts it again.

// teamLeadHosts is the lead's allowlist: any site, so it can read the
// links people give it. The box is still its boundary: no credentials, and
// its model calls go through the proxy's token listener.
var teamLeadHosts = []string{"*"}

func teamServe(name string, stdout, stderr io.Writer) error {
	config, err := loadTeam(name)
	if err != nil {
		return err
	}
	secrets, err := loadTeamSecrets(name)
	if err != nil {
		return fmt.Errorf("the team's Slack tokens: %w", err)
	}
	if err := hermesInstalled(); err != nil {
		return err
	}
	engine, warning, err := detectBoxEngine()
	if err != nil {
		return err
	}
	if warning != "" {
		fmt.Fprintln(stderr, "hi: "+warning)
	}
	hi, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(hi); err == nil {
		hi = resolved
	}
	dir := teamDir(name)
	proxyDir, brokerDir, home := filepath.Join(dir, "run", "proxy"), teamBrokerDir(name), filepath.Join(dir, "run", "home")
	for _, sub := range []string{proxyDir, brokerDir, home, filepath.Join(dir, "lead"), filepath.Join(dir, "specs"), teamTaskDir(name)} {
		if err := os.MkdirAll(sub, 0o700); err != nil {
			return err
		}
	}

	meta := boxMeta{Name: "team-" + name, Agent: "hermes", Engine: engine.name, Network: "team", Created: time.Now().UTC()}
	container := "hi-box-" + meta.Name
	// Whatever a crash left behind goes first.
	teardownBox(engine, meta)
	if err := os.WriteFile(filepath.Join(proxyDir, "allow"), []byte(strings.Join(teamLeadHosts, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if meta.Image, err = engine.ensureBaseImage(stdout, stderr); err != nil {
		return err
	}
	if err := startBoxNetwork(engine, &meta, proxyDir, "hermes", nil); err != nil {
		teardownBox(engine, meta)
		return err
	}
	defer teardownBox(engine, meta)
	if err := writeTeamLead(config, secrets, meta.ProxyIP); err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	broker := newTeamBroker(name, hi)
	serveBroker, err := broker.listen(ctx, filepath.Join(brokerDir, teamSocketName))
	if err != nil {
		return fmt.Errorf("the broker's socket: %w", err)
	}
	brokerErr := make(chan error, 1)
	go func() { brokerErr <- serveBroker() }()
	broker.resume()

	run := []string{"run", "--rm", "--init", "--name", container, "--hostname", name + "-lead", "--network", "hi-box-" + meta.Name, "--dns", "127.0.0.1"}
	run = append(run, engine.userArgs()...)
	run = append(run, boxHardening("4g")...)
	run = append(run,
		"-v", home+":/box/home",
		"-v", filepath.Join(dir, "lead")+":/box/home/.hermes",
		"-v", filepath.Join(dir, "specs")+":/team/specs",
		"-v", filepath.Join(dir, "repo")+":/team/repo:ro",
		"-v", brokerDir+":/team/run",
		"-v", hi+":/usr/local/bin/hi:ro",
		"-w", "/team")
	hermesMounts(&run)
	proxy := fmt.Sprintf("http://%s:%d", meta.ProxyIP, boxProxyPort)
	env := map[string]string{
		"HTTPS_PROXY": proxy, "HTTP_PROXY": proxy, "https_proxy": proxy, "http_proxy": proxy,
		"NO_PROXY": "localhost,127.0.0.1," + meta.ProxyIP, "no_proxy": "localhost,127.0.0.1," + meta.ProxyIP,
		"HOME": "/box/home", "HERMES_HOME": "/box/home/.hermes", "HI_BOX": meta.Name, "HI_TEAM": name,
		teamSocketEnv: "/team/run/" + teamSocketName,
		// The box is the boundary, so Hermes doesn't ask before commands,
		// doesn't install into its home, and keeps its locks in the box.
		"HERMES_YOLO_MODE": "1", "HERMES_DISABLE_LAZY_INSTALLS": "1", "PYTHONDONTWRITEBYTECODE": "1",
		"HERMES_GATEWAY_LOCK_DIR": "/box/home/.locks",
		"PATH":                    "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		run = append(run, "-e", key+"="+env[key])
	}
	install := hermesInstallDir()
	run = append(run, meta.Image, filepath.Join(install, "venv", "bin", "python"), filepath.Join(install, "hermes"), "gateway", "run", "--accept-hooks")

	// systemd stops the team with SIGTERM; the lead gets time to finish
	// what it is saying.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	defer signal.Stop(signals)
	var stopped atomic.Bool
	go func() {
		if _, ok := <-signals; ok {
			stopped.Store(true)
			engine.output("stop", "-t", "20", container)
		}
	}()
	fmt.Fprintf(stdout, "Team %s: the lead is starting in %s, model %s.\n", name, container, firstNonEmpty(config.Members["lead"].Model, "Hermes' default"))
	err = engine.interactive(nil, stdout, stderr, run...)
	cancel()
	if stopped.Load() {
		fmt.Fprintf(stdout, "Team %s stopped.\n", name)
		return nil
	}
	select {
	case brokerFailure := <-brokerErr:
		if brokerFailure != nil {
			return fmt.Errorf("the broker: %w", brokerFailure)
		}
	default:
	}
	if err == nil {
		err = errors.New("the lead stopped")
	}
	return fmt.Errorf("team %s: %w; systemd starts it again", name, err)
}

// teamBrokerDir holds the broker's socket. A socket's path must be short,
// so it lives in the user's runtime folder when there is one.
func teamBrokerDir(name string) string {
	if runtime := os.Getenv("XDG_RUNTIME_DIR"); runtime != "" && fileExists(runtime) {
		return filepath.Join(runtime, "hi-team-"+name)
	}
	return filepath.Join(teamDir(name), "run", "broker")
}

// hermesMounts mounts the host's Hermes and its Python read-only, at the
// same paths.
func hermesMounts(run *[]string) {
	install := hermesInstallDir()
	*run = append(*run, "-v", install+":"+install+":ro")
	// The venv's python is a link to a Python uv installed in the home
	// folder; that comes along read-only at the same path.
	if python, err := filepath.EvalSymlinks(filepath.Join(install, "venv", "bin", "python")); err == nil {
		home, _ := os.UserHomeDir()
		if root := filepath.Dir(filepath.Dir(filepath.Dir(python))); strings.HasPrefix(root, home+"/") {
			*run = append(*run, "-v", root+":"+root+":ro")
		}
	}
}

// writeTeamLead writes what hi owns in the lead's Hermes home on every
// start: its settings, its .env, and the hi-team skill. SOUL.md and the
// memories are the lead's own and are left alone.
func writeTeamLead(config teamConfig, secrets teamSecrets, proxyIP string) error {
	lead := filepath.Join(teamDir(config.Name), "lead")
	host := readHermesModelConfig(filepath.Join(hermesHome(), "config.yaml"))
	model := firstNonEmpty(config.Members["lead"].Model, host.Default)
	settings := "model:\n"
	if model != "" {
		settings += "  default: " + model + "\n"
	}
	settings += fmt.Sprintf("  provider: custom\n  base_url: http://%s:%d/api/v1\n  api_key: %s\n  api_mode: %s\n", proxyIP, boxInjectPort, boxClaudePlacehold, firstNonEmpty(host.APIMode, "chat_completions"))
	settings += "terminal:\n  backend: local\n  cwd: /team\n"
	settings += "approvals:\n  mode: \"off\"\n"
	settings += "updates:\n  check: false\n"
	settings += "plugins:\n  auto_update_check_hours: 0\n"
	if err := os.WriteFile(filepath.Join(lead, "config.yaml"), []byte(settings), 0o600); err != nil {
		return err
	}
	env := []string{
		"OPENROUTER_API_KEY=" + boxClaudePlacehold,
		"SLACK_BOT_TOKEN=" + secrets.SlackBotToken,
		"SLACK_APP_TOKEN=" + secrets.SlackAppToken,
		// One channel, every message in it, and no direct messages.
		"SLACK_ALLOWED_CHANNELS=" + config.Channel,
		"SLACK_HOME_CHANNEL=" + config.Channel,
		"SLACK_DISABLE_DMS=true",
		"SLACK_REQUIRE_MENTION=false",
	}
	if len(config.Askers) == 0 {
		env = append(env, "SLACK_ALLOW_ALL_USERS=true")
	} else {
		env = append(env, "SLACK_ALLOWED_USERS="+strings.Join(append(append([]string{}, config.Owners...), config.Askers...), ","))
	}
	if err := os.WriteFile(filepath.Join(lead, ".env"), []byte(strings.Join(env, "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if !fileExists(filepath.Join(lead, "SOUL.md")) {
		if err := os.WriteFile(filepath.Join(lead, "SOUL.md"), []byte(teamSoul(config)), 0o600); err != nil {
			return err
		}
	}
	skill := filepath.Join(lead, "skills", "hi-team")
	if err := os.MkdirAll(skill, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(teamSkill(config)), 0o600)
}

// teamManifest is the team's Slack app: Socket Mode, one channel, no
// direct messages or assistant view.
func teamManifest(config teamConfig) string {
	description := firstNonEmpty(config.Purpose, "A hi team")
	if len(description) > 139 {
		description = description[:136] + "..."
	}
	manifest := map[string]any{
		"display_information": map[string]any{"name": teamBotName(config.Name), "description": description, "background_color": "#1a1a2e"},
		"features": map[string]any{
			"bot_user": map[string]any{"display_name": teamBotName(config.Name), "always_online": true},
		},
		"oauth_config": map[string]any{"scopes": map[string]any{"bot": []string{
			"app_mentions:read", "channels:history", "channels:read", "chat:write", "files:read", "files:write",
			"groups:history", "groups:read", "reactions:read", "reactions:write", "users:read",
		}}},
		"settings": map[string]any{
			"event_subscriptions":    map[string]any{"bot_events": []string{"app_mention", "message.channels", "message.groups", "reaction_added", "reaction_removed"}},
			"interactivity":          map[string]any{"is_enabled": true},
			"org_deploy_enabled":     false,
			"socket_mode_enabled":    true,
			"token_rotation_enabled": false,
		},
	}
	data, _ := json.MarshalIndent(manifest, "", "  ")
	return string(data)
}

func teamPeople(ids []string) string {
	var mentions []string
	for _, id := range ids {
		mentions = append(mentions, "<@"+id+">")
	}
	return strings.Join(mentions, ", ")
}

// teamSoul is the lead's identity, written once; owners may edit it.
func teamSoul(config teamConfig) string {
	var roles []string
	for _, role := range teamRoles(config)[1:] {
		roles = append(roles, fmt.Sprintf("- the %s: %s", role, teamMemberLabel(config.Members[role])))
	}
	return fmt.Sprintf(`# The %[1]s team's lead

You lead the %[1]s team, a virtual team of agents that lives in one Slack
channel and builds one project for the people in it.

The project: %[2]s

The team's owners are %[3]s. They decide what matters most, and only they
may ask you to stop, undo, or change how the team works.

## The people

The people in the channel may not read code. They may be experts in their
own field who want something that works. You bridge the gap:

- Talk with them in plain language. No code, file names, or jargon unless
  they ask for it.
- When you need a decision, ask it as a choice about what the project
  should do, never as a question about code.
- Keep them posted in short messages: what you're doing, what's next, and
  when to try something.

## Your team

You don't write code yourself. You hand tasks to the members:

%[4]s

The hi-team skill says how: hi team task, review, wait, and merge.

## How work is done

1. Agree with the people what will change and how they will know it works:
   a short list of things they can try. That is the acceptance list.
2. Write the feature's spec in /team/specs/<feature>.md: the acceptance
   list in their words, then the technical breakdown for the team.
3. Give the coder a brief with everything it needs: the goal, the
   acceptance list, the files or areas, and the tests that must pass.
4. Have the reviewer check the work. If it needs changes, give the coder a
   new task on the same branch (--on) with the reviewer's notes, then have
   it reviewed again.
5. Merge only after the reviewer approves.
6. Tell the people what changed and how to try it, and ask them to
   confirm it works. A feature is done when they agree it does. A bug
   they report becomes a new task for that feature.

Small fixes, such as a typo or a wrong label, needn't wait for the people
to agree first: fix, review, merge, and tell them what changed.

## What you keep

You hold the project's memory. Keep /team/specs current: one file per
feature, with its status. /team/repo is the project as merged, read-only
for you: read it to answer questions about how things work.
`, config.Name, config.Purpose, teamPeople(config.Owners), strings.Join(roles, "\n"))
}

// teamSkill teaches the lead the broker's commands. hi rewrites it on
// every start, so it matches the hi that runs the team.
func teamSkill(config teamConfig) string {
	var roles []string
	for _, role := range teamRoles(config)[1:] {
		roles = append(roles, role)
	}
	return fmt.Sprintf(`---
name: hi-team
description: Hand tasks to the %[1]s team's members (%[2]s), get their work reviewed, and merge it, with the hi team command.
metadata:
  hermes:
    requires_toolsets: [terminal]
---

# Working with the team

Run these with the terminal tool. Members work on the host, each in a box
of its own on a branch of the project; you get their reports.

| Command | What it does |
|---|---|
| hi team task <role> <brief.md> | Start a task for a member; prints its ID, such as t-4 |
| hi team task coder <brief.md> --on t-4 | Continue t-4 on its branch, for example with the reviewer's notes |
| hi team review t-4 [<notes.md>] | Have the reviewer check t-4's work; prints the review's ID |
| hi team wait t-5 | Wait for a task or review to finish and print its report |
| hi team show t-5 | Print a task's state and report without waiting |
| hi team tasks | List the tasks |
| hi team merge t-4 | Merge t-4 into the project once a review approved its latest commit |
| hi team stop t-5 | Stop a task that went wrong |

Members: %[2]s.

## Briefs

Write each brief to a file first, for example /team/specs/briefs/t-next.md,
then pass its path. A brief starts with a one-line title, then says:

- the goal, in one or two sentences;
- the acceptance list, and the tests that must cover it;
- where in the project the work goes, if you know;
- what not to change.

The member sees only the brief and the project, not this conversation.

## Waiting

A task takes minutes to an hour. Start hi team wait <id> with
background=true and notify=true, tell the channel what's happening, and
carry on; you are told when it finishes. If it prints "Still working", wait
again.

## Reviews and merging

A review's report starts with Approve or Changes needed. hi team merge
refuses work that no review approved, and work changed since its review.
After a merge, tell the people what changed. If a merge doesn't apply
cleanly, give the coder a new task to redo the work on the latest project.
`, config.Name, strings.Join(roles, ", "))
}

func teamSpecsReadme(config teamConfig) string {
	return fmt.Sprintf(`# %s: specs

The lead keeps one file per feature here: what was agreed with the people,
in their words, with the list of things they can try to see it works, and
the technical breakdown for the team. Each file says the feature's status.

The project: %s
`, config.Name, config.Purpose)
}
