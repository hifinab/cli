package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const templateMetadataPath = ".hifin/template.json"

// templateCatalog is every template layer this hi can see, and where each
// source keeps its skills.
type templateCatalog struct {
	layers map[string]*templateLayer
	skills map[string]fs.FS // source name → its skills/ folder
}

// loadTemplateCatalog reads the built-in templates and, for template
// authors, a local source from HI_TEMPLATES_DIR. Server sources come in
// v0.17.0.
func loadTemplateCatalog() (*templateCatalog, error) {
	layers, err := builtinTemplateLayers()
	if err != nil {
		return nil, err
	}
	builtin, err := fs.Sub(builtinTemplateFiles, "templates/skills")
	if err != nil {
		return nil, err
	}
	catalog := &templateCatalog{layers: layers, skills: map[string]fs.FS{"builtin": builtin}}
	if directory := os.Getenv("HI_TEMPLATES_DIR"); directory != "" {
		local := os.DirFS(directory)
		localLayers, err := loadTemplateSource("local", local)
		if err != nil {
			return nil, fmt.Errorf("HI_TEMPLATES_DIR: %w", err)
		}
		for name, layer := range localLayers {
			if _, taken := layers[name]; taken {
				return nil, fmt.Errorf("HI_TEMPLATES_DIR: %q is a built-in template; give the layer another name", name)
			}
			layers[name] = layer
		}
		skills, err := fs.Sub(local, "skills")
		if err != nil {
			return nil, err
		}
		catalog.skills["local"] = skills
	}
	return catalog, nil
}

// choosable returns the templates a person may pick, sorted by name.
func (c *templateCatalog) choosable() []*templateLayer {
	var list []*templateLayer
	for _, layer := range c.layers {
		if !layer.Hidden {
			list = append(list, layer)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })
	return list
}

// tooOld says which hi a layer needs when this one is older.
func (layer *templateLayer) tooOld() string {
	required, ok := strings.CutPrefix(layer.RequiresHi, ">=")
	if !ok {
		return ""
	}
	required = "v" + strings.TrimPrefix(strings.TrimSpace(required), "v")
	if olderRelease(version, required) {
		return required
	}
	return ""
}

// ---------------------------------------------------------------------------
// the plan

type initEntry struct {
	path   string // slash-separated, relative to the target
	data   []byte
	link   string // a symlink target instead of data
	class  string // owned, managed, seeded; empty for metadata
	exists bool   // already there with the same content
}

type initPlan struct {
	template  *composedTemplate
	target    string
	newTarget bool
	entries   []initEntry
	conflicts []string
	commands  [][]string
}

func (p *initPlan) changes() []initEntry {
	var changes []initEntry
	for _, entry := range p.entries {
		if !entry.exists {
			changes = append(changes, entry)
		}
	}
	return changes
}

func planInit(catalog *templateCatalog, templateName, name, directory string, setup bool, github string) (*initPlan, error) {
	composed, err := composeTemplate(catalog.layers, templateName, map[string]string{"name": name})
	if err != nil {
		return nil, err
	}
	for _, layer := range composed.Layers {
		if required := layer.tooOld(); required != "" {
			return nil, fmt.Errorf("the %s template needs hi %s or newer; run `hi update`", layer.Name, required)
		}
	}
	target, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	plan := &initPlan{template: composed, target: target}
	if info, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		plan.newTarget = true
	} else if err != nil {
		return nil, err
	} else if !info.IsDir() {
		return nil, fmt.Errorf("%s exists and is not a directory", target)
	}

	classes := map[string]string{}
	for _, owned := range composed.Owned {
		classes[owned] = "owned"
	}
	for _, managed := range composed.Managed {
		classes[managed] = "managed"
	}
	paths := make([]string, 0, len(composed.Files))
	for file := range composed.Files {
		paths = append(paths, file)
	}
	sort.Strings(paths)
	for _, file := range paths {
		class := classes[file]
		if class == "" {
			class = "seeded"
		}
		plan.entries = append(plan.entries, initEntry{path: file, data: composed.Files[file], class: class})
	}
	skills, skillSources, err := planSkills(catalog, composed)
	if err != nil {
		return nil, err
	}
	plan.entries = append(plan.entries, skills...)

	metadata, err := templateMetadata(composed, plan.entries, skillSources, name)
	if err != nil {
		return nil, err
	}
	plan.entries = append(plan.entries, initEntry{path: templateMetadataPath, data: metadata})

	for i := range plan.entries {
		entry := &plan.entries[i]
		same, conflict, err := compareExisting(filepath.Join(target, filepath.FromSlash(entry.path)), *entry)
		if err != nil {
			return nil, err
		}
		entry.exists = same
		if conflict && entry.path != templateMetadataPath {
			plan.conflicts = append(plan.conflicts, entry.path)
		}
	}

	if !insideGitRepository(target) {
		plan.commands = append(plan.commands, []string{"git", "init", "-q"})
	}
	if setup {
		plan.commands = append(plan.commands, composed.Commands["setup"]...)
	}
	if github != "" {
		plan.commands = append(plan.commands,
			[]string{"git", "add", "-A"},
			[]string{"git", "commit", "-q", "-m", "Start from the hi " + templateName + " template"},
			[]string{"gh", "repo", "create", github, "--private", "--source", ".", "--push"},
		)
	}
	return plan, nil
}

// planSkills copies each skill into .agents/skills and links it for Claude
// Code, which reads only .claude/skills. The links are per skill, as
// `hi skill` writes them.
func planSkills(catalog *templateCatalog, composed *composedTemplate) ([]initEntry, map[string]string, error) {
	var entries []initEntry
	sources := map[string]string{}
	for _, skill := range composed.Skills {
		folder := path.Join(".agents/skills", skill)
		sources[skill] = "builtin"
		if skill == "hi" {
			entries = append(entries, initEntry{path: folder + "/SKILL.md", data: skillContent(), class: "owned"})
		} else {
			files, source := findSkill(catalog, composed, skill)
			if files == nil {
				return nil, nil, fmt.Errorf("the %s template names the skill %q, which no source has", composed.Name, skill)
			}
			err := fs.WalkDir(files, ".", func(name string, entry fs.DirEntry, err error) error {
				if err != nil || entry.IsDir() {
					return err
				}
				data, err := fs.ReadFile(files, name)
				if err != nil {
					return err
				}
				if name == "SKILL.md" {
					data = markSkill(data, source)
				}
				entries = append(entries, initEntry{path: path.Join(folder, name), data: data, class: "owned"})
				return nil
			})
			if err != nil {
				return nil, nil, err
			}
			sources[skill] = source
		}
		entries = append(entries, initEntry{path: path.Join(".claude/skills", skill), link: "../../.agents/skills/" + skill})
	}
	return entries, sources, nil
}

// findSkill looks in the template's own sources, the most specific first.
func findSkill(catalog *templateCatalog, composed *composedTemplate, skill string) (fs.FS, string) {
	for i := len(composed.Layers) - 1; i >= 0; i-- {
		source := composed.Layers[i].source
		if files := catalog.skills[source]; files != nil {
			if sub, err := fs.Sub(files, skill); err == nil {
				if _, err := fs.Stat(sub, "SKILL.md"); err == nil {
					return sub, source
				}
			}
		}
	}
	return nil, ""
}

// markSkill adds the marker that tells `hi skill` and `hi init` the file is
// theirs to replace.
func markSkill(content []byte, source string) []byte {
	marker := fmt.Sprintf("%s init from the %s templates; change it there. -->\n", skillMarker, source)
	_, body, found := bytes.Cut(content, []byte("\n---\n"))
	if !found {
		return append([]byte(marker), content...)
	}
	frontmatter := content[:len(content)-len(body)]
	return append(append(append([]byte{}, frontmatter...), []byte("\n"+marker)...), body...)
}

type templateMetadataFile struct {
	Template    string                            `json:"template"`
	Schema      int                               `json:"schema"`
	GeneratedBy string                            `json:"generatedBy"`
	Layers      []templateMetadataSource          `json:"layers"`
	Params      map[string]string                 `json:"params"`
	Skills      map[string]templateMetadataSource `json:"skills"`
	Files       map[string]templateMetadataHash   `json:"files"`
}

type templateMetadataSource struct {
	Name    string `json:"name,omitempty"`
	Source  string `json:"source"`
	Version string `json:"version,omitempty"`
}

type templateMetadataHash struct {
	Class  string `json:"class"`
	SHA256 string `json:"sha256"`
}

// templateMetadata records what generated the repository. It holds names,
// versions, and hashes only: no paths, users, hosts, or secrets.
func templateMetadata(composed *composedTemplate, entries []initEntry, skillSources map[string]string, name string) ([]byte, error) {
	sourceVersion := func(source string) string {
		if source == "builtin" {
			return "hi " + version
		}
		return ""
	}
	metadata := templateMetadataFile{
		Template:    composed.Name,
		GeneratedBy: "hi " + version,
		Params:      map[string]string{"name": name},
		Skills:      map[string]templateMetadataSource{},
		Files:       map[string]templateMetadataHash{},
	}
	for _, layer := range composed.Layers {
		metadata.Schema = max(metadata.Schema, layer.Schema)
		metadata.Layers = append(metadata.Layers, templateMetadataSource{Name: layer.Name, Source: layer.source, Version: sourceVersion(layer.source)})
	}
	for skill, source := range skillSources {
		metadata.Skills[skill] = templateMetadataSource{Source: source, Version: sourceVersion(source)}
	}
	for _, entry := range entries {
		if entry.link != "" {
			continue
		}
		data := entry.data
		if entry.class == "managed" {
			data = managedBlock(data)
		}
		sum := sha256.Sum256(data)
		metadata.Files[entry.path] = templateMetadataHash{Class: entry.class, SHA256: hex.EncodeToString(sum[:])}
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	return append(data, '\n'), err
}

// managedBlock returns the lines from hi:begin to hi:end, the part hi may
// replace later.
func managedBlock(data []byte) []byte {
	text := string(data)
	start := strings.Index(text, "hi:begin")
	end := strings.Index(text, "hi:end")
	if start < 0 || end < start {
		return data
	}
	start = strings.LastIndex(text[:start], "\n") + 1
	if newline := strings.Index(text[end:], "\n"); newline >= 0 {
		end += newline + 1
	} else {
		end = len(text)
	}
	return []byte(text[start:end])
}

// compareExisting reports whether a planned file is already there as
// planned, or there with other content.
func compareExisting(target string, entry initEntry) (same, conflict bool, err error) {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if entry.link != "" {
		existing, err := os.Readlink(target)
		return err == nil && existing == entry.link, err != nil || existing != entry.link, nil
	}
	if !info.Mode().IsRegular() {
		return false, true, nil
	}
	existing, err := os.ReadFile(target)
	if err != nil {
		return false, false, err
	}
	if bytes.Equal(existing, entry.data) {
		return true, false, nil
	}
	return false, len(existing) > 0, nil
}

func insideGitRepository(target string) bool {
	for directory := target; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
			return true
		}
		if parent := filepath.Dir(directory); parent == directory {
			return false
		}
	}
}

// ---------------------------------------------------------------------------
// the command

func runInit(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hi init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "project name")
	yes := flags.Bool("yes", false, "do not ask for confirmation")
	dryRun := flags.Bool("dry-run", false, "print the plan and stop")
	noSetup := flags.Bool("no-setup", false, "write the files but run no setup commands")
	github := flags.String("github", "", "also create this private GitHub repository and push")
	list := flags.Bool("list", false, "list the templates")
	update := flags.Bool("update", false, "")
	adopt := flags.String("adopt", "", "")
	positional, err := (&flagSet{flags}).parse(args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printInitUsage(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "hi: %v\n\n", err)
		printInitUsage(stderr)
		return 2
	}
	if len(positional) == 1 && positional[0] == "help" {
		printInitUsage(stdout)
		return 0
	}
	if *update || *adopt != "" {
		fmt.Fprintln(stderr, "hi: hi init --update and --adopt are planned for hi v0.18.0")
		return 1
	}
	if len(positional) > 2 {
		printInitUsage(stderr)
		return 2
	}
	if *github != "" && !regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(*github) {
		fmt.Fprintln(stderr, "hi: --github takes owner/repository, for example hifinab/pricing-tools")
		return 2
	}

	catalog, err := loadTemplateCatalog()
	if err != nil {
		return exitCode(err, stderr)
	}
	if *list {
		printTemplateList(catalog, stdout)
		return 0
	}

	request := initRequest{name: *name, setup: !*noSetup, github: *github}
	if len(positional) == 0 {
		ui := newMenuUI(stdin, stdout)
		if err := guidedInit(ui, catalog, &request); err != nil {
			if errors.Is(err, errMenuBack) {
				fmt.Fprintln(stdout, "Cancelled; nothing was written.")
				return 1
			}
			return exitCode(err, stderr)
		}
	} else {
		request.template = positional[0]
		if len(positional) == 2 {
			request.directory = positional[1]
		}
		if err := request.resolve(); err != nil {
			return exitCode(err, stderr)
		}
	}
	return exitCode(executeInit(catalog, request, *yes, *dryRun, stdin, stdout, stderr), stderr)
}

type initRequest struct {
	template, name, directory string
	setup                     bool
	github                    string
}

// resolve fills in the name from the directory, or the directory from the
// name, the same way for the guided and direct forms.
func (r *initRequest) resolve() error {
	if r.directory == "" && r.name == "" {
		return usageError{fmt.Sprintf("give a directory or --name, for example `hi init %s pricing-tools`", r.template)}
	}
	if r.directory == "" {
		r.directory = r.name
	}
	if r.name == "" {
		absolute, err := filepath.Abs(r.directory)
		if err != nil {
			return err
		}
		r.name = strings.ToLower(filepath.Base(absolute))
	}
	return nil
}

func guidedInit(ui menuUI, catalog *templateCatalog, request *initRequest) error {
	templates := catalog.choosable()
	width := 0
	for _, layer := range templates {
		width = max(width, len(layer.Name))
	}
	labels := make([]string, len(templates))
	for i, layer := range templates {
		labels[i] = fmt.Sprintf("%-*s  %s", width, layer.Name, layer.Summary)
		if layer.source != "builtin" {
			labels[i] += "  (" + layer.source + ")"
		}
	}
	choice, err := ui.choose("Template", labels, false)
	if err != nil {
		return errMenuBack
	}
	request.template = templates[choice].Name
	pattern := projectNamePattern(catalog, request.template)
	if request.name == "" {
		request.name, err = ui.input("Project name", "", func(answer string) error {
			if !pattern.MatchString(answer) {
				return errors.New("use lowercase letters, digits, and dashes, starting with a letter")
			}
			return nil
		})
		if err != nil {
			return errMenuBack
		}
	}
	request.directory, err = ui.input("Directory", request.name, nil)
	if err != nil {
		return errMenuBack
	}
	return request.resolve()
}

func projectNamePattern(catalog *templateCatalog, template string) *regexp.Regexp {
	chain, err := layerChain(catalog.layers, template)
	if err == nil {
		for i := len(chain) - 1; i >= 0; i-- {
			if param, ok := chain[i].Params["name"]; ok && param.Pattern != "" {
				if pattern, err := regexp.Compile(param.Pattern); err == nil {
					return pattern
				}
			}
		}
	}
	return regexp.MustCompile(`.+`)
}

func executeInit(catalog *templateCatalog, request initRequest, yes, dryRun bool, stdin io.Reader, stdout, stderr io.Writer) error {
	plan, err := planInit(catalog, request.template, request.name, request.directory, request.setup, request.github)
	if err != nil {
		return err
	}
	printInitPlan(plan, stdout)
	if len(plan.conflicts) > 0 {
		return fmt.Errorf("%d file(s) already exist with other content; nothing was written:\n  %s",
			len(plan.conflicts), strings.Join(plan.conflicts, "\n  "))
	}
	changes := plan.changes()
	if len(changes) == 0 || (len(changes) == 1 && changes[0].path == templateMetadataPath) {
		fmt.Fprintf(stdout, "The %s template is already applied in %s.\n", request.template, plan.target)
		return nil
	}
	if dryRun {
		fmt.Fprintln(stdout, "Dry run; nothing was written.")
		return nil
	}
	if err := confirm(stdin, stdout, yes, "Create it?"); err != nil {
		return err
	}

	if err := writeInitFiles(plan.target, changes); err != nil {
		return err
	}
	for _, args := range plan.commands {
		fmt.Fprintf(stdout, "$ %s\n", strings.Join(args, " "))
		command := exec.Command(args[0], args[1:]...)
		command.Dir = plan.target
		command.Stdout = stdout
		command.Stderr = stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("`%s` failed in %s: %v", strings.Join(args, " "), plan.target, err)
		}
	}

	fmt.Fprintf(stdout, "\nCreated %s from the %s template (%d files).\n", plan.target, request.template, len(changes))
	fmt.Fprintln(stdout, "Next:")
	if relative, err := filepath.Rel(mustGetwd(), plan.target); err == nil && relative != "." {
		fmt.Fprintf(stdout, "  cd %s\n", relative)
	}
	if !request.setup {
		for _, args := range plan.template.Commands["setup"] {
			fmt.Fprintf(stdout, "  %s\n", strings.Join(args, " "))
		}
	}
	for _, args := range plan.template.Commands["check"] {
		fmt.Fprintf(stdout, "  %s\n", strings.Join(args, " "))
	}
	fmt.Fprintln(stdout, "  then start Claude Code or Codex in the folder")
	return nil
}

func printInitPlan(plan *initPlan, stdout io.Writer) {
	var layers []string
	for _, layer := range plan.template.Layers {
		label := layer.Name
		if layer.source == "builtin" {
			label += " (hi " + version + ")"
		} else {
			label += " (" + layer.source + ")"
		}
		layers = append(layers, label)
	}
	where := "existing directory"
	if plan.newTarget {
		where = "new directory"
	}
	fmt.Fprintf(stdout, "Template  %s: %s\n", plan.template.Name, strings.Join(layers, ", "))
	fmt.Fprintf(stdout, "Target    %s (%s)\n", plan.target, where)
	conflicts := map[string]bool{}
	for _, path := range plan.conflicts {
		conflicts[path] = true
	}
	fmt.Fprintln(stdout, "Files")
	for _, entry := range plan.entries {
		state := "write"
		switch {
		case conflicts[entry.path]:
			state = "CONFLICT"
		case entry.exists:
			state = "same"
		}
		name := entry.path
		if entry.link != "" {
			name += " -> " + entry.link
		}
		fmt.Fprintf(stdout, "  %-8s %s\n", state, name)
	}
	if len(plan.commands) > 0 {
		fmt.Fprintln(stdout, "Commands")
		for _, args := range plan.commands {
			fmt.Fprintf(stdout, "  %s\n", strings.Join(args, " "))
		}
	}
}

// writeInitFiles writes each file through a temporary file in its own
// directory, then renames it into place.
func writeInitFiles(target string, entries []initEntry) error {
	for _, entry := range entries {
		destination := filepath.Join(target, filepath.FromSlash(entry.path))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		if entry.link != "" {
			if err := os.Symlink(entry.link, destination); err != nil {
				return err
			}
			continue
		}
		temporary, err := os.CreateTemp(filepath.Dir(destination), ".hi-init-*")
		if err != nil {
			return err
		}
		_, writeErr := temporary.Write(entry.data)
		closeErr := temporary.Close()
		if err := errors.Join(writeErr, closeErr, os.Chmod(temporary.Name(), 0o644)); err != nil {
			os.Remove(temporary.Name())
			return err
		}
		if err := os.Rename(temporary.Name(), destination); err != nil {
			os.Remove(temporary.Name())
			return err
		}
	}
	return nil
}

func printTemplateList(catalog *templateCatalog, stdout io.Writer) {
	table := newTable(stdout)
	fmt.Fprintln(table, "TEMPLATE\tSOURCE\tSUMMARY")
	for _, layer := range catalog.choosable() {
		summary := layer.Summary
		if required := layer.tooOld(); required != "" {
			summary += " (needs hi " + required + ")"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\n", layer.Name, layer.source, summary)
	}
	table.Flush()
}

func mustGetwd() string {
	directory, err := os.Getwd()
	if err != nil {
		return "/"
	}
	return directory
}

func printInitUsage(w io.Writer) {
	fmt.Fprintln(w, `usage:
  hi init                         choose a template, name, and directory
  hi init <template> [directory]  create a project, for example hi init python pricing-tools
  hi init --list                  list the templates

options:
  --name <name>          project name (default: the directory's name)
  --yes                  do not ask for confirmation (for agents and CI)
  --dry-run              print the plan and stop
  --no-setup             write the files but run no setup commands
  --github <owner/repo>  also create a private GitHub repository and push`)
}
