package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// hi box runs a shell or a command, or for hi agent a coding agent, in a
// rootless container with the project and nothing else from the home
// folder, and a network whose only way out is hi's proxy. See
// docs/specs/approved/hi_box.md and hi_agent.md.

// boxPresets are the hosts each network preset allows. An agent's own hosts
// come from hi agent (agentHosts), not from a preset.
var boxPresets = map[string][]string{
	"locked": {},
	"dev": {
		"github.com", "codeload.github.com", "objects.githubusercontent.com", "raw.githubusercontent.com",
		"release-assets.githubusercontent.com", "api.github.com",
		"pypi.org", "files.pythonhosted.org", "download.pytorch.org",
		"registry.npmjs.org", "registry.yarnpkg.com",
		"proxy.golang.org", "sum.golang.org", "storage.googleapis.com",
		"crates.io", "static.crates.io", "index.crates.io",
		"huggingface.co", "cdn-lfs.huggingface.co", "cas-bridge.xethub.hf.co",
		"skills.sh", "www.skills.sh",
	},
	"open": {"*"},
}

// boxPresetHosts is a preset's allowlist.
func boxPresetHosts(network string) []string {
	return append([]string{}, boxPresets[network]...)
}

// boxRiskyFiles run on the host later, so diff flags changes to them.
var boxRiskyFiles = regexp.MustCompile(`(^|/)(Makefile|GNUmakefile|Justfile|justfile|package\.json|\.envrc|\.vscode/(tasks|launch|settings)\.json|\.github/workflows/[^/]+|\.gitlab-ci\.yml|\.pre-commit-config\.yaml|\.husky/[^/]+|setup\.py|pyproject\.toml|\.git/hooks/[^/]+|\.devcontainer/[^/]+|Dockerfile)$`)

var boxNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// boxMeta is box.json in the box's state folder.
type boxMeta struct {
	Name       string    `json:"name"`
	Agent      string    `json:"agent"` // claude, codex, shell, or run
	Root       string    `json:"root"`
	Workdir    string    `json:"workdir"`
	Worktree   bool      `json:"worktree"`
	Branch     string    `json:"branch,omitempty"`
	Base       string    `json:"base,omitempty"`
	Network    string    `json:"network"`
	GPU        bool      `json:"gpu,omitempty"`
	Data       bool      `json:"data,omitempty"`
	Image      string    `json:"image"`
	Engine     string    `json:"engine"`
	ProxyIP    string    `json:"proxy_ip"`
	Background bool      `json:"background,omitempty"`
	Prompt     string    `json:"prompt,omitempty"`
	NotGit     bool      `json:"not_git,omitempty"`
	TaskFile   string    `json:"task_file,omitempty"`
	Bundles    []string  `json:"bundles,omitempty"`
	Skills     []string  `json:"skills,omitempty"`
	Created    time.Time `json:"created"`
}

type boxOptions struct {
	name     string
	worktree bool
	here     bool
	gpu      bool
	data     bool
	network  string
	image    string
	memory   string
	allow    []string
	force    bool
	all      bool
	yes      bool
	full     bool
	detach   bool   // hi agent
	json     bool   // hi agent
	taskFile string // hi agent
	bundles  []string
	words    []string
	// fromBrief holds what a task file's front matter asked for, to ask
	// about before it widens the box (agent.go).
	fromBrief *agentFrontMatter
}

func boxStateFile(parts ...string) string {
	path, err := qStatePath("")
	if err != nil {
		return ""
	}
	// hi q's state folder is ~/.local/state/hi/q; boxes live next to it.
	return filepath.Join(append([]string{filepath.Dir(filepath.Clean(path)), "box"}, parts...)...)
}

func runBox(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printBoxUsage(stdout)
		return 0
	}
	command, rest := args[0], args[1:]
	if command == "__proxy" {
		return runBoxProxy(rest, stderr)
	}
	options, err := parseBoxOptions(command, rest)
	if err != nil {
		fmt.Fprintf(stderr, "hi: %v\n\n", err)
		printBoxUsage(stderr)
		return 2
	}
	switch command {
	case "claude", "codex":
		fmt.Fprintf(stderr, "hi: agents moved: use hi agent %s, with the same options\n", command)
		return 2
	case "shell", "run":
		_, err := startBox(command, options, stdin, stdout, stderr)
		return exitCode(err, stderr)
	case "ls":
		return exitCode(listBoxes(stdout), stderr)
	case "attach":
		return exitCode(withBox(options, func(engine boxEngine, meta boxMeta) error { return attachBox(engine, meta, stdin, stdout, stderr) }), stderr)
	case "diff":
		return exitCode(withBox(options, func(_ boxEngine, meta boxMeta) error { return diffBox(meta, options.full, stdout) }), stderr)
	case "stop":
		return exitCode(withBox(options, func(engine boxEngine, meta boxMeta) error { return stopBox(engine, meta, stdout) }), stderr)
	case "rm":
		if options.all {
			return exitCode(removeAllBoxes(options, stdin, stdout), stderr)
		}
		return exitCode(withBox(options, func(engine boxEngine, meta boxMeta) error { return removeBox(engine, meta, options.force, stdout) }), stderr)
	case "allow":
		return exitCode(allowBox(options, stdout), stderr)
	case "token":
		fmt.Fprintln(stderr, "hi: the Claude token moved: use hi agent token claude")
		return 2
	}
	fmt.Fprintf(stderr, "hi: unknown box command %q\n\n", command)
	printBoxUsage(stderr)
	return 2
}

func printBoxUsage(w io.Writer) {
	fmt.Fprintln(w, `hi box runs a shell or a command in a rootless container that holds the
project and nothing else from your home folder, with no way out but hi's
proxy. Coding agents run in boxes with hi agent.

usage:
  hi box shell                    a shell in a box for this project
  hi box run -- <command>         run one command in a box and exit with its status
  hi box ls                       boxes, their state, branch, and changes
  hi box attach <name>            follow a box's agent, or take over its terminal
  hi box diff <name> [--full]     what the box changed, flagging files that run on the host
  hi box allow <name> [<domain>]  let a box reach a domain; without one, list what was blocked
  hi box stop|rm <name> [--force]
  hi box rm --all [--force] [--yes]
                                  remove every box after one question; boxes with
                                  uncommitted work are kept unless --force

options when starting:
  --name <name>          the box's name (default: the project and a number)
  --network locked|dev|open
                         locked: nothing (an agent's own hosts only); dev (default):
                         GitHub and package registries; open: everything, still logged
  --allow <domain>       one more domain (repeat it)
  --gpu                  the AMD GPU (Strix Halo)
  --data                 the team's Hugging Face data through the hi server (hi data);
                         the box gets a placeholder token, the proxy the real one
  --worktree             a new git worktree (agents always get one)
  --here                 work in the project folder itself, not a worktree
  --image <image>        another image; devcontainer.json's image or Dockerfile is used too
  --bundle a,b           skills and their tools from bundles (hi bundle ls), on an image built once
  --memory <size>        memory limit (default 16g)`)
}

func parseBoxOptions(command string, args []string) (boxOptions, error) {
	options := boxOptions{memory: "16g"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func() (string, error) {
			if _, after, ok := strings.Cut(arg, "="); ok {
				return after, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", arg)
			}
			i++
			return args[i], nil
		}
		var err error
		var text string
		switch {
		case arg == "--":
			options.words = append(options.words, args[i+1:]...)
			i = len(args)
		case arg == "--worktree":
			options.worktree = true
		case arg == "--here":
			options.here = true
		case arg == "--gpu":
			options.gpu = true
		case arg == "--data":
			options.data = true
		case arg == "--all" && command == "rm":
			options.all = true
		case (arg == "--yes" || arg == "-y") && command == "rm":
			options.yes = true
		case arg == "--force" || arg == "-f":
			options.force = true
		case arg == "--full":
			options.full = true
		case arg == "--detach" && command == "agent":
			options.detach = true
		case arg == "--json" && command == "agent":
			options.json = true
		case command == "agent" && (arg == "--task-file" || strings.HasPrefix(arg, "--task-file=")):
			options.taskFile, err = value()
		case arg == "--bundle" || strings.HasPrefix(arg, "--bundle="):
			text, err = value()
			for _, name := range strings.Split(text, ",") {
				if name = strings.TrimSpace(name); name != "" && !containsString(options.bundles, name) {
					options.bundles = append(options.bundles, name)
				}
			}
		case arg == "--name" || strings.HasPrefix(arg, "--name="):
			options.name, err = value()
		case arg == "--network" || strings.HasPrefix(arg, "--network="):
			options.network, err = value()
			if err == nil {
				if _, ok := boxPresets[options.network]; !ok {
					return options, fmt.Errorf("--network is locked, dev, or open, not %q", options.network)
				}
			}
		case arg == "--image" || strings.HasPrefix(arg, "--image="):
			options.image, err = value()
		case arg == "--memory" || strings.HasPrefix(arg, "--memory="):
			options.memory, err = value()
		case arg == "--allow" || strings.HasPrefix(arg, "--allow="):
			text, err = value()
			options.allow = append(options.allow, text)
		case strings.HasPrefix(arg, "-") && arg != "-" && command != "run":
			return options, fmt.Errorf("unknown option %s", arg)
		default:
			// An agent's task, a box's name, or run's command.
			if command == "run" {
				options.words = append(options.words, args[i:]...)
				i = len(args)
			} else {
				options.words = append(options.words, arg)
			}
		}
		if err != nil {
			return options, err
		}
	}
	if command == "run" && len(options.words) == 0 {
		return options, errors.New("usage: hi box run -- <command>")
	}
	if options.here && options.worktree {
		return options, errors.New("--here and --worktree don't go together")
	}
	return options, nil
}

// ---------------------------------------------------------------------------
// starting a box

// startBox starts a box for kind: shell, run, or an agent from hi agent.
// An agent with a prompt starts in the background, and startBox returns
// its box at once; everything else runs in the terminal until it ends.
func startBox(kind string, options boxOptions, stdin io.Reader, stdout, stderr io.Writer) (meta boxMeta, err error) {
	engine, warning, err := detectBoxEngine()
	if err != nil {
		return meta, err
	}
	if warning != "" {
		fmt.Fprintln(stderr, "hi: "+warning)
	}
	root, inGit := boxProjectRoot()
	home, _ := os.UserHomeDir()
	if root == "" || root == "/" || strings.HasPrefix(home+"/", root+"/") {
		return meta, fmt.Errorf("run hi box in a project folder, not in %s: the box gets the whole folder", firstNonEmpty(root, "/"))
	}
	devcontainer, err := readBoxDevcontainer(root)
	if err != nil {
		return meta, err
	}
	if devcontainer != nil && len(devcontainer.ignored) > 0 {
		fmt.Fprintf(stdout, "Ignoring %s from %s: they run on the host or widen the box.\n", strings.Join(devcontainer.ignored, ", "), filepath.Base(devcontainer.path))
	}
	var custom boxCustomizations
	if devcontainer != nil {
		custom = devcontainer.Customizations.Hi
	}
	network := firstNonEmpty(options.network, custom.Network, "dev")
	if _, ok := boxPresets[network]; !ok {
		return meta, fmt.Errorf("devcontainer.json asks for network %q; use locked, dev, or open", network)
	}
	gpu := options.gpu || custom.GPU
	if gpu {
		if _, err := os.Stat("/dev/kfd"); err != nil {
			return meta, errors.New("--gpu needs an AMD GPU with ROCm (/dev/kfd); install it with hi install strix")
		}
	}

	data := options.data || custom.Data

	// Bundles: their skills' files, and the network they need, before
	// anything is made.
	var bundles *bundlePlan
	var bundleHosts []string
	if len(options.bundles) > 0 {
		if options.image != "" || (devcontainer != nil && (devcontainer.Image != "" || devcontainer.Build.Dockerfile != "")) {
			return meta, errors.New("--bundle builds its image on hi's own base image, so it doesn't go with --image or an image from devcontainer.json yet")
		}
		all, err := loadAllBundles(stderr)
		if err != nil {
			return meta, err
		}
		if bundles, err = planBundles(options.bundles, all); err != nil {
			return meta, err
		}
		if network, bundleHosts, err = bundles.widen(network, options.allow, stdin, stdout); err != nil {
			return meta, err
		}
		if data && network == "open" {
			return meta, errors.New("--data doesn't go with an open network: an agent reading untrusted pages could be told to pass the team's data on; run the data work in a separate box")
		}
		if err := bundles.fetchSkills(stdout); err != nil {
			return meta, err
		}
	}

	agent := agentKinds[kind]
	prompt := ""
	if agent {
		prompt = strings.Join(options.words, " ")
	}
	name := options.name
	if name == "" {
		name = nextBoxName(filepath.Base(root))
	} else if !boxNamePattern.MatchString(name) {
		return meta, fmt.Errorf("a box name has lowercase letters, digits, and dashes, up to 40: %q", name)
	}
	stateDir := boxStateFile(name)
	if existing, err := loadBoxMeta(name); err == nil {
		// shell in a running box opens another shell there.
		if kind == "shell" && engine.state("hi-box-"+name) == "running" {
			return meta, engine.interactive(stdin, stdout, stderr, "exec", "-it", "hi-box-"+name, "bash")
		}
		return meta, fmt.Errorf("box %s already exists (%s); attach with hi box attach %s, or remove it with hi box rm %s", name, existing.Agent, name, name)
	}
	if err := os.MkdirAll(filepath.Join(stateDir, "home"), 0o700); err != nil {
		return meta, err
	}
	cleanup := func() { os.RemoveAll(stateDir) }

	var dataSetup *boxData
	if data {
		if dataSetup, err = prepareBoxData(stateDir); err != nil {
			cleanup()
			return meta, err
		}
	}
	meta = boxMeta{Name: name, Agent: kind, Root: root, Network: network, GPU: gpu, Data: data, Engine: engine.name,
		Created: time.Now().UTC(), Background: agent && prompt != "", Prompt: qClip(prompt), TaskFile: options.taskFile}
	// Agents get a worktree in a git repository, and work in place outside
	// one.
	meta.NotGit = !inGit
	useWorktree := (agent && !options.here && inGit) || options.worktree
	var mounts []string
	var gitCommon string
	if inGit {
		gitCommon = boxGit(root, "rev-parse", "--path-format=absolute", "--git-common-dir")
	}
	if useWorktree {
		if !inGit {
			cleanup()
			return meta, errors.New("--worktree needs a git repository; without it, the box works in the folder itself")
		}
		meta.Worktree = true
		meta.Branch = "hi-box/" + name
		meta.Base = boxGit(root, "rev-parse", "HEAD")
		work := filepath.Join(stateDir, "work")
		var out bytes.Buffer
		if err := boxCommand(nil, &out, &out, "git", "-C", root, "worktree", "add", "-q", "-b", meta.Branch, work, "HEAD"); err != nil {
			cleanup()
			return meta, fmt.Errorf("git worktree add: %s", qFirstLine(out.String(), err.Error()))
		}
		meta.Workdir = work
		mounts = append(mounts, work+":"+work)
		if dirty := boxGit(root, "status", "--porcelain"); dirty != "" {
			fmt.Fprintln(stdout, "Note: the box starts from your last commit; uncommitted changes in the project are not in it.")
		}
	} else {
		meta.Workdir, _ = os.Getwd()
		mounts = append(mounts, root+":"+root)
		if agent {
			if err := saveFolderSnapshot(name, root, stdout); err != nil {
				cleanup()
				return meta, err
			}
		}
	}
	if gitCommon != "" {
		mounts = append(mounts, gitCommon+":"+gitCommon)
		// The agent may commit, but not plant hooks or change git's settings.
		for _, part := range []string{"hooks", "config"} {
			if _, err := os.Stat(filepath.Join(gitCommon, part)); err == nil {
				mounts = append(mounts, filepath.Join(gitCommon, part)+":"+filepath.Join(gitCommon, part)+":ro")
			}
		}
	}

	// The allowlist: the preset, the project's domains once allowed, and
	// --allow.
	hosts := boxPresetHosts(network)
	if len(custom.Domains) > 0 && boxDomainDecision(root, custom.Domains, stdin, stdout) {
		hosts = append(hosts, custom.Domains...)
	}
	hosts = append(hosts, agentHosts[kind]...)
	hosts = append(hosts, options.allow...)
	hosts = append(hosts, bundleHosts...)
	if data {
		hosts = append(hosts, boxDataHosts...)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "allow"), []byte(strings.Join(hosts, "\n")+"\n"), 0o600); err != nil {
		cleanup()
		return meta, err
	}

	baseImage, err := engine.ensureBaseImage(stdout, stderr)
	if err != nil {
		removeBoxWorktree(meta, true)
		cleanup()
		return meta, err
	}
	meta.Image = baseImage
	imageCached := false
	switch {
	case bundles != nil:
		if meta.Image, imageCached, err = bundles.ensureImage(engine, baseImage, stdout, stderr); err != nil {
			removeBoxWorktree(meta, true)
			cleanup()
			return meta, err
		}
		meta.Bundles, meta.Skills = bundles.bundleNames(), bundles.skillNames()
	case options.image != "":
		meta.Image = options.image
	case devcontainer != nil && devcontainer.Image != "":
		meta.Image = devcontainer.Image
	case devcontainer != nil && devcontainer.Build.Dockerfile != "":
		dir := filepath.Dir(devcontainer.path)
		context := filepath.Join(dir, firstNonEmpty(devcontainer.Build.Context, "."))
		if meta.Image, err = engine.ensureProjectImage(filepath.Join(dir, devcontainer.Build.Dockerfile), context, stdout, stderr); err != nil {
			removeBoxWorktree(meta, true)
			cleanup()
			return meta, err
		}
	}

	fail := func(err error) error {
		teardownBox(engine, meta)
		removeBoxWorktree(meta, true)
		cleanup()
		return err
	}
	if err := startBoxNetwork(engine, &meta, stateDir, kind == "claude", dataSetup); err != nil {
		return meta, fail(err)
	}

	run := []string{"run", "--name", "hi-box-" + name, "--hostname", name, "--network", "hi-box-" + name, "--dns", "127.0.0.1"}
	switch {
	case meta.Background:
		run = append(run, "-d")
	case isTerminal(stdin):
		run = append(run, "-it")
	default:
		run = append(run, "-i")
	}
	run = append(run, engine.userArgs()...)
	run = append(run, boxHardening(options.memory)...)
	if gpu {
		run = append(run, engine.gpuArgs()...)
	}
	homeDir := filepath.Join(stateDir, "home")
	run = append(run, "-v", homeDir+":/box/home", "-e", "HOME=/box/home", "-w", meta.Workdir)
	for _, mount := range mounts {
		run = append(run, "-v", mount)
	}
	proxy := fmt.Sprintf("http://%s:%d", meta.ProxyIP, boxProxyPort)
	env := map[string]string{
		"HTTPS_PROXY": proxy, "HTTP_PROXY": proxy, "https_proxy": proxy, "http_proxy": proxy,
		"NO_PROXY": "localhost,127.0.0.1," + meta.ProxyIP, "no_proxy": "localhost,127.0.0.1," + meta.ProxyIP,
		"HI_BOX": name, "PATH": "/opt/codex/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	if bundles != nil {
		// The bundle image's tools come first. Chrome ignores HTTPS_PROXY,
		// so agent-browser passes the proxy to it.
		env["PATH"] = "/opt/hi/venv/bin:/opt/hi/npm/bin:/opt/hi/bin:" + env["PATH"]
		if bundles.chrome {
			env["AGENT_BROWSER_PROXY"] = proxy
			env["AGENT_BROWSER_PROXY_BYPASS"] = "localhost,127.0.0.1"
		}
		if err := bundles.installSkills(homeDir); err != nil {
			return meta, fail(err)
		}
	}
	if data {
		// hf and huggingface_hub reach the server through the proxy's data
		// listener, which puts the hi data token in place of this one.
		env["HF_ENDPOINT"] = fmt.Sprintf("http://%s:%d", meta.ProxyIP, boxDataPort)
		env["HF_TOKEN"] = boxClaudePlacehold
	}
	for _, key := range []string{"name", "email"} {
		if value := boxGit(root, "config", "user."+key); value != "" {
			upper := strings.ToUpper(key)
			env["GIT_AUTHOR_"+upper], env["GIT_COMMITTER_"+upper] = value, value
		}
	}
	if devcontainer != nil {
		for key, value := range devcontainer.ContainerEnv {
			env[key] = value
		}
	}
	var command []string
	switch kind {
	case "shell":
		command = []string{"bash"}
	case "run":
		command = options.words
	default:
		command, err = agentBoxSetup(kind, prompt, meta, homeDir, &run, env)
	}
	if err != nil {
		return meta, fail(err)
	}
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		run = append(run, "-e", key+"="+env[key])
	}
	run = append(run, meta.Image)
	if post := devcontainer.postCreate(); post != "" {
		run = append(run, "sh", "-c", post+` && exec "$@"`, "sh")
	}
	run = append(run, command...)

	if err := saveBoxMeta(meta); err != nil {
		return meta, fail(err)
	}
	if bundles != nil {
		bundles.printPlan(meta.Image, imageCached, network, stdout)
	}
	describeBox(meta, gpu, stdout)
	if meta.Background {
		if _, err := engine.output(run...); err != nil {
			return meta, fail(err)
		}
		return meta, nil
	}
	err = engine.interactive(stdin, stdout, stderr, run...)
	engine.output("stop", "-t", "2", "hi-box-"+name+"-proxy")
	syncCodexAuth(meta)
	switch kind {
	case "run":
		// Pass the command's own status on.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return meta, exitStatusError{code: exitErr.ExitCode(), message: fmt.Sprintf("the command exited with status %d", exitErr.ExitCode())}
		}
	default:
		fmt.Fprintf(stdout, "\nBox %s has stopped. hi box diff %s shows its work; hi box rm %s removes it.\n", name, name, name)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return meta, nil // the agent or shell's own exit status
		}
		return meta, err
	}
	return meta, nil
}

// boxHostBinary resolves an installed agent to its real file.
func boxHostBinary(name string) (string, error) {
	path, err := boxLookPath(name)
	if err != nil {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, ".local", "bin", name)
		if _, statErr := os.Stat(path); statErr != nil {
			return "", err
		}
	}
	return filepath.EvalSymlinks(path)
}

// startBoxNetwork creates the box's internal network and starts its proxy,
// which is also on a normal network.
func startBoxNetwork(engine boxEngine, meta *boxMeta, stateDir string, claude bool, data *boxData) error {
	network := "hi-box-" + meta.Name
	hash := fnv.New32a()
	hash.Write([]byte(meta.Name))
	var lastErr error
	for attempt := uint32(0); attempt < 40; attempt++ {
		third := (hash.Sum32()+attempt*37)%250 + 1
		subnet := fmt.Sprintf("10.234.%d.0/24", third)
		args := []string{"network", "create", "--internal", "--subnet", subnet}
		if engine.name == "podman" {
			args = append(args, "--disable-dns")
		}
		if _, lastErr = engine.output(append(args, network)...); lastErr == nil {
			meta.ProxyIP = fmt.Sprintf("10.234.%d.2", third)
			break
		}
	}
	if meta.ProxyIP == "" {
		return fmt.Errorf("could not create the box's network: %w", lastErr)
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	proxy := "hi-box-" + meta.Name + "-proxy"
	args := []string{"create", "--name", proxy, "--network", network, "--ip", meta.ProxyIP}
	args = append(args, engine.userArgs()...)
	args = append(args, "--cap-drop=ALL", "--security-opt", "no-new-privileges", "--memory", "256m",
		"-v", executable+":/usr/local/bin/hi:ro", "-v", stateDir+":/state")
	secret := "-"
	if claude {
		source, err := boxClaudeSecret()
		if err != nil {
			return err
		}
		args = append(args, "-v", source+":/secrets/claude:ro")
		secret = "/secrets/claude"
	}
	if data != nil && data.address != "" {
		// The server's name may resolve only on the host, through NetBird.
		args = append(args, "--add-host", data.host+":"+data.address)
	}
	args = append(args, boxBaseImage(), "/usr/local/bin/hi", "box", "__proxy", "/state/allow", "/state/network.log", secret)
	if data != nil {
		args = append(args, "/state/"+boxDataTokenFile, data.upstream)
	}
	if _, err := engine.output(args...); err != nil {
		return err
	}
	if _, err := engine.output("network", "connect", engine.defaultNetwork(), proxy); err != nil {
		return err
	}
	_, err = engine.output("start", proxy)
	return err
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func describeBox(meta boxMeta, gpu bool, stdout io.Writer) {
	where := meta.Workdir
	switch {
	case meta.Worktree:
		where = fmt.Sprintf("%s (branch %s)", meta.Workdir, meta.Branch)
	case agentKinds[meta.Agent] && meta.NotGit:
		where += " (not a git repository: it works in place)"
	case agentKinds[meta.Agent]:
		where += " (it works in place)"
	}
	parts := []string{fmt.Sprintf("network %s", meta.Network)}
	if meta.Data {
		parts = append(parts, "hi data through the server")
	}
	if gpu {
		parts = append(parts, "GPU: shares the host kernel and GPU driver")
	}
	fmt.Fprintf(stdout, "Box %s: %s in %s, %s.\n", meta.Name, meta.Agent, where, strings.Join(parts, ", "))
}

// boxProjectRoot is the git repository's root, or the current folder.
func boxProjectRoot() (string, bool) {
	if root := boxGit(".", "rev-parse", "--show-toplevel"); root != "" {
		return root, true
	}
	wd, _ := os.Getwd()
	return wd, false
}

func boxGit(dir string, args ...string) string {
	var out bytes.Buffer
	if err := boxCommand(nil, &out, io.Discard, "git", append([]string{"-C", dir}, args...)...); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}

func nextBoxName(project string) string {
	base := strings.Trim(regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(strings.ToLower(project), "-"), "-")
	if base == "" {
		base = "box"
	}
	if len(base) > 30 {
		base = base[:30]
	}
	for n := 1; ; n++ {
		name := fmt.Sprintf("%s-%d", base, n)
		if !fileExists(boxStateFile(name)) {
			return name
		}
	}
}

func saveBoxMeta(meta boxMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(boxStateFile(meta.Name, "box.json"), append(data, '\n'), 0o600)
}

func loadBoxMeta(name string) (boxMeta, error) {
	var meta boxMeta
	data, err := os.ReadFile(boxStateFile(name, "box.json"))
	if err != nil {
		return meta, fmt.Errorf("no box named %s; hi box ls lists them", name)
	}
	return meta, json.Unmarshal(data, &meta)
}

// withBox runs an action on the box named in the arguments.
func withBox(options boxOptions, action func(boxEngine, boxMeta) error) error {
	if len(options.words) != 1 {
		return usageError{"name one box; hi box ls lists them"}
	}
	meta, err := loadBoxMeta(options.words[0])
	if err != nil {
		return err
	}
	engine := boxEngine{name: meta.Engine}
	if engine.bin, err = boxLookPath(meta.Engine); err != nil {
		return fmt.Errorf("%s made this box but is not installed now", meta.Engine)
	}
	return action(engine, meta)
}

// ---------------------------------------------------------------------------
// the other commands

func listBoxes(stdout io.Writer) error {
	entries, _ := os.ReadDir(boxStateFile())
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	count := 0
	for _, entry := range entries {
		meta, err := loadBoxMeta(entry.Name())
		if err != nil {
			continue
		}
		if count == 0 {
			fmt.Fprintln(table, "NAME\tAGENT\tSTATE\tBRANCH\tCHANGES\tAGE")
		}
		count++
		state := "gone"
		if path, err := boxLookPath(meta.Engine); err == nil {
			engine := boxEngine{name: meta.Engine, bin: path}
			state = firstNonEmpty(engine.state("hi-box-"+meta.Name), "gone")
			if state != "running" && engine.state("hi-box-"+meta.Name+"-proxy") == "running" {
				engine.output("stop", "-t", "2", "hi-box-"+meta.Name+"-proxy")
			}
		}
		changes := "-"
		if meta.Worktree {
			changes = firstNonEmpty(boxShortStat(meta), "none")
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%s\n", meta.Name, meta.Agent, state, firstNonEmpty(meta.Branch, "-"), changes, formatDuration(time.Since(meta.Created)))
	}
	if count == 0 {
		fmt.Fprintln(stdout, "No boxes. Start one with hi box shell, hi box run, or hi agent.")
		return nil
	}
	return table.Flush()
}

// boxShortStat is "3 files +40 -2, 1 new" for a worktree box, counting
// uncommitted work too.
func boxShortStat(meta boxMeta) string {
	if !fileExists(meta.Workdir) {
		return ""
	}
	stat := boxGit(meta.Workdir, "diff", "--shortstat", meta.Base)
	number := func(pattern string) string {
		if match := regexp.MustCompile(`(\d+) ` + pattern).FindStringSubmatch(stat); match != nil {
			return match[1]
		}
		return "0"
	}
	var parts []string
	if stat != "" {
		parts = append(parts, fmt.Sprintf("%s files +%s -%s", number("files? changed"), number("insertions?"), number("deletions?")))
	}
	if untracked := boxUntracked(meta.Workdir); len(untracked) > 0 {
		parts = append(parts, fmt.Sprintf("%d new", len(untracked)))
	}
	return strings.Join(parts, ", ")
}

func attachBox(engine boxEngine, meta boxMeta, stdin io.Reader, stdout, stderr io.Writer) error {
	container := "hi-box-" + meta.Name
	switch state := engine.state(container); {
	case state == "running" && !meta.Background:
		fmt.Fprintln(stdout, "Taking over the box's terminal.")
		return engine.interactive(stdin, stdout, stderr, "attach", container)
	case state == "running":
		return engine.interactive(nil, stdout, stderr, "logs", "-f", container)
	case state == "":
		return fmt.Errorf("box %s's container is gone; hi box rm %s cleans up", meta.Name, meta.Name)
	default:
		fmt.Fprintf(stdout, "%s has %s. Its output:\n", meta.Name, state)
		return engine.interactive(nil, stdout, stderr, "logs", container)
	}
}

func diffBox(meta boxMeta, full bool, stdout io.Writer) error {
	if !meta.Worktree {
		changes, ok, complete := folderChanges(meta.Name, meta.Root)
		switch {
		case ok && len(changes) == 0:
			fmt.Fprintf(stdout, "%s has changed nothing in %s.\n", meta.Name, meta.Root)
		case ok:
			fmt.Fprintf(stdout, "Changed in %s since %s started:\n", meta.Root, meta.Name)
			for _, change := range changes {
				fmt.Fprintf(stdout, "  %-8s %s\n", change.Status, change.Path)
			}
			if !complete {
				fmt.Fprintln(stdout, "The folder has too many files to compare them all.")
			}
		case meta.NotGit:
			fmt.Fprintf(stdout, "%s worked in %s itself.\n", meta.Name, meta.Root)
		default:
			fmt.Fprintf(stdout, "%s worked in %s itself; see its changes there with git status.\n", meta.Name, meta.Root)
		}
		return nil
	}
	if !fileExists(meta.Workdir) {
		return fmt.Errorf("the worktree %s is gone", meta.Workdir)
	}
	commits := boxGit(meta.Workdir, "log", "--oneline", meta.Base+"..HEAD")
	files := boxGit(meta.Workdir, "diff", "--name-status", meta.Base)
	for _, path := range boxUntracked(meta.Workdir) {
		files = strings.TrimSpace(files + "\nnew\t" + path)
	}
	if commits == "" && files == "" {
		fmt.Fprintf(stdout, "%s has changed nothing yet.\n", meta.Name)
		return nil
	}
	fmt.Fprintf(stdout, "%s on %s, from %s:\n", meta.Name, meta.Branch, meta.Base[:min(12, len(meta.Base))])
	if commits != "" {
		fmt.Fprintf(stdout, "\nCommits\n%s\n", qIndent(commits, "  "))
	}
	if uncommitted := boxGit(meta.Workdir, "status", "--porcelain"); uncommitted != "" {
		fmt.Fprintln(stdout, "\nNot committed yet: the files marked below include work in the worktree.")
	}
	fmt.Fprintln(stdout, "\nFiles")
	var risky []string
	for _, line := range strings.Split(files, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		path := fields[len(fields)-1]
		mark := ""
		if boxRiskyFiles.MatchString(path) {
			mark = "   ⚠ runs on the host later"
			risky = append(risky, path)
		}
		fmt.Fprintf(stdout, "  %s %s%s\n", fields[0], path, mark)
	}
	if len(risky) > 0 {
		fmt.Fprintf(stdout, "\nRead %s before you run anything from this branch on your machine.\n", strings.Join(risky, ", "))
	}
	fmt.Fprintf(stdout, "\nThe work is on the branch %s in %s. Merge it there after review:\n  git merge %s\n", meta.Branch, meta.Root, meta.Branch)
	if full {
		var out bytes.Buffer
		boxCommand(nil, &out, io.Discard, "git", "-C", meta.Workdir, "diff", meta.Base)
		fmt.Fprintf(stdout, "\n%s", out.String())
	}
	return nil
}

func stopBox(engine boxEngine, meta boxMeta, stdout io.Writer) error {
	engine.output("stop", "-t", "5", "hi-box-"+meta.Name)
	engine.output("stop", "-t", "2", "hi-box-"+meta.Name+"-proxy")
	fmt.Fprintf(stdout, "Stopped %s. Its files and branch are kept; hi box rm %s removes it.\n", meta.Name, meta.Name)
	return nil
}

func teardownBox(engine boxEngine, meta boxMeta) {
	engine.output("rm", "-f", "hi-box-"+meta.Name)
	engine.output("rm", "-f", "hi-box-"+meta.Name+"-proxy")
	engine.output("network", "rm", "hi-box-"+meta.Name)
}

func removeBox(engine boxEngine, meta boxMeta, force bool, stdout io.Writer) error {
	if meta.Worktree && fileExists(meta.Workdir) && !force {
		if dirty := boxGit(meta.Workdir, "status", "--porcelain"); dirty != "" {
			return fmt.Errorf("%s has work that isn't committed in %s; commit it on the branch, or remove the box anyway with --force", meta.Name, meta.Workdir)
		}
	}
	teardownBox(engine, meta)
	syncCodexAuth(meta)
	removeBoxWorktree(meta, force)
	if err := os.RemoveAll(boxStateFile(meta.Name)); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed %s.", meta.Name)
	if meta.Worktree && boxGit(meta.Root, "rev-parse", "--verify", "-q", meta.Branch) != "" {
		if boxGit(meta.Root, "rev-parse", meta.Branch) == meta.Base {
			boxGit(meta.Root, "branch", "-D", meta.Branch)
		} else {
			fmt.Fprintf(stdout, " Its branch %s is kept; delete it with git branch -D %s.", meta.Branch, meta.Branch)
		}
	}
	fmt.Fprintln(stdout)
	return nil
}

func removeBoxWorktree(meta boxMeta, force bool) {
	if !meta.Worktree || meta.Workdir == "" {
		return
	}
	args := []string{"worktree", "remove", meta.Workdir}
	if force {
		args = []string{"worktree", "remove", "--force", meta.Workdir}
	}
	boxGit(meta.Root, args...)
	boxGit(meta.Root, "worktree", "prune")
}

var boxDomainPattern = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)+[a-z]{2,}$`)

// allowBox adds a domain to a box's allowlist, which its proxy rereads at
// once, or lists what the box was refused.
func allowBox(options boxOptions, stdout io.Writer) error {
	if len(options.words) == 0 || len(options.words) > 2 {
		return usageError{"usage: hi box allow <name> [<domain>]"}
	}
	meta, err := loadBoxMeta(options.words[0])
	if err != nil {
		return err
	}
	if len(options.words) == 1 {
		blocked := map[string]int{}
		for _, line := range boxReadLines(boxStateFile(meta.Name, "network.log"), 5000) {
			fields := strings.Fields(line)
			if len(fields) == 3 && fields[1] == "blocked" {
				blocked[fields[2]]++
			}
		}
		if len(blocked) == 0 {
			fmt.Fprintf(stdout, "%s hasn't been refused anything.\n", meta.Name)
			return nil
		}
		fmt.Fprintf(stdout, "%s was refused:\n", meta.Name)
		hosts := make([]string, 0, len(blocked))
		for host := range blocked {
			hosts = append(hosts, host)
		}
		sort.Strings(hosts)
		for _, host := range hosts {
			fmt.Fprintf(stdout, "  %s (%s)\n", host, strconv.Itoa(blocked[host])+" times")
		}
		fmt.Fprintf(stdout, "Allow one with hi box allow %s <domain>.\n", meta.Name)
		return nil
	}
	domain := strings.ToLower(strings.TrimSpace(options.words[1]))
	if !boxDomainPattern.MatchString(domain) {
		return fmt.Errorf("%q is not a domain such as files.pythonhosted.org", domain)
	}
	file, err := os.OpenFile(boxStateFile(meta.Name, "allow"), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.WriteString(domain + "\n"); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s may now reach %s and its subdomains. No restart needed.\n", meta.Name, strings.TrimPrefix(domain, "*."))
	return nil
}

// boxUntracked lists new files the box hasn't added to git, without
// touching its index.
func boxUntracked(dir string) []string {
	out := boxGit(dir, "ls-files", "--others", "--exclude-standard")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// removeAllBoxes removes every box after one confirmation. Boxes with
// uncommitted work are kept unless --force; branches with commits are kept
// as with a single rm.
func removeAllBoxes(options boxOptions, stdin io.Reader, stdout io.Writer) error {
	if len(options.words) > 0 {
		return usageError{"hi box rm --all takes no box names"}
	}
	entries, _ := os.ReadDir(boxStateFile())
	var boxes []boxMeta
	for _, entry := range entries {
		if meta, err := loadBoxMeta(entry.Name()); err == nil {
			boxes = append(boxes, meta)
		}
	}
	if len(boxes) == 0 {
		fmt.Fprintln(stdout, "No boxes to remove.")
		return nil
	}
	var remove, keep []boxMeta
	fmt.Fprintln(stdout, "hi box rm --all removes these boxes, with their containers and worktrees:")
	for _, meta := range boxes {
		dirty := meta.Worktree && fileExists(meta.Workdir) && boxGit(meta.Workdir, "status", "--porcelain") != ""
		note := ""
		switch {
		case dirty && !options.force:
			note = "   kept: uncommitted work (use --force to remove it too)"
			keep = append(keep, meta)
		case dirty:
			note = "   ⚠ uncommitted work is lost"
			remove = append(remove, meta)
		default:
			remove = append(remove, meta)
		}
		fmt.Fprintf(stdout, "  %s (%s, %s)%s\n", meta.Name, meta.Agent, firstNonEmpty(meta.Branch, meta.Root), note)
	}
	if len(remove) == 0 {
		fmt.Fprintln(stdout, "Nothing to remove.")
		return nil
	}
	if !options.yes {
		if !isTerminal(stdin) {
			return errors.New("removing every box needs confirmation; rerun with --yes")
		}
		fmt.Fprintf(stdout, "Remove %d boxes? Branches with commits are kept. [y/N] ", len(remove))
		answer, _ := readLine(stdin)
		if answer = strings.ToLower(strings.TrimSpace(answer)); answer != "y" && answer != "yes" {
			fmt.Fprintln(stdout, "Nothing removed.")
			return nil
		}
	}
	var failed []string
	for _, meta := range remove {
		engine := boxEngine{name: meta.Engine}
		path, err := boxLookPath(meta.Engine)
		if err != nil {
			failed = append(failed, meta.Name+" ("+meta.Engine+" is not installed)")
			continue
		}
		engine.bin = path
		if err := removeBox(engine, meta, options.force, stdout); err != nil {
			failed = append(failed, meta.Name+" ("+err.Error()+")")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not remove %s", strings.Join(failed, ", "))
	}
	return nil
}
