package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// hi init --update brings a repository up to the current templates, and
// --adopt brings an existing repository under one
// (docs/specs/approved/hi_init.md).

// Actions on a file when updating or adopting.
const (
	actionSame     = "same"     // already as the template has it
	actionWrite    = "write"    // write the whole file
	actionBlock    = "block"    // replace or add the managed block only
	actionDelete   = "delete"   // an unedited owned file the template dropped
	actionNote     = "note"     // a seeded file the template changed: see docs/upgrades
	actionConflict = "conflict" // edited by hand; left alone
	actionSkip     = "skip"     // a starting file adopt doesn't add
)

type fileAction struct {
	entry  initEntry
	action string
	data   []byte // what to write, for write and block
	reason string
}

type templateChange struct {
	template  *composedTemplate
	target    string
	metadata  templateMetadataFile // what the repository recorded
	actions   []fileAction
	unchecked []string // sources a check without them could not cover
	notes     []byte   // docs/upgrades content, if any
	notesPath string
	record    []byte // the new .hifin/template.json
	commands  [][]string
}

func (c *templateChange) pending() []fileAction {
	var pending []fileAction
	for _, action := range c.actions {
		if action.action != actionSame && action.action != actionSkip {
			pending = append(pending, action)
		}
	}
	return pending
}

func (c *templateChange) conflicts() []string {
	var conflicts []string
	for _, action := range c.actions {
		if action.action == actionConflict {
			conflicts = append(conflicts, action.entry.path)
		}
	}
	return conflicts
}

func readTemplateMetadata(target string) (templateMetadataFile, error) {
	var metadata templateMetadataFile
	data, err := os.ReadFile(filepath.Join(target, templateMetadataPath))
	if errors.Is(err, os.ErrNotExist) {
		return metadata, fmt.Errorf("%s has no %s; start it with `hi init <template> .` or `hi init --adopt <template>`", target, templateMetadataPath)
	}
	if err != nil {
		return metadata, err
	}
	if err := json.Unmarshal(data, &metadata); err != nil || metadata.Template == "" || metadata.Params["name"] == "" {
		return metadata, fmt.Errorf("%s is not valid; restore it from Git", templateMetadataPath)
	}
	return metadata, nil
}

// availableLayers reports whether the catalog has every non-built-in layer
// the repository was made from, and which sources are missing.
func availableLayers(catalog *templateCatalog, metadata templateMetadataFile) []string {
	missing := map[string]bool{}
	for _, recorded := range metadata.Layers {
		if recorded.Source == "builtin" {
			continue
		}
		found := false
		for _, layer := range catalog.layers {
			if layer.Name == recorded.Name && layer.source == recorded.Source {
				found = true
			}
		}
		if !found {
			missing[recorded.Source] = true
		}
	}
	var sources []string
	for source := range missing {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	return sources
}

// planUpdate compares the repository with the current templates. Without a
// template's private layers, check compares only what came from the others.
func planUpdate(catalog *templateCatalog, target string, check, strict, force bool) (*templateChange, error) {
	metadata, err := readTemplateMetadata(target)
	if err != nil {
		return nil, err
	}
	for _, layer := range metadata.Layers {
		recorded, isHi := strings.CutPrefix(layer.Version, "hi ")
		if layer.Source == "builtin" && isHi && olderRelease(version, recorded) {
			return nil, fmt.Errorf("this repository was made with hi %s, newer than this hi %s; run `hi update`", recorded, version)
		}
	}
	change := &templateChange{target: target, metadata: metadata}
	templateName := metadata.Template
	missing := availableLayers(catalog, metadata)
	if len(missing) > 0 {
		if !check || strict {
			return nil, fmt.Errorf("this repository uses layers from %s, which this device can't reach; "+
				"check `hi connect status` and try again", strings.Join(missing, ", "))
		}
		// Check what came from the built-in layers only.
		change.unchecked = missing
		templateName = ""
		for _, layer := range metadata.Layers {
			if layer.Source == "builtin" {
				templateName = layer.Name
			}
		}
		if templateName == "" {
			return change, nil
		}
	}
	composed, err := composeTemplateAllowingHidden(catalog.layers, templateName, metadata.Params)
	if err != nil {
		return nil, err
	}
	change.template = composed
	for _, layer := range composed.Layers {
		if required := layer.tooOld(); required != "" {
			return nil, fmt.Errorf("the %s template needs hi %s or newer; run `hi update`", layer.Name, required)
		}
		if layer.Schema > metadata.Schema && metadata.Schema > 0 {
			return nil, fmt.Errorf("the %s template changed its layout (schema %d to %d), which needs a migration hi doesn't have yet",
				layer.Name, metadata.Schema, layer.Schema)
		}
	}
	entries, skillSources, err := templateEntries(catalog, composed)
	if err != nil {
		return nil, err
	}

	keep := map[string]templateMetadataHash{}
	present := map[string]bool{}
	var notes []string
	for _, entry := range entries {
		present[entry.path] = true
		recorded, wasRecorded := metadata.Files[entry.path]
		if len(change.unchecked) > 0 && (entry.source != "builtin" || recorded.Source != "builtin") {
			continue
		}
		action, err := updateAction(target, entry, recorded, wasRecorded, force)
		if err != nil {
			return nil, err
		}
		if action.action == actionConflict && wasRecorded {
			keep[entry.path] = recorded
		}
		if action.action == actionNote {
			notes = append(notes, upgradeNote(target, entry, wasRecorded))
		}
		change.actions = append(change.actions, action)
	}
	if len(change.unchecked) == 0 {
		var dropped []string
		for file := range metadata.Files {
			if !present[file] {
				dropped = append(dropped, file)
			}
		}
		sort.Strings(dropped)
		for _, file := range dropped {
			action := droppedAction(target, file, metadata.Files[file])
			if action.action == actionNote {
				notes = append(notes, fmt.Sprintf("### `%s`\n\nThe template no longer includes this file. Delete it if the project doesn't use it.\n", file))
			}
			change.actions = append(change.actions, action)
		}
		for skill := range metadata.Skills {
			if _, still := skillSources[skill]; !still {
				link := path.Join(".claude/skills", skill)
				if existing, err := os.Readlink(filepath.Join(target, filepath.FromSlash(link))); err == nil && existing == "../../.agents/skills/"+skill {
					change.actions = append(change.actions, fileAction{entry: initEntry{path: link, link: existing}, action: actionDelete})
				}
			}
		}
	}
	if len(notes) > 0 {
		change.notesPath = upgradeNotesPath(target, composed)
		change.notes = []byte(upgradeNotes(composed, notes))
	}
	change.record, err = templateMetadata(composed, entries, skillSources, metadata.Params["name"], keep)
	if err != nil {
		return nil, err
	}
	return change, nil
}

// composeTemplateAllowingHidden composes a template, or for a check without
// the private layers, the built-in layers it was built on.
func composeTemplateAllowingHidden(layers map[string]*templateLayer, name string, params map[string]string) (*composedTemplate, error) {
	if layer, ok := layers[name]; ok && layer.Hidden {
		copy := *layer
		copy.Hidden = false
		withVisible := map[string]*templateLayer{}
		for key, value := range layers {
			withVisible[key] = value
		}
		withVisible[name] = &copy
		return composeTemplate(withVisible, name, params)
	}
	return composeTemplate(layers, name, params)
}

func updateAction(target string, entry initEntry, recorded templateMetadataHash, wasRecorded, force bool) (fileAction, error) {
	full := filepath.Join(target, filepath.FromSlash(entry.path))
	if entry.link != "" {
		existing, err := os.Readlink(full)
		switch {
		case err == nil && existing == entry.link:
			return fileAction{entry: entry, action: actionSame}, nil
		case errors.Is(err, os.ErrNotExist):
			if _, statErr := os.Lstat(full); errors.Is(statErr, os.ErrNotExist) {
				return fileAction{entry: entry, action: actionWrite}, nil
			}
		}
		if force {
			return fileAction{entry: entry, action: actionWrite, reason: "replacing what was there"}, nil
		}
		return fileAction{entry: entry, action: actionConflict, reason: "something else is there"}, nil
	}
	disk, err := os.ReadFile(full)
	exists := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fileAction{}, err
	}
	switch entry.class {
	case "owned":
		written := strings.HasPrefix(entry.path, ".agents/skills/") && bytes.Contains(disk, []byte(skillMarker))
		switch {
		case !exists:
			return fileAction{entry: entry, action: actionWrite, data: entry.data}, nil
		case bytes.Equal(disk, entry.data):
			return fileAction{entry: entry, action: actionSame}, nil
		case wasRecorded && sha256Hex(disk) == recorded.SHA256, force, written:
			return fileAction{entry: entry, action: actionWrite, data: entry.data}, nil
		}
		return fileAction{entry: entry, action: actionConflict, reason: "edited by hand"}, nil
	case "managed":
		newBlock := managedBlock(entry.data)
		if !exists {
			return fileAction{entry: entry, action: actionWrite, data: entry.data}, nil
		}
		block, ok := findManagedBlock(disk)
		switch {
		case ok && bytes.Equal(block, newBlock):
			return fileAction{entry: entry, action: actionSame}, nil
		case ok && (force || wasRecorded && sha256Hex(block) == recorded.SHA256):
			return fileAction{entry: entry, action: actionBlock, data: replaceManagedBlock(disk, newBlock)}, nil
		case !ok && force:
			return fileAction{entry: entry, action: actionBlock, data: append(append(append([]byte{}, newBlock...), '\n'), disk...)}, nil
		case !ok:
			return fileAction{entry: entry, action: actionConflict, reason: "its hi block is missing"}, nil
		}
		return fileAction{entry: entry, action: actionConflict, reason: "its hi block was edited by hand"}, nil
	default: // seeded: the project's own; report what changed
		if wasRecorded && sha256Hex(entry.data) == recorded.SHA256 {
			return fileAction{entry: entry, action: actionSame}, nil
		}
		if exists && bytes.Equal(disk, entry.data) {
			return fileAction{entry: entry, action: actionSame}, nil
		}
		return fileAction{entry: entry, action: actionNote}, nil
	}
}

func droppedAction(target, file string, recorded templateMetadataHash) fileAction {
	entry := initEntry{path: file, class: recorded.Class}
	disk, err := os.ReadFile(filepath.Join(target, filepath.FromSlash(file)))
	if err != nil {
		return fileAction{entry: entry, action: actionSame}
	}
	if recorded.Class == "owned" && sha256Hex(disk) == recorded.SHA256 {
		return fileAction{entry: entry, action: actionDelete}
	}
	return fileAction{entry: entry, action: actionNote, reason: "no longer in the template"}
}

func findManagedBlock(data []byte) ([]byte, bool) {
	text := string(data)
	if !strings.Contains(text, "hi:begin") || !strings.Contains(text, "hi:end") {
		return nil, false
	}
	return managedBlock(data), true
}

func replaceManagedBlock(data, block []byte) []byte {
	old := managedBlock(data)
	return bytes.Replace(data, old, block, 1)
}

// upgradeNote describes one seeded file the template changed, for an agent
// to apply in a pull request.
func upgradeNote(target string, entry initEntry, wasRecorded bool) string {
	full := filepath.Join(target, filepath.FromSlash(entry.path))
	var note strings.Builder
	fmt.Fprintf(&note, "### `%s`\n\n", entry.path)
	if _, err := os.Stat(full); err != nil {
		if wasRecorded {
			note.WriteString("The project deleted this file; the template still has it. Restore it only if it is useful:\n\n")
		} else {
			note.WriteString("The template now starts with this file. Add it if it fits the project:\n\n")
		}
		fmt.Fprintf(&note, "````\n%s````\n", ensureNewline(string(entry.data)))
		return note.String()
	}
	note.WriteString("The template's version changed. This diff turns the project's file into the template's; " +
		"apply only the template's improvements and keep the project's own changes:\n\n")
	fmt.Fprintf(&note, "````diff\n%s````\n", ensureNewline(unifiedDiff(full, entry.path, entry.data)))
	return note.String()
}

func ensureNewline(text string) string {
	if text != "" && !strings.HasSuffix(text, "\n") {
		return text + "\n"
	}
	return text
}

// unifiedDiff runs diff -u from the project's file to the template's.
func unifiedDiff(projectFile, name string, template []byte) string {
	temporary, err := os.CreateTemp("", "hi-template-*")
	if err != nil {
		return string(template)
	}
	defer os.Remove(temporary.Name())
	temporary.Write(template)
	temporary.Close()
	output, err := exec.Command("diff", "-u", "--label", "project/"+name, "--label", "template/"+name, projectFile, temporary.Name()).Output()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return string(template)
	}
	return string(output)
}

func upgradeNotesPath(target string, composed *composedTemplate) string {
	name := "hi-" + version
	for _, layer := range composed.Layers {
		if layer.commit != "" {
			name += "-" + layer.source + "-" + shortCommit(layer.commit)
			break
		}
	}
	file := path.Join("docs/upgrades", name+".md")
	for i := 2; ; i++ {
		if _, err := os.Stat(filepath.Join(target, filepath.FromSlash(file))); errors.Is(err, os.ErrNotExist) {
			return file
		}
		file = path.Join("docs/upgrades", fmt.Sprintf("%s-%d.md", name, i))
	}
}

func upgradeNotes(composed *composedTemplate, notes []string) string {
	var layers []string
	for _, layer := range composed.Layers {
		layers = append(layers, layer.Name+" ("+layer.label()+")")
	}
	return fmt.Sprintf("# Template upgrade: %s\n\n"+
		"`hi init --update` brought this repository to %s. It updated the files hi owns, "+
		"and left the files below to the project, because they were the project's from the start.\n\n"+
		"For an agent: apply what still fits in a pull request, run `make check`, then delete this file.\n\n%s",
		composed.Name, strings.Join(layers, ", "), strings.Join(notes, "\n"))
}

// ---------------------------------------------------------------------------
// adopting

var makeTargetPattern = regexp.MustCompile(`(?m)^(help|check|fix|lint|test)\s*:`)

// planAdopt adds a template's owned and managed files to an existing
// repository. It never touches the project's own files.
func planAdopt(catalog *templateCatalog, templateName, name, target string, force bool) (*templateChange, error) {
	if _, err := os.Stat(filepath.Join(target, templateMetadataPath)); err == nil {
		return nil, fmt.Errorf("%s is already under a template; use `hi init --update`", target)
	}
	if info, err := os.Stat(target); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not an existing directory; use `hi init %s %s` for a new project", target, templateName, target)
	}
	composed, err := composeTemplate(catalog.layers, templateName, map[string]string{"name": name})
	if err != nil {
		return nil, err
	}
	for _, layer := range composed.Layers {
		if required := layer.tooOld(); required != "" {
			return nil, fmt.Errorf("the %s template needs hi %s or newer; run `hi update`", layer.Name, required)
		}
	}
	entries, skillSources, err := templateEntries(catalog, composed)
	if err != nil {
		return nil, err
	}
	change := &templateChange{template: composed, target: target}
	for _, entry := range entries {
		full := filepath.Join(target, filepath.FromSlash(entry.path))
		if entry.class == "seeded" {
			if _, err := os.Stat(full); err != nil {
				change.actions = append(change.actions, fileAction{entry: entry, action: actionSkip, reason: "the template starts with it; this repository doesn't have it"})
			}
			continue
		}
		if entry.class == "managed" {
			disk, err := os.ReadFile(full)
			if err == nil {
				if _, ok := findManagedBlock(disk); !ok && path.Base(entry.path) == "Makefile" {
					if clashes := makeTargetPattern.FindAllStringSubmatch(string(disk), -1); len(clashes) > 0 && !force {
						var names []string
						for _, clash := range clashes {
							names = append(names, clash[1])
						}
						change.actions = append(change.actions, fileAction{entry: entry, action: actionConflict,
							reason: "it already has " + strings.Join(uniqueStrings(names), ", ") + " targets"})
						continue
					}
				}
			}
		}
		action, err := updateAction(target, entry, templateMetadataHash{}, false, force)
		if err != nil {
			return nil, err
		}
		if action.action == actionConflict && entry.class == "managed" && action.reason == "its hi block is missing" {
			// Adopting adds the block at the top and keeps the rest.
			disk, _ := os.ReadFile(full)
			action = fileAction{entry: entry, action: actionBlock, data: append(append(managedBlock(entry.data), '\n'), disk...)}
		}
		change.actions = append(change.actions, action)
	}
	if !insideGitRepository(target) {
		change.commands = append(change.commands, []string{"git", "init", "-q"})
	}
	change.record, err = templateMetadata(composed, entries, skillSources, name, nil)
	return change, err
}

// ---------------------------------------------------------------------------
// output and applying

func printTemplateChange(change *templateChange, verb string, stdout io.Writer) {
	if change.template != nil {
		var layers []string
		for _, layer := range change.template.Layers {
			layers = append(layers, layer.Name+" ("+layer.label()+")")
		}
		fmt.Fprintf(stdout, "Template  %s: %s\n", change.template.Name, strings.Join(layers, ", "))
	}
	fmt.Fprintf(stdout, "Target    %s\n", change.target)
	labels := map[string]string{actionWrite: "write", actionBlock: "block", actionDelete: "delete",
		actionNote: "note", actionConflict: "CONFLICT", actionSkip: "missing"}
	shown := false
	for _, action := range change.actions {
		label, ok := labels[action.action]
		if !ok {
			continue
		}
		if !shown {
			fmt.Fprintln(stdout, "Files")
			shown = true
		}
		line := fmt.Sprintf("  %-8s %s", label, action.entry.path)
		if action.entry.link != "" {
			line += " -> " + action.entry.link
		}
		if action.reason != "" {
			line += " (" + action.reason + ")"
		}
		fmt.Fprintln(stdout, line)
	}
	if change.notesPath != "" {
		fmt.Fprintf(stdout, "  %-8s %s (what changed in the project's own files)\n", "write", change.notesPath)
	}
	for _, args := range change.commands {
		fmt.Fprintf(stdout, "Command   %s\n", strings.Join(args, " "))
	}
	if len(change.unchecked) > 0 {
		fmt.Fprintf(stdout, "Not checked: the layers from %s (no connection to its server)\n", strings.Join(change.unchecked, ", "))
	}
	if !shown && change.notesPath == "" {
		fmt.Fprintf(stdout, "Nothing to %s; the repository matches the templates.\n", verb)
	}
}

func applyTemplateChange(change *templateChange, stdout, stderr io.Writer) error {
	var writes []initEntry
	for _, action := range change.pending() {
		switch action.action {
		case actionWrite, actionBlock:
			entry := action.entry
			if entry.link == "" {
				entry.data = action.data
			}
			full := filepath.Join(change.target, filepath.FromSlash(entry.path))
			if entry.link != "" {
				os.Remove(full)
			}
			writes = append(writes, entry)
		case actionDelete:
			if err := os.Remove(filepath.Join(change.target, filepath.FromSlash(action.entry.path))); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if change.notesPath != "" {
		writes = append(writes, initEntry{path: change.notesPath, data: change.notes})
	}
	writes = append(writes, initEntry{path: templateMetadataPath, data: change.record})
	if err := writeInitFiles(change.target, writes); err != nil {
		return err
	}
	for _, args := range change.commands {
		fmt.Fprintf(stdout, "$ %s\n", strings.Join(args, " "))
		command := exec.Command(args[0], args[1:]...)
		command.Dir, command.Stdout, command.Stderr = change.target, stdout, stderr
		if err := command.Run(); err != nil {
			return fmt.Errorf("`%s` failed in %s: %v", strings.Join(args, " "), change.target, err)
		}
	}
	return nil
}

func executeUpdate(catalog *templateCatalog, directory string, check, strict, force, yes, dryRun bool, stdin io.Reader, stdout, stderr io.Writer) error {
	target, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	change, err := planUpdate(catalog, target, check, strict, force)
	if err != nil {
		return err
	}
	printTemplateChange(change, "update", stdout)
	pending := change.pending()
	if check {
		if len(pending) > 0 || change.notesPath != "" {
			return exitStatusError{code: 1, message: fmt.Sprintf("the repository is behind the templates or has conflicts (%d files); run `hi init --update`", len(pending))}
		}
		return nil
	}
	if len(pending) == 0 && change.notesPath == "" {
		// Record the new versions even when no file changed.
		current, _ := os.ReadFile(filepath.Join(target, templateMetadataPath))
		if !bytes.Equal(current, change.record) && !dryRun {
			return writeInitFiles(target, []initEntry{{path: templateMetadataPath, data: change.record}})
		}
		return nil
	}
	if dryRun {
		fmt.Fprintln(stdout, "Dry run; nothing was written.")
		return nil
	}
	if err := confirm(stdin, stdout, yes, "Update it?"); err != nil {
		return err
	}
	if err := applyTemplateChange(change, stdout, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\nUpdated %s.\n", target)
	if change.notesPath != "" {
		fmt.Fprintf(stdout, "The template also changed files that belong to the project; %s says how. Ask an agent to apply it.\n", change.notesPath)
	}
	fmt.Fprintln(stdout, "Next: review with `git diff`, then run `make check`.")
	if conflicts := change.conflicts(); len(conflicts) > 0 {
		return fmt.Errorf("%d file(s) were edited by hand and left alone: %s; make the change in the template instead, "+
			"or rerun with --force to overwrite them", len(conflicts), strings.Join(conflicts, ", "))
	}
	return nil
}

func executeAdopt(catalog *templateCatalog, templateName, name, directory string, force, yes, dryRun bool, stdin io.Reader, stdout, stderr io.Writer) error {
	target, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	if name == "" {
		name = strings.ToLower(filepath.Base(target))
	}
	if pattern := projectNamePattern(catalog, templateName); !pattern.MatchString(name) {
		return usageError{fmt.Sprintf("%q can't be a project name; pass --name with lowercase letters, digits, and dashes", name)}
	}
	change, err := planAdopt(catalog, templateName, name, target, force)
	if err != nil {
		return err
	}
	printTemplateChange(change, "adopt", stdout)
	if conflicts := change.conflicts(); len(conflicts) > 0 {
		return fmt.Errorf("%d file(s) conflict with the template; nothing was written: %s; move them aside, or rerun with --force",
			len(conflicts), strings.Join(conflicts, ", "))
	}
	if dryRun {
		fmt.Fprintln(stdout, "Dry run; nothing was written.")
		return nil
	}
	if err := confirm(stdin, stdout, yes, "Adopt it?"); err != nil {
		return err
	}
	if err := applyTemplateChange(change, stdout, stderr); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "\n%s is now under the %s template.\n", target, templateName)
	var missing []string
	for _, action := range change.actions {
		if action.action == actionSkip {
			missing = append(missing, action.entry.path)
		}
	}
	if len(missing) > 0 {
		fmt.Fprintf(stdout, "The template also starts with files this repository doesn't have, so `make check` may not pass yet:\n  %s\n",
			strings.Join(missing, "\n  "))
	}
	fmt.Fprintln(stdout, "Next: run `make check`, fix what fails (or ask an agent to), and commit.")
	return nil
}
