package main

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Bundles in hi agent: --bundle web,office installs the bundles' skills in
// the box's home and runs it on an image with what they need, built once
// and cached as hi-agent:<hash>. See docs/specs/ideas/hi_agent_bundles.md.

//go:embed bundles/*.json
var builtinBundleFiles embed.FS

// bundleSource is where bundles come from: hi itself, a team's template
// source on the hi server, or the person's own folder. Later ones win.
type bundleSource struct {
	name   string // builtin, a team source's name, or local
	files  fs.FS  // bundles/, and skills/ for a team or local source
	commit string // a team source's commit
	dir    string // local: the folder on disk
}

func (s bundleSource) label() string {
	switch s.name {
	case "builtin":
		return "built in"
	case "local":
		return "local, " + s.dir
	}
	return "team source " + s.name
}

type loadedBundle struct {
	skillBundle
	source bundleSource
}

// bundleLocalDir is the person's own bundles: HI_BUNDLES_DIR, or
// ~/.local/share/hi/bundles.
func bundleLocalDir() string {
	if dir := os.Getenv("HI_BUNDLES_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(firstNonEmpty(os.Getenv("XDG_DATA_HOME"), filepath.Join(home, ".local", "share")), "hi", "bundles")
}

func loadBundleSource(source bundleSource) (map[string]*loadedBundle, error) {
	entries, err := fs.ReadDir(source.files, "bundles")
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	bundles := map[string]*loadedBundle{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := fs.ReadFile(source.files, path.Join("bundles", entry.Name()))
		if err != nil {
			return nil, err
		}
		bundle, err := parseSkillBundle(data, "bundles/"+entry.Name(), source.name != "builtin")
		if err != nil {
			return nil, err
		}
		if bundle.Name+".json" != entry.Name() {
			return nil, fmt.Errorf("bundles/%s names the bundle %q", entry.Name(), bundle.Name)
		}
		for _, skill := range bundle.Skills {
			if skill.Source == "." {
				if _, err := fs.Stat(source.files, path.Join("skills", skill.Skill, "SKILL.md")); err != nil {
					return nil, fmt.Errorf("bundles/%s names the skill %s, which is not in skills/", entry.Name(), skill.Skill)
				}
			}
		}
		bundles[bundle.Name] = &loadedBundle{skillBundle: bundle, source: source}
	}
	return bundles, nil
}

// loadAllBundles reads the built-in bundles, the team's on a connected
// device, and the local ones. A source with a broken bundle file is skipped
// with a warning, except the built-in one.
func loadAllBundles(stderr io.Writer) (map[string]*loadedBundle, error) {
	builtin, err := loadBundleSource(bundleSource{name: "builtin", files: builtinBundleFiles})
	if err != nil {
		return nil, fmt.Errorf("the built-in bundles: %w", err)
	}
	all := builtin
	add := func(source bundleSource) {
		bundles, err := loadBundleSource(source)
		if err != nil {
			fmt.Fprintf(stderr, "hi: warning: skipping the bundles from %s: %v\n", source.label(), err)
			return
		}
		for name, bundle := range bundles {
			all[name] = bundle
		}
	}
	for _, source := range loadServerTemplateSources(stderr) {
		add(bundleSource{name: source.name, files: source.files, commit: source.commit})
	}
	if dir := bundleLocalDir(); fileExists(filepath.Join(dir, "bundles")) {
		add(bundleSource{name: "local", files: os.DirFS(dir), dir: dir})
	}
	return all, nil
}

// ---------------------------------------------------------------------------
// the plan

// bundleSkillPlan is one skill a box gets.
type bundleSkillPlan struct {
	Name   string `json:"name"`
	Bundle string `json:"bundle"`
	Source string `json:"source"`
	Commit string `json:"commit,omitempty"`
	needs  *bundleNeeds
	from   bundleSource
	dir    string // its files on disk, once fetched
}

// bundlePlan is what --bundle resolves to.
type bundlePlan struct {
	bundles []*loadedBundle
	skills  []*bundleSkillPlan
	// network is the widest mode a skill asks for, hosts the hosts they add,
	// and reasons why, one line per skill.
	network string
	hosts   []string
	reasons []string
	notes   []string
	chrome  bool // agent-browser's Chrome, which needs the proxy passed to it
}

var bundleNetworkRank = map[string]int{"": 0, "locked": 0, "dev": 1, "open": 2}

// planBundles resolves bundle names to their skills, checks they fit
// together, and works out the network they need.
func planBundles(names []string, all map[string]*loadedBundle) (*bundlePlan, error) {
	plan := &bundlePlan{}
	seen := map[string]*bundleSkillPlan{}
	for _, name := range names {
		bundle, ok := all[name]
		if !ok {
			var known []string
			for name := range all {
				known = append(known, name)
			}
			sort.Strings(known)
			return nil, fmt.Errorf("there is no bundle %q; hi bundle ls lists them (%s)", name, strings.Join(known, ", "))
		}
		if containsBundle(plan.bundles, bundle) {
			continue
		}
		plan.bundles = append(plan.bundles, bundle)
		if bundle.Note != "" {
			plan.notes = append(plan.notes, bundle.Name+": "+bundle.Note)
		}
		for _, skill := range bundle.Skills {
			source := skill.Source
			if source == "." {
				source = bundle.source.name + "/skills"
			}
			if skill.Commit == "" && skill.Source != "." {
				return nil, fmt.Errorf("the %s bundle doesn't pin %s to a commit; hi skill update --bundles pins it", bundle.Name, skill.Skill)
			}
			if earlier, ok := seen[skill.Skill]; ok {
				if earlier.Source != source || earlier.Commit != skill.Commit {
					return nil, fmt.Errorf("the %s and %s bundles both have a skill %s, from different places", earlier.Bundle, bundle.Name, skill.Skill)
				}
				continue
			}
			entry := &bundleSkillPlan{Name: skill.Skill, Bundle: bundle.Name, Source: source, Commit: skill.Commit, needs: skill.Needs, from: bundle.source}
			seen[skill.Skill] = entry
			plan.skills = append(plan.skills, entry)
		}
	}
	sort.Slice(plan.skills, func(i, j int) bool { return plan.skills[i].Name < plan.skills[j].Name })
	hosts := map[string]bool{}
	npm := map[string]bool{}
	for _, skill := range plan.skills {
		if skill.needs == nil {
			continue
		}
		for _, pkg := range skill.needs.Npm {
			npm[strings.SplitN(strings.TrimPrefix(pkg, "@"), "@", 2)[0]] = true
		}
		if network := skill.needs.Network; network != nil {
			if bundleNetworkRank[network.Mode] > bundleNetworkRank[plan.network] {
				plan.network = network.Mode
			}
			for _, host := range network.Hosts {
				hosts[host] = true
			}
			if network.Mode == "open" || len(network.Hosts) > 0 {
				what := network.Mode
				if len(network.Hosts) > 0 {
					what = strings.Join(network.Hosts, ", ")
				}
				plan.reasons = append(plan.reasons, fmt.Sprintf("%s (%s): %s", skill.Name, what, firstNonEmpty(network.Reason, "no reason given")))
			}
		}
	}
	for _, skill := range plan.skills {
		if skill.needs != nil && containsString(skill.needs.Browsers, "chrome") {
			if !npm["agent-browser"] {
				return nil, fmt.Errorf("%s asks for chrome, which comes from agent-browser install; add agent-browser to its npm packages", skill.Name)
			}
			plan.chrome = true
		}
		if skill.needs != nil && containsString(skill.needs.Browsers, "chromium") && !containsPrefix(allBundlePip(plan), "playwright==") {
			return nil, fmt.Errorf("%s asks for chromium, which comes from Playwright; add playwright to its pip packages", skill.Name)
		}
	}
	plan.hosts = sortedKeys(hosts)
	return plan, nil
}

func containsBundle(list []*loadedBundle, bundle *loadedBundle) bool {
	for _, item := range list {
		if item == bundle {
			return true
		}
	}
	return false
}

func containsPrefix(list []string, prefix string) bool {
	for _, item := range list {
		if strings.HasPrefix(strings.ToLower(item), prefix) {
			return true
		}
	}
	return false
}

func allBundlePip(plan *bundlePlan) []string {
	var pip []string
	for _, skill := range plan.skills {
		if skill.needs != nil {
			pip = append(pip, skill.needs.Pip...)
		}
	}
	return pip
}

func (p *bundlePlan) bundleNames() []string {
	var names []string
	for _, bundle := range p.bundles {
		names = append(names, bundle.Name)
	}
	return names
}

func (p *bundlePlan) skillNames() []string {
	names := []string{"hi"}
	for _, skill := range p.skills {
		names = append(names, skill.Name)
	}
	sort.Strings(names)
	return names
}

// widen decides the box's network for the plan: the skills' mode and
// hosts beyond what the box would have are asked about in a terminal, and
// need --network or --allow without one. It returns the network to use
// and the hosts to add.
func (p *bundlePlan) widen(network string, allowed []string, stdin io.Reader, stdout io.Writer) (string, []string, error) {
	wider := bundleNetworkRank[p.network] > bundleNetworkRank[network]
	mode := network
	if wider {
		mode = p.network
	}
	var extra []string
	if mode != "open" {
		for _, host := range p.hosts {
			if !containsString(boxPresetHosts(mode), host) && !containsString(allowed, host) {
				extra = append(extra, host)
			}
		}
	}
	if !wider && len(extra) == 0 {
		return network, p.hosts, nil
	}
	var asks []string
	if wider {
		asks = append(asks, "network "+p.network)
	}
	asks = append(asks, extra...)
	if !isTerminal(stdin) {
		var flags []string
		if wider {
			flags = append(flags, "--network "+p.network)
		}
		for _, host := range extra {
			flags = append(flags, "--allow "+host)
		}
		return "", nil, fmt.Errorf("the bundles' skills need %s (%s); add %s to allow it", strings.Join(asks, " and "), strings.Join(p.reasons, "; "), strings.Join(flags, " "))
	}
	fmt.Fprintf(stdout, "The bundles' skills need more than the %s network:\n", network)
	for _, reason := range p.reasons {
		fmt.Fprintf(stdout, "  %s\n", reason)
	}
	fmt.Fprintf(stdout, "Allow %s for this box? [y/N] ", strings.Join(asks, " and "))
	if answer := readSkillAnswer(stdin); answer != "y" && answer != "yes" {
		return "", nil, errors.New("nothing was started: the box wouldn't have the network its skills need")
	}
	return mode, p.hosts, nil
}

// ---------------------------------------------------------------------------
// skill files

// bundleSkillCache is where skills fetched at a commit are kept.
func bundleSkillCache(parts ...string) string {
	base, err := os.UserCacheDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(append([]string{base, "hi", "skills"}, parts...)...)
}

var bundleCacheName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// fetchSkills puts each skill's files on disk: from the cache, from its
// repository at its commit, or from its bundle source.
func (p *bundlePlan) fetchSkills(stdout io.Writer) error {
	fetched := map[string]string{} // source → checkout, for skills sharing one
	defer func() {
		for _, dir := range fetched {
			os.RemoveAll(dir)
		}
	}()
	for _, skill := range p.skills {
		switch {
		case skill.from.name == "local" && skill.Commit == "":
			skill.dir = filepath.Join(skill.from.dir, "skills", skill.Name)
			continue
		case skill.Commit == "":
			// A team source's own skill, at the source's commit.
			dir := bundleSkillCache("team-"+bundleCacheName.ReplaceAllString(skill.from.name, "-"), firstNonEmpty(skill.from.commit, "current"), skill.Name)
			if skill.from.commit == "" || !fileExists(filepath.Join(dir, "SKILL.md")) {
				if err := copySkillFS(skill.from.files, path.Join("skills", skill.Name), dir); err != nil {
					return err
				}
			}
			skill.dir = dir
			continue
		}
		dir := bundleSkillCache(bundleCacheName.ReplaceAllString(skill.Source, "-"), skill.Commit, skill.Name)
		if fileExists(filepath.Join(dir, "SKILL.md")) {
			skill.dir = dir
			continue
		}
		source, err := parseSkillSource(skill.Source)
		if err != nil {
			return err
		}
		key := skill.Source + "@" + skill.Commit
		root, ok := fetched[key]
		if !ok {
			fmt.Fprintf(stdout, "Fetching %s at %s...\n", skill.Source, shortCommit(skill.Commit))
			var cleanup func()
			if root, cleanup, err = fetchSkillCommits(source, skill.Commit); err != nil {
				return err
			}
			_ = cleanup // the deferred loop removes it
			fetched[key] = root
		}
		found, err := discoverSkills(root, source.Path)
		if err != nil {
			return err
		}
		var match foundSkill
		for _, candidate := range found {
			if candidate.Name == skill.Name {
				match = candidate
			}
		}
		if match.Dir == "" {
			return fmt.Errorf("%s at %s has no skill %s", skill.Source, shortCommit(skill.Commit), skill.Name)
		}
		if err := copySkillTree(match.Dir, dir); err != nil {
			return err
		}
		skill.dir = dir
	}
	return nil
}

// copySkillTree copies a skill's regular files, without .git or links,
// replacing target.
func copySkillTree(source, target string) error {
	staging := target + ".hi-new"
	os.RemoveAll(staging)
	err := filepath.WalkDir(source, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(source, file)
		out := filepath.Join(staging, rel)
		switch {
		case entry.IsDir() && file != source && (entry.Name() == ".git" || entry.Name() == "node_modules"):
			return filepath.SkipDir
		case entry.IsDir():
			return os.MkdirAll(out, 0o755)
		case !entry.Type().IsRegular():
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return os.WriteFile(out, data, mode)
	})
	if err == nil {
		os.RemoveAll(target)
		err = os.Rename(staging, target)
	}
	if err != nil {
		os.RemoveAll(staging)
	}
	return err
}

func copySkillFS(files fs.FS, root, target string) error {
	staging := target + ".hi-new"
	os.RemoveAll(staging)
	err := fs.WalkDir(files, root, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(file, root), "/")
		out := filepath.Join(staging, filepath.FromSlash(rel))
		if entry.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		data, err := fs.ReadFile(files, file)
		if err != nil {
			return err
		}
		return os.WriteFile(out, data, 0o644)
	})
	if err == nil {
		os.RemoveAll(target)
		err = os.Rename(staging, target)
	}
	if err != nil {
		os.RemoveAll(staging)
	}
	return err
}

// installSkills puts the plan's skills and the hi skill in a box's home
// folder, where Claude Code (~/.claude/skills) and Codex (~/.agents/skills)
// find them, never in the project.
func (p *bundlePlan) installSkills(homeDir string) error {
	if _, err := writeSkill(homeDir, true); err != nil {
		return err
	}
	for _, skill := range p.skills {
		if err := installSkillFiles(homeDir, foundSkill{Name: skill.Name, Dir: skill.dir}); err != nil {
			return fmt.Errorf("installing %s in the box: %w", skill.Name, err)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// the image

// bundleLayers are installed in this order, so the packages that change
// least come first and their layers stay cached.
var bundleLayers = []string{"base", "heavy", "light"}

// bundleDockerfile is the image for the plan's skills on the box's base
// image. The same skills always give the same bytes, so its hash names the
// image. Nothing in it comes from a skill's files: each key of needs is one
// fixed step.
func (p *bundlePlan) dockerfile(base string) (string, error) {
	env := map[string]string{}
	if p.chrome {
		env["AGENT_BROWSER_EXECUTABLE_PATH"] = "/opt/hi/bin/chrome"
	}
	pipAny, npmAny := false, false
	for _, skill := range p.skills {
		if skill.needs == nil {
			continue
		}
		pipAny = pipAny || len(skill.needs.Pip) > 0
		npmAny = npmAny || len(skill.needs.Npm) > 0
		for key, value := range skill.needs.Env {
			if existing, ok := env[key]; ok && existing != value {
				return "", fmt.Errorf("two skills set %s differently (%q and %q)", key, existing, value)
			}
			env[key] = value
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Written by hi for the skills %s. Don't edit: hi writes it again.\n", strings.Join(p.skillNames(), ", "))
	fmt.Fprintf(&b, "FROM %s\n", base)
	if npmAny {
		env["NODE_PATH"] = "/opt/hi/npm/lib/node_modules"
	}
	for _, key := range sortedKeys(env) {
		fmt.Fprintf(&b, "ENV %s=%s\n", key, shellQuote(env[key]))
	}
	b.WriteString("RUN mkdir -p /opt/hi/bin")
	if pipAny {
		b.WriteString(" && python3 -m venv /opt/hi/venv")
	}
	b.WriteString("\n")
	for _, layer := range bundleLayers {
		var apt, pip, npm []string
		var browsers []string
		for _, skill := range p.skills {
			if skill.needs == nil || firstNonEmpty(skill.needs.Layer, "light") != layer {
				continue
			}
			apt = append(apt, skill.needs.Apt...)
			pip = append(pip, skill.needs.Pip...)
			npm = append(npm, skill.needs.Npm...)
			browsers = append(browsers, skill.needs.Browsers...)
		}
		apt, pip, npm, browsers = sortedUnique(apt), sortedUnique(pip), sortedUnique(npm), sortedUnique(browsers)
		if len(apt)+len(pip)+len(npm)+len(browsers) == 0 {
			continue
		}
		fmt.Fprintf(&b, "# %s\n", layer)
		if len(apt) > 0 {
			fmt.Fprintf(&b, "RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends %s && rm -rf /var/lib/apt/lists/*\n", strings.Join(apt, " "))
		}
		if len(pip) > 0 {
			fmt.Fprintf(&b, "RUN /opt/hi/venv/bin/pip install --no-cache-dir %s\n", quoteAll(pip))
		}
		if len(npm) > 0 {
			fmt.Fprintf(&b, "RUN npm install -g --no-fund --no-audit --prefix /opt/hi/npm %s\n", quoteAll(npm))
		}
		for _, browser := range browsers {
			switch browser {
			case "chromium":
				b.WriteString("RUN PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright /opt/hi/venv/bin/playwright install --with-deps chromium\n")
			case "chrome":
				// agent-browser downloads Chrome for Testing into its home;
				// the box's home is mounted over at run time, so it goes
				// under /opt.
				// Its --with-deps runs sudo apt-get; the build is root
				// already, so a stand-in sudo lives for this step only.
				// Headless Chrome names itself HeadlessChrome and sets
				// navigator.webdriver, which some CDNs refuse (plejd.com's
				// images, for one), so /opt/hi/bin/chrome passes the user
				// agent a normal Chrome of the same version gives and turns
				// the automation flag off.
				b.WriteString(`RUN printf '#!/bin/sh\nexec "$@"\n' > /usr/local/bin/sudo && chmod +x /usr/local/bin/sudo` +
					` && HOME=/opt/hi/agent-browser /opt/hi/npm/bin/agent-browser install --with-deps` +
					` && rm -f /usr/local/bin/sudo && rm -rf /var/lib/apt/lists/*` +
					` && real="$(find /opt/hi/agent-browser -type f -name chrome -perm -u+x | head -n 1)"` +
					` && major="$("$real" --version | grep -o '[0-9][0-9]*' | head -n 1)" && test -n "$major"` +
					` && printf '#!/bin/sh\nexec %s --disable-blink-features=AutomationControlled --user-agent="Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/%s.0.0.0 Safari/537.36" "$@"\n' "$real" "$major" > /opt/hi/bin/chrome` +
					` && chmod +x /opt/hi/bin/chrome && /opt/hi/bin/chrome --version` + "\n")
			}
		}
	}
	b.WriteString("RUN chmod -R a+rX /opt/hi\n")
	return b.String(), nil
}

func sortedUnique(values []string) []string {
	set := map[string]bool{}
	for _, value := range values {
		set[value] = true
	}
	return sortedKeys(set)
}

func quoteAll(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = shellQuote(value)
	}
	return strings.Join(quoted, " ")
}

const bundleImageRepository = "localhost/hi-agent"

// bundleImageUses is when each bundle image was last used, for prune.
func bundleImageUses() string {
	return filepath.Join(filepath.Dir(boxStateFile()), "bundle-images.json")
}

func readBundleImageUses() map[string]time.Time {
	uses := map[string]time.Time{}
	if data, err := os.ReadFile(bundleImageUses()); err == nil {
		json.Unmarshal(data, &uses)
	}
	return uses
}

func recordBundleImageUse(image string) {
	uses := readBundleImageUses()
	uses[image] = time.Now().UTC()
	data, _ := json.MarshalIndent(uses, "", "  ")
	os.MkdirAll(filepath.Dir(bundleImageUses()), 0o700)
	os.WriteFile(bundleImageUses(), append(data, '\n'), 0o600)
}

// ensureImage builds the plan's image unless it exists, and returns it and
// whether it was already there.
func (p *bundlePlan) ensureImage(engine boxEngine, base string, stdout, stderr io.Writer) (string, bool, error) {
	dockerfile, err := p.dockerfile(base)
	if err != nil {
		return "", false, err
	}
	digest := sha256.Sum256([]byte(dockerfile))
	image := bundleImageRepository + ":" + hex.EncodeToString(digest[:])[:12]
	if engine.exists("image", image) {
		recordBundleImageUse(image)
		return image, true, nil
	}
	fmt.Fprintf(stdout, "Building %s for the skills %s (once; later runs use it)...\n", image, strings.Join(p.skillNames(), ", "))
	dir, err := os.MkdirTemp("", "hi-agent-image-")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "Containerfile"), []byte(dockerfile), 0o644); err != nil {
		return "", false, err
	}
	if err := engine.interactive(nil, stdout, stderr, "build", "-t", image, "-f", filepath.Join(dir, "Containerfile"), dir); err != nil {
		return "", false, fmt.Errorf("building the bundle image failed: %w", err)
	}
	recordBundleImageUse(image)
	return image, false, nil
}

// printPlan says what the box gets.
func (p *bundlePlan) printPlan(image string, cached bool, network string, stdout io.Writer) {
	var bundles []string
	for _, bundle := range p.bundles {
		label := bundle.Name
		if bundle.source.name != "builtin" {
			label += " (" + bundle.source.label() + ")"
		}
		bundles = append(bundles, label)
	}
	fmt.Fprintf(stdout, "Bundles: %s\n", strings.Join(bundles, ", "))
	fmt.Fprintf(stdout, "Skills: %s\n", strings.Join(p.skillNames(), ", "))
	state := "built"
	if cached {
		state = "cached"
	}
	fmt.Fprintf(stdout, "Image: %s (%s)\n", image, state)
	line := "Network: " + network
	if len(p.reasons) > 0 {
		line += " (" + strings.Join(p.reasons, "; ") + ")"
	}
	fmt.Fprintln(stdout, line)
	for _, note := range p.notes {
		fmt.Fprintf(stdout, "Note: %s\n", note)
	}
}

// ---------------------------------------------------------------------------
// hi bundle

func runBundle(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printBundleUsage(stdout)
		return 0
	}
	command, rest := args[0], args[1:]
	var err error
	switch command {
	case "ls", "list":
		err = bundleList(rest, stdout, stderr)
	case "show":
		err = bundleShow(rest, stdout, stderr)
	case "prune":
		err = bundlePrune(rest, stdout)
	default:
		err = usageError{"unknown command hi bundle " + command}
	}
	return exitCode(err, stderr)
}

func printBundleUsage(w io.Writer) {
	fmt.Fprintln(w, `hi bundle lists the bundles hi agent --bundle takes: named sets of skills,
with the packages each needs, installed in the box's home folder on an
image built once.

usage:
  hi bundle ls [--json]       bundles and where they come from
  hi bundle show <name>       its skills, what each needs, and its network
  hi bundle prune [--all]     remove bundle images unused for 30 days (--all: every unused one)

Bundles come from hi itself, the team's template sources on a hi server,
and your own folder (HI_BUNDLES_DIR, or ~/.local/share/hi/bundles). The
same name in a later one wins. Use them with hi agent --bundle web,office.`)
}

func bundleList(args []string, stdout, stderr io.Writer) error {
	asJSON := false
	for _, arg := range args {
		switch arg {
		case "--json":
			asJSON = true
		default:
			return usageError{"usage: hi bundle ls [--json]"}
		}
	}
	all, err := loadAllBundles(stderr)
	if err != nil {
		return err
	}
	names := sortedKeys(all)
	if asJSON {
		type row struct {
			Name        string   `json:"name"`
			Description string   `json:"description"`
			Source      string   `json:"source"`
			Skills      []string `json:"skills"`
		}
		rows := []row{}
		for _, name := range names {
			bundle := all[name]
			var skills []string
			for _, skill := range bundle.Skills {
				skills = append(skills, skill.Skill)
			}
			rows = append(rows, row{Name: name, Description: bundle.Description, Source: bundle.source.name, Skills: skills})
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	}
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "BUNDLE\tSKILLS\tFROM\tWHAT")
	for _, name := range names {
		bundle := all[name]
		var skills []string
		for _, skill := range bundle.Skills {
			skills = append(skills, skill.Skill)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", name, truncate(strings.Join(skills, ", "), 40), bundle.source.label(), truncate(bundle.Description, 70))
	}
	table.Flush()
	fmt.Fprintln(stdout, "\nhi bundle show <name> shows one; hi agent --bundle <name> \"<task>\" uses it.")
	return nil
}

func bundleShow(args []string, stdout, stderr io.Writer) error {
	if len(args) != 1 {
		return usageError{"usage: hi bundle show <name>"}
	}
	all, err := loadAllBundles(stderr)
	if err != nil {
		return err
	}
	plan, err := planBundles(args, all)
	if err != nil {
		return err
	}
	bundle := plan.bundles[0]
	fmt.Fprintf(stdout, "%s (%s)\n%s\n", bundle.Name, bundle.source.label(), bundle.Description)
	if bundle.Note != "" {
		fmt.Fprintf(stdout, "Note: %s\n", bundle.Note)
	}
	fmt.Fprintln(stdout)
	for _, skill := range plan.skills {
		from := skill.Source
		if skill.Commit != "" {
			from += " at " + shortCommit(skill.Commit)
		}
		fmt.Fprintf(stdout, "%s  %s\n", skill.Name, from)
		fmt.Fprintf(stdout, "  needs: %s\n", skill.needs.summary())
	}
	fmt.Fprintf(stdout, "hi  the hi skill, from hi %s\n\n", version)
	network := firstNonEmpty(plan.network, "the box's own (dev unless you choose)")
	fmt.Fprintf(stdout, "Network: %s\n", network)
	for _, reason := range plan.reasons {
		fmt.Fprintf(stdout, "  %s\n", reason)
	}
	return nil
}

func bundlePrune(args []string, stdout io.Writer) error {
	all := false
	for _, arg := range args {
		switch arg {
		case "--all":
			all = true
		default:
			return usageError{"usage: hi bundle prune [--all]"}
		}
	}
	engine, _, err := detectBoxEngine()
	if err != nil {
		return err
	}
	out, err := engine.output("images", "--format", "{{.Repository}}:{{.Tag}}")
	if err != nil {
		return err
	}
	inUse := map[string]bool{}
	entries, _ := os.ReadDir(boxStateFile())
	for _, entry := range entries {
		if meta, err := loadBoxMeta(entry.Name()); err == nil {
			inUse[meta.Image] = true
		}
	}
	uses := readBundleImageUses()
	removed := 0
	for _, image := range strings.Fields(out) {
		if !strings.HasPrefix(image, bundleImageRepository+":") || inUse[image] {
			continue
		}
		if last, ok := uses[image]; ok && !all && time.Since(last) < 30*24*time.Hour {
			continue
		}
		if _, err := engine.output("rmi", image); err != nil {
			fmt.Fprintf(stdout, "Kept %s: %v\n", image, err)
			continue
		}
		delete(uses, image)
		removed++
		fmt.Fprintf(stdout, "Removed %s\n", image)
	}
	data, _ := json.MarshalIndent(uses, "", "  ")
	os.WriteFile(bundleImageUses(), append(data, '\n'), 0o600)
	if removed == 0 {
		fmt.Fprintln(stdout, "No bundle images to remove.")
	}
	return nil
}
