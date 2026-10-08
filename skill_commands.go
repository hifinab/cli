package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// hi skill find, add, ls, show, update, and rm. See
// docs/specs/approved/hi_skills_sh.md.

type skillOptions struct {
	global, yes, acceptRisk, force, check, json bool
	bundles                                     bool
	skills                                      []string
	ref                                         string
	limit                                       int
	words                                       []string
}

func parseSkillOptions(args []string) (skillOptions, error) {
	options := skillOptions{limit: 10}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func() (string, error) {
			if _, after, ok := strings.Cut(arg, "="); ok {
				return after, nil
			}
			if i+1 >= len(args) {
				return "", usageError{arg + " needs a value"}
			}
			i++
			return args[i], nil
		}
		var err error
		var text string
		switch {
		case arg == "--global" || arg == "-g":
			options.global = true
		case arg == "--yes" || arg == "-y":
			options.yes = true
		case arg == "--accept-risk":
			options.acceptRisk = true
		case arg == "--force" || arg == "-f":
			options.force = true
		case arg == "--check":
			options.check = true
		case arg == "--bundles":
			options.bundles = true
		case arg == "--json":
			options.json = true
		case arg == "--skill" || arg == "-s" || strings.HasPrefix(arg, "--skill="):
			text, err = value()
			for _, name := range strings.Split(text, ",") {
				if name = strings.TrimSpace(name); name != "" {
					options.skills = append(options.skills, name)
				}
			}
		case arg == "--ref" || strings.HasPrefix(arg, "--ref="):
			options.ref, err = value()
		case arg == "--limit" || strings.HasPrefix(arg, "--limit="):
			text, err = value()
			if err == nil {
				if options.limit, err = strconv.Atoi(text); err != nil || options.limit < 1 || options.limit > 200 {
					err = usageError{"--limit is a number from 1 to 200"}
				}
			}
		case strings.HasPrefix(arg, "-") && arg != "-":
			return options, usageError{"unknown option " + arg}
		default:
			options.words = append(options.words, arg)
		}
		if err != nil {
			return options, err
		}
	}
	return options, nil
}

func printSkillUsage(w io.Writer) {
	fmt.Fprintln(w, `hi skill installs agent skills: from skills.sh and any git repository,
at a recorded commit, into .agents/skills (Codex and others), linked from
.claude/skills (Claude Code).

usage:
  hi skill                         in a terminal: search, browse, and pick skills;
                                   otherwise write or update the hi skill here
  hi skill find <query>            search skills.sh
  hi skill add <source>            install skills from owner/repo, owner/repo/skill,
                                   a repository URL, or a folder; hi is the hi skill
  hi skill ls                      installed skills, their source and commit
  hi skill show <name|source>      a skill's description, license, files, and audits
  hi skill update [name...]        move skills to their source's newest commit
  hi skill rm <name>...            remove skills

options:
  --global, -g     in your home folder, for every project
  --skill a,b      which skills to add from a source with several
  --yes, -y        don't ask; add every skill a source has
  --accept-risk    add or update a skill a partner rates high or critical
  --check          update: only list what would change; exit 1 if anything would
  --bundles        update: move the skills in bundles/<name>.json instead, in
                   the folder that has them
  --ref <ref>      add: a branch or tag instead of the default branch
  --force          replace skills hi didn't install, or local changes
  --json           find, ls: JSON on stdout

hi skills is the same command. Searches and audits come from skills.sh;
DO_NOT_TRACK=1 turns them off. hi sends skills.sh no install events.`)
}

func skillCommand(command string, options skillOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	if options.bundles && command != "update" && command != "upgrade" {
		return usageError{"--bundles goes with hi skill update"}
	}
	switch command {
	case "find", "search":
		return skillFind(options, stdin, stdout, stderr)
	case "add", "install":
		return skillAdd(options, stdin, stdout)
	case "ls", "list":
		return skillList(options, stdout)
	case "show":
		return skillShow(options, stdout)
	case "update", "upgrade":
		return skillUpdate(options, stdin, stdout)
	case "rm", "remove":
		return skillRemove(options, stdin, stdout)
	}
	return usageError{"unknown command hi skill " + command}
}

// ---------------------------------------------------------------------------
// find

func skillFind(options skillOptions, stdin io.Reader, stdout, stderr io.Writer) error {
	query := strings.TrimSpace(strings.Join(options.words, " "))
	if query == "" {
		if interactive(stdin, stdout) {
			return runSkillSelector(options.global, stdin, stdout, stderr)
		}
		return usageError{"usage: hi skill find <query>"}
	}
	hits, err := searchSkillsSh(query, options.limit)
	if err != nil {
		return err
	}
	bySource := map[string][]string{}
	for _, hit := range hits {
		bySource[hit.Source] = append(bySource[hit.Source], hit.SkillID)
	}
	audits := fetchSkillAuditsBySource(bySource)
	if options.json {
		type jsonHit struct {
			skillHit
			Verified bool                  `json:"verified"`
			Install  string                `json:"install"`
			Audits   map[string]skillAudit `json:"audits"`
		}
		out := []jsonHit{}
		for _, hit := range hits {
			out = append(out, jsonHit{hit, hit.verified(), hit.Source + "/" + hit.SkillID, audits[hit.Source][hit.SkillID]})
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(out)
	}
	if len(hits) == 0 {
		fmt.Fprintf(stdout, "No skills on skills.sh match %q.\n", query)
		return nil
	}
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "SKILL\tSOURCE\tINSTALLS\tAUDITS")
	for _, hit := range hits {
		source := hit.Source
		if hit.verified() {
			source += " ✓"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", hit.SkillID, source, formatInstalls(hit.Installs), skillAuditShort(audits[hit.Source][hit.SkillID]))
	}
	table.Flush()
	fmt.Fprintf(stdout, "\n✓ the tool's maker. Install with hi skill add <source>/<skill>, for example hi skill add %s/%s\n", hits[0].Source, hits[0].SkillID)
	return nil
}

func formatInstalls(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%dk", (n+500)/1000)
	}
	return fmt.Sprint(n)
}

// ---------------------------------------------------------------------------
// add

// preparedSource is a source fetched at a commit, with its skills.
type preparedSource struct {
	source  skillSource
	root    string
	commit  string
	date    time.Time
	skills  []foundSkill
	cleanup func()
}

func prepareSkillSource(source skillSource) (*preparedSource, error) {
	prepared := &preparedSource{source: source, cleanup: func() {}}
	switch source.Kind {
	case "local":
		prepared.root = source.Source
	case "github", "git":
		commit, err := resolveSkillCommit(source)
		if err != nil {
			return nil, err
		}
		root, cleanup, err := fetchSkillCommits(source, commit)
		if err != nil {
			return nil, err
		}
		prepared.root, prepared.commit, prepared.cleanup = root, commit, cleanup
		prepared.date = skillCommitDate(root)
	default:
		return nil, fmt.Errorf("can't fetch skills from %s", source.Source)
	}
	skills, err := discoverSkills(prepared.root, source.Path)
	if err != nil {
		prepared.cleanup()
		return nil, err
	}
	if len(skills) == 0 {
		prepared.cleanup()
		return nil, fmt.Errorf("%s has no skills (no SKILL.md)", source.Source)
	}
	prepared.skills = skills
	return prepared, nil
}

// chooseSkills picks the skills to add from a source with several.
func chooseSkills(prepared *preparedSource, options skillOptions, stdin io.Reader, stdout io.Writer) ([]foundSkill, error) {
	byName := map[string]foundSkill{}
	var names []string
	for _, skill := range prepared.skills {
		byName[skill.Name] = skill
		byName[filepath.Base(skill.Dir)] = skill
		names = append(names, skill.Name)
	}
	wanted := options.skills
	if prepared.source.Skill != "" {
		wanted = append([]string{prepared.source.Skill}, wanted...)
	}
	if len(wanted) > 0 {
		var chosen []foundSkill
		seen := map[string]bool{}
		for _, name := range wanted {
			skill, ok := byName[name]
			if !ok {
				return nil, fmt.Errorf("%s has no skill %s; it has %s", prepared.source.Source, name, strings.Join(names, ", "))
			}
			if !seen[skill.Name] {
				seen[skill.Name] = true
				chosen = append(chosen, skill)
			}
		}
		return chosen, nil
	}
	if len(prepared.skills) == 1 || options.yes {
		return prepared.skills, nil
	}
	if !isTerminal(stdin) {
		return nil, usageError{fmt.Sprintf("%s has %d skills: %s; choose with --skill a,b, or --yes for all of them",
			prepared.source.Source, len(names), strings.Join(names, ", "))}
	}
	fmt.Fprintf(stdout, "%s has %d skills:\n", prepared.source.Source, len(prepared.skills))
	for i, skill := range prepared.skills {
		fmt.Fprintf(stdout, "  %2d  %-24s %s\n", i+1, skill.Name, clipText(skill.Description, 60))
	}
	fmt.Fprint(stdout, "Which? Numbers or names separated by spaces, or all: ")
	answer := readSkillAnswer(stdin)
	if answer == "all" {
		return prepared.skills, nil
	}
	var chosen []foundSkill
	for _, word := range strings.FieldsFunc(answer, func(r rune) bool { return r == ' ' || r == ',' }) {
		if n, err := strconv.Atoi(word); err == nil && n >= 1 && n <= len(prepared.skills) {
			chosen = append(chosen, prepared.skills[n-1])
		} else if skill, ok := byName[word]; ok {
			chosen = append(chosen, skill)
		} else {
			return nil, fmt.Errorf("%s is not one of the skills", word)
		}
	}
	if len(chosen) == 0 {
		return nil, errors.New("cancelled")
	}
	return chosen, nil
}

func clipText(text string, width int) string {
	text = strings.Join(strings.Fields(text), " ")
	if len([]rune(text)) <= width {
		return text
	}
	return string([]rune(text)[:width-1]) + "…"
}

// printSkillBlock describes a skill before it's installed or updated.
func printSkillBlock(w io.Writer, skill foundSkill, from string, commit string, date time.Time, audits map[string]skillAudit, auditNote string) {
	at := ""
	if commit != "" {
		at = " at " + shortCommit(commit)
		if !date.IsZero() {
			at += " (" + date.Format("2006-01-02") + ")"
		}
	}
	fmt.Fprintf(w, "%s from %s%s\n", skill.Name, from, at)
	if skill.Description != "" {
		fmt.Fprintf(w, "  %s\n", clipText(skill.Description, 160))
	}
	fmt.Fprintf(w, "  License: %s · %s\n", firstNonEmpty(skill.License, "not stated"), skillFiles(skill.Dir))
	if auditNote != "" {
		fmt.Fprintf(w, "  Audits: %s\n", auditNote)
	} else {
		fmt.Fprintf(w, "  Audits: %s\n", skillAuditSummary(audits))
	}
	if skill.AllowedTools != "" {
		fmt.Fprintf(w, "  Allowed tools: %s\n", skill.AllowedTools)
	}
	if mentions := skillMentions(skill.Dir); len(mentions) > 0 {
		fmt.Fprintf(w, "  Mentions: %s\n", strings.Join(mentions, ", "))
	}
}

// lookupAudits gets a GitHub source's audits, or a note on why there are
// none.
func lookupAudits(source skillSource, names []string) (map[string]map[string]skillAudit, string) {
	if source.Kind != "github" {
		return nil, "none (not a GitHub repository on skills.sh)"
	}
	audits, err := fetchSkillAudits(source.Source, names)
	if errors.Is(err, errSkillLookupsOff) {
		return nil, "not looked up (DO_NOT_TRACK)"
	}
	if err != nil {
		return nil, "unavailable (" + err.Error() + ")"
	}
	return audits, ""
}

// checkSkillRisk asks before adding or updating a skill that a partner
// rates high or critical; without a terminal it needs --accept-risk.
func checkSkillRisk(name string, audits map[string]skillAudit, options skillOptions, stdin io.Reader, stdout io.Writer) error {
	risks := skillRisks(audits)
	if len(risks) == 0 || options.acceptRisk {
		return nil
	}
	if !isTerminal(stdin) {
		return fmt.Errorf("%s is rated %s on skills.sh; read its audits, then rerun with --accept-risk to add it anyway", name, strings.Join(risks, " and "))
	}
	fmt.Fprintf(stdout, "%s is rated %s on skills.sh. Add it anyway? [y/N] ", name, strings.Join(risks, " and "))
	if answer := readSkillAnswer(stdin); answer != "y" && answer != "yes" {
		return errors.New("cancelled")
	}
	return nil
}

// skillEntryFor records where a skill came from.
func skillEntryFor(prepared *preparedSource, skill foundSkill, lockDir string, global bool) (skillLockEntry, error) {
	hash, err := skillFolderHash(skill.Dir)
	if err != nil {
		return skillLockEntry{}, err
	}
	entry := skillLockEntry{SourceType: prepared.source.Kind, Ref: prepared.source.Ref, ComputedHash: hash,
		Commit: prepared.commit, License: skill.License}
	rel, _ := filepath.Rel(prepared.root, filepath.Join(skill.Dir, "SKILL.md"))
	entry.SkillPath = filepath.ToSlash(rel)
	switch prepared.source.Kind {
	case "github":
		entry.Source = prepared.source.Source
	case "git":
		entry.Source, entry.SourceURL = prepared.source.Source, prepared.source.URL
	case "local":
		entry.Source = prepared.source.Source
		if !global {
			if rel, err := filepath.Rel(lockDir, prepared.source.Source); err == nil {
				entry.Source = "./" + filepath.ToSlash(rel)
				if strings.HasPrefix(rel, "..") {
					entry.Source = filepath.ToSlash(rel)
				}
			}
		}
	}
	return entry, nil
}

func skillWhere(global bool) string {
	if global {
		return "~/.agents/skills, linked from ~/.claude/skills"
	}
	return ".agents/skills, linked from .claude/skills"
}

func skillAdd(options skillOptions, stdin io.Reader, stdout io.Writer) error {
	if len(options.words) != 1 {
		return usageError{"usage: hi skill add <owner/repo | owner/repo/skill | URL | folder> [--skill a,b]"}
	}
	source, err := parseSkillSource(options.words[0])
	if err != nil {
		return err
	}
	if source.Kind == "builtin" {
		return writeHiSkill(options.global, options.force, stdout)
	}
	if options.ref != "" {
		if source.Kind == "local" {
			return usageError{"--ref is for git repositories"}
		}
		source.Ref = options.ref
	}
	prepared, err := prepareSkillSource(source)
	if err != nil {
		return err
	}
	defer prepared.cleanup()
	chosen, err := chooseSkills(prepared, options, stdin, stdout)
	if err != nil {
		return err
	}
	return installSkillPlans([]skillPlan{{prepared: prepared, chosen: chosen}}, options, stdin, stdout)
}

// skillPlan is the skills to install from one fetched source.
type skillPlan struct {
	prepared *preparedSource
	chosen   []foundSkill
}

// installSkillPlans shows every skill about to be installed, asks about
// risky ones and then once for all, and installs them.
func installSkillPlans(plans []skillPlan, options skillOptions, stdin io.Reader, stdout io.Writer) error {
	base, err := skillBase(options.global)
	if err != nil {
		return err
	}
	lock, err := readSkillLock(options.global)
	if err != nil {
		return err
	}
	var names []string
	seen := map[string]string{}
	for _, plan := range plans {
		for _, skill := range plan.chosen {
			if skill.Name == "hi" {
				return errors.New("the hi skill comes with hi; hi skill add hi writes it")
			}
			if other, ok := seen[skill.Name]; ok {
				return fmt.Errorf("two skills are called %s, from %s and %s; add one at a time", skill.Name, other, plan.prepared.source.Source)
			}
			seen[skill.Name] = plan.prepared.source.Source
			if _, recorded := lock.entry(skill.Name); !recorded && skillInstalled(base, skill.Name) && !options.force {
				return fmt.Errorf("%s exists and wasn't installed by hi skill; rerun with --force to replace it", skillInstallDir(base, skill.Name))
			}
			names = append(names, skill.Name)
		}
	}
	audits := map[string]map[string]skillAudit{}
	for i, plan := range plans {
		var planNames []string
		for _, skill := range plan.chosen {
			planNames = append(planNames, skill.Name)
		}
		found, note := lookupAudits(plan.prepared.source, planNames)
		for j, skill := range plan.chosen {
			if i+j > 0 {
				fmt.Fprintln(stdout)
			}
			printSkillBlock(stdout, skill, plan.prepared.source.Source, plan.prepared.commit, plan.prepared.date, found[skill.Name], note)
			audits[skill.Name] = found[skill.Name]
		}
	}
	for _, name := range names {
		if err := checkSkillRisk(name, audits[name], options, stdin, stdout); err != nil {
			return err
		}
	}
	question := fmt.Sprintf("Install %s into %s?", strings.Join(names, ", "), skillWhere(options.global))
	if err := confirm(stdin, stdout, options.yes, question); err != nil {
		return err
	}
	lockDir := filepath.Dir(lock.path)
	for _, plan := range plans {
		for _, skill := range plan.chosen {
			entry, err := skillEntryFor(plan.prepared, skill, lockDir, options.global)
			if err != nil {
				return err
			}
			if err := installSkillFiles(base, skill); err != nil {
				return err
			}
			lock.set(skill.Name, entry)
		}
	}
	if err := lock.write(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Installed %s in %s; recorded in %s.\n", strings.Join(names, ", "), skillWhere(options.global), lock.path)
	if !options.global {
		fmt.Fprintf(stdout, "Commit .agents/skills, .claude/skills, and %s to share them.\n", skillLockFile)
	}
	return nil
}

// writeHiSkill writes the built-in hi skill, as hi skill always has.
func writeHiSkill(global, force bool, stdout io.Writer) error {
	base, err := skillBase(global)
	if err != nil {
		return err
	}
	written, err := writeSkill(base, force)
	for _, path := range written {
		fmt.Fprintf(stdout, "Wrote %s\n", path)
	}
	if err != nil {
		return err
	}
	if global {
		fmt.Fprintln(stdout, "Claude Code, Codex, and other agents now know how to use hi in every project.")
	} else {
		fmt.Fprintln(stdout, "Agents started in this folder now know how to use hi. Commit the files to share them.")
	}
	fmt.Fprintln(stdout, "Rerun `hi skill` after updating hi to refresh them.")
	return nil
}

// ---------------------------------------------------------------------------
// ls and show

// skillRow is one installed skill.
type skillRow struct {
	Name   string `json:"name"`
	Source string `json:"source"`
	Commit string `json:"commit,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Status string `json:"status"`
}

// hiSkillVersion is the hi version that wrote the hi skill under base, or
// "" when it isn't there or hi didn't write it.
func hiSkillVersion(base string) string {
	data, err := os.ReadFile(filepath.Join(base, skillDirectory, "SKILL.md"))
	if err != nil {
		return ""
	}
	_, after, found := strings.Cut(string(data), skillMarker+" ")
	if !found {
		return ""
	}
	installed, _, _ := strings.Cut(after, ";")
	return strings.TrimSpace(installed)
}

func installedSkillRows(base string, lock *skillLock) []skillRow {
	var rows []skillRow
	if installed := hiSkillVersion(base); installed != "" {
		status := "up to date"
		if installed != version {
			status = "from hi " + installed + "; hi skill update refreshes it"
		}
		rows = append(rows, skillRow{Name: "hi", Source: "built in", Status: status})
	}
	listed := map[string]bool{"hi": true}
	for _, name := range lock.names() {
		listed[name] = true
		entry, _ := lock.entry(name)
		row := skillRow{Name: name, Source: entry.Source, Commit: entry.Commit, Ref: entry.Ref, Status: "pinned"}
		dir := skillInstallDir(base, name)
		hash, err := skillFolderHash(dir)
		switch {
		case !fileExists(filepath.Join(dir, "SKILL.md")) || err != nil:
			row.Status = "missing; hi skill update reinstalls it"
		case hash != entry.ComputedHash:
			row.Status = "changed here since it was installed"
		case entry.SourceType == "local":
			row.Status = "from a folder"
		case entry.Commit == "":
			row.Status = "not pinned; hi skill update pins it"
		}
		rows = append(rows, row)
	}
	entries, _ := os.ReadDir(filepath.Join(base, ".agents", "skills"))
	for _, entry := range entries {
		if entry.IsDir() && !listed[entry.Name()] && fileExists(filepath.Join(base, ".agents", "skills", entry.Name(), "SKILL.md")) {
			rows = append(rows, skillRow{Name: entry.Name(), Source: "-", Status: "not installed by hi skill"})
		}
	}
	return rows
}

func skillList(options skillOptions, stdout io.Writer) error {
	base, err := skillBase(options.global)
	if err != nil {
		return err
	}
	lock, err := readSkillLock(options.global)
	if err != nil {
		return err
	}
	rows := installedSkillRows(base, lock)
	if options.json {
		if rows == nil {
			rows = []skillRow{}
		}
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	}
	if len(rows) == 0 {
		fmt.Fprintf(stdout, "No skills in %s. Find some with hi skill find <query>, or hi skill in a terminal.\n", filepath.Join(base, ".agents", "skills"))
		return nil
	}
	table := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "SKILL\tSOURCE\tCOMMIT\tSTATUS")
	for _, row := range rows {
		commit := firstNonEmpty(shortCommit(row.Commit), "-")
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\n", row.Name, row.Source, commit, row.Status)
	}
	return table.Flush()
}

func skillShow(options skillOptions, stdout io.Writer) error {
	if len(options.words) != 1 {
		return usageError{"usage: hi skill show <name | source>"}
	}
	arg := options.words[0]
	base, err := skillBase(options.global)
	if err != nil {
		return err
	}
	lock, err := readSkillLock(options.global)
	if err != nil {
		return err
	}
	if arg == "hi" {
		fmt.Fprintf(stdout, "hi comes with hi %s and teaches agents to use it; hi skill writes it, and hi skill --print shows it.\n", version)
		return nil
	}
	if entry, ok := lock.entry(arg); ok || (skillInstalled(base, arg) && !strings.Contains(arg, "/")) {
		dir := skillInstallDir(base, arg)
		meta := map[string]string{}
		if data, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err == nil {
			meta = parseSkillFrontmatter(data)
		}
		skill := foundSkill{Name: arg, Description: meta["description"], License: meta["license"], AllowedTools: meta["allowed-tools"], Dir: dir}
		var audits map[string]skillAudit
		note := ""
		if ok {
			source, _ := entry.skillSource(filepath.Dir(lock.path))
			all, n := lookupAudits(source, []string{arg})
			audits, note = all[arg], n
		} else {
			note = "none (not installed by hi skill)"
		}
		printSkillBlock(stdout, skill, firstNonEmpty(entry.Source, "this folder"), entry.Commit, time.Time{}, audits, note)
		fmt.Fprintf(stdout, "  Installed in %s\n", dir)
		return nil
	}
	source, err := parseSkillSource(arg)
	if err != nil {
		return err
	}
	prepared, err := prepareSkillSource(source)
	if err != nil {
		return err
	}
	defer prepared.cleanup()
	skills := prepared.skills
	if source.Skill != "" {
		if skills, err = chooseSkills(prepared, skillOptions{}, nil, io.Discard); err != nil {
			return err
		}
	}
	var names []string
	for _, skill := range skills {
		names = append(names, skill.Name)
	}
	audits, note := lookupAudits(source, names)
	for i, skill := range skills {
		if i > 0 {
			fmt.Fprintln(stdout)
		}
		printSkillBlock(stdout, skill, source.Source, prepared.commit, prepared.date, audits[skill.Name], note)
	}
	return nil
}

// ---------------------------------------------------------------------------
// update

func skillUpdate(options skillOptions, stdin io.Reader, stdout io.Writer) error {
	if options.bundles {
		return skillUpdateBundles(options, stdin, stdout)
	}
	base, err := skillBase(options.global)
	if err != nil {
		return err
	}
	lock, err := readSkillLock(options.global)
	if err != nil {
		return err
	}
	lockDir := filepath.Dir(lock.path)
	names := options.words
	if len(names) == 0 {
		if hiSkillVersion(base) != "" {
			names = append(names, "hi")
		}
		names = append(names, lock.names()...)
	}
	for _, name := range names {
		if _, ok := lock.entry(name); !ok && name != "hi" {
			return fmt.Errorf("%s wasn't installed by hi skill; see hi skill ls", name)
		}
	}
	if len(names) == 0 {
		fmt.Fprintln(stdout, "No skills installed by hi skill here.")
		return nil
	}
	changes, changed, skipped := 0, false, 0
	commits := map[string]string{}
	for _, name := range names {
		if name == "hi" {
			installed := hiSkillVersion(base)
			if installed == "" || installed == version {
				fmt.Fprintf(stdout, "hi: up to date\n")
				continue
			}
			changes++
			if options.check {
				fmt.Fprintf(stdout, "hi: from hi %s; hi %s has a newer one\n", installed, version)
				continue
			}
			if _, err := writeSkill(base, false); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "hi: updated from hi %s to %s\n", installed, version)
			continue
		}
		entry, _ := lock.entry(name)
		result, err := updateOneSkill(name, entry, base, lockDir, commits, options, stdin, stdout)
		if err != nil {
			return err
		}
		switch result.outcome {
		case "changed":
			changes++
		case "updated":
			changes++
			changed = true
			lock.set(name, result.entry)
		case "recorded":
			changed = true
			lock.set(name, result.entry)
		case "skipped":
			changes++
			skipped++
		}
	}
	if changed {
		if err := lock.write(); err != nil {
			return err
		}
	}
	switch {
	case options.check && changes > 0:
		return exitStatusError{code: 1, message: fmt.Sprintf("%s can be updated; hi skill update updates them", plural(changes, "skill"))}
	case skipped > 0:
		return exitStatusError{code: 1, message: fmt.Sprintf("%s not updated", plural(skipped, "skill"))}
	case changes == 0:
		fmt.Fprintln(stdout, "Every skill is up to date.")
	}
	return nil
}

type skillUpdateResult struct {
	outcome string // current, recorded, changed (with --check), updated, or skipped
	entry   skillLockEntry
}

func updateOneSkill(name string, entry skillLockEntry, base, lockDir string, commits map[string]string, options skillOptions, stdin io.Reader, stdout io.Writer) (skillUpdateResult, error) {
	source, err := entry.skillSource(lockDir)
	if err != nil {
		fmt.Fprintf(stdout, "%s: %v\n", name, err)
		return skillUpdateResult{outcome: "current"}, nil
	}
	installedDir := skillInstallDir(base, name)
	installedHash, hashErr := skillFolderHash(installedDir)
	missing := hashErr != nil || !fileExists(filepath.Join(installedDir, "SKILL.md"))
	localChanges := !missing && installedHash != entry.ComputedHash

	var prepared *preparedSource
	newCommit := ""
	if source.Kind == "local" {
		prepared = &preparedSource{source: source, root: source.Source, cleanup: func() {}}
	} else {
		key := source.URL + " " + source.Ref
		if commits[key] == "" {
			if commits[key], err = resolveSkillCommit(source); err != nil {
				return skillUpdateResult{}, err
			}
		}
		newCommit = commits[key]
		if newCommit == entry.Commit && !missing && !(localChanges && options.force) {
			if localChanges {
				fmt.Fprintf(stdout, "%s: up to date, but changed here since it was installed; --force puts back the source's files\n", name)
			} else {
				fmt.Fprintf(stdout, "%s: up to date\n", name)
			}
			return skillUpdateResult{outcome: "current"}, nil
		}
		wanted := []string{newCommit}
		if entry.Commit != "" && entry.Commit != newCommit {
			wanted = []string{entry.Commit, newCommit}
		}
		root, cleanup, err := fetchSkillCommits(source, wanted...)
		if err != nil && len(wanted) > 1 {
			// The old commit may be gone from the repository.
			root, cleanup, err = fetchSkillCommits(source, newCommit)
		}
		if err != nil {
			return skillUpdateResult{}, err
		}
		prepared = &preparedSource{source: source, root: root, commit: newCommit, date: skillCommitDate(root), cleanup: cleanup}
	}
	defer prepared.cleanup()

	skill, err := findUpdatedSkill(prepared, entry, name)
	if err != nil {
		return skillUpdateResult{}, err
	}
	updated, err := skillEntryFor(prepared, skill, lockDir, options.global)
	if err != nil {
		return skillUpdateResult{}, err
	}
	updated.Ref = entry.Ref
	if source.Kind == "local" {
		updated.Source = entry.Source
	}
	if updated.ComputedHash == entry.ComputedHash && !missing && !(localChanges && options.force) {
		if entry.Commit == updated.Commit {
			fmt.Fprintf(stdout, "%s: up to date\n", name)
			return skillUpdateResult{outcome: "current"}, nil
		}
		if options.check {
			fmt.Fprintf(stdout, "%s: unchanged at %s; hi skill update records the commit\n", name, shortCommit(newCommit))
			return skillUpdateResult{outcome: "current"}, nil
		}
		// The skill's files didn't change; only record the commit.
		fmt.Fprintf(stdout, "%s: unchanged, now pinned at %s\n", name, shortCommit(newCommit))
		return skillUpdateResult{outcome: "recorded", entry: updated}, nil
	}

	header := fmt.Sprintf("%s (%s)", name, entry.Source)
	switch {
	case missing:
		header += ": missing here; reinstall"
	case source.Kind == "local":
		header += ": the folder's files changed"
	case entry.Commit == "":
		header += ": not pinned; " + shortCommit(newCommit) + " differs from the installed files"
	case entry.Commit == newCommit:
		header += ": put back the files changed here"
	default:
		header += fmt.Sprintf(": %s → %s", shortCommit(entry.Commit), shortCommit(newCommit))
		if !prepared.date.IsZero() {
			header += " (" + prepared.date.Format("2006-01-02") + ")"
		}
	}
	fmt.Fprintln(stdout, header)
	skillPathInRepo, _ := filepath.Rel(prepared.root, skill.Dir)
	canDiff := source.Kind != "local" && entry.Commit != "" && entry.Commit != newCommit &&
		printSkillNumstat(prepared.root, entry.Commit, newCommit, filepath.ToSlash(skillPathInRepo), stdout)
	all, note := lookupAudits(source, []string{name})
	if note != "" {
		fmt.Fprintf(stdout, "  Audits: %s\n", note)
	} else {
		fmt.Fprintf(stdout, "  Audits: %s\n", skillAuditSummary(all[name]))
	}
	if options.check {
		return skillUpdateResult{outcome: "changed"}, nil
	}
	if localChanges && !options.force {
		if !isTerminal(stdin) {
			fmt.Fprintf(stdout, "  Skipped: it was changed here since it was installed; --force replaces those changes.\n")
			return skillUpdateResult{outcome: "skipped"}, nil
		}
		fmt.Fprintf(stdout, "  It was changed here since it was installed; updating replaces those changes. Update anyway? [y/N] ")
		if answer := readSkillAnswer(stdin); answer != "y" && answer != "yes" {
			return skillUpdateResult{outcome: "skipped"}, nil
		}
	}
	if err := checkSkillRisk(name, all[name], options, stdin, stdout); err != nil {
		fmt.Fprintf(stdout, "  Skipped: %v\n", err)
		return skillUpdateResult{outcome: "skipped"}, nil
	}
	if !confirmSkillUpdate(prepared.root, entry.Commit, newCommit, filepath.ToSlash(skillPathInRepo), canDiff, options, stdin, stdout) {
		return skillUpdateResult{outcome: "skipped"}, nil
	}
	if err := installSkillFiles(base, skill); err != nil {
		return skillUpdateResult{}, err
	}
	fmt.Fprintf(stdout, "  Updated %s.\n", name)
	return skillUpdateResult{outcome: "updated", entry: updated}, nil
}

// findUpdatedSkill finds an entry's skill in a newer checkout: at its
// recorded path, or by name if it moved.
func findUpdatedSkill(prepared *preparedSource, entry skillLockEntry, name string) (foundSkill, error) {
	if dir := entry.skillDirInSource(prepared.root); dir != "" {
		if data, err := os.ReadFile(filepath.Join(dir, "SKILL.md")); err == nil {
			meta := parseSkillFrontmatter(data)
			if firstNonEmpty(meta["name"], filepath.Base(dir)) == name {
				return foundSkill{Name: name, Description: meta["description"], License: meta["license"],
					AllowedTools: meta["allowed-tools"], Dir: dir, Path: entry.SkillPath}, nil
			}
		}
	}
	skills, err := discoverSkills(prepared.root, "")
	if err != nil {
		return foundSkill{}, err
	}
	for _, skill := range skills {
		if skill.Name == name {
			return skill, nil
		}
	}
	return foundSkill{}, fmt.Errorf("%s no longer has the skill %s; hi skill rm %s removes it", entry.Source, name, name)
}

// ---------------------------------------------------------------------------
// rm

func skillRemove(options skillOptions, stdin io.Reader, stdout io.Writer) error {
	if len(options.words) == 0 {
		return usageError{"usage: hi skill rm <name>..."}
	}
	base, err := skillBase(options.global)
	if err != nil {
		return err
	}
	lock, err := readSkillLock(options.global)
	if err != nil {
		return err
	}
	for _, name := range options.words {
		_, recorded := lock.entry(name)
		switch {
		case name == "hi" && hiSkillVersion(base) != "":
		case recorded:
		case skillInstalled(base, name) && options.force:
		case skillInstalled(base, name):
			return fmt.Errorf("%s wasn't installed by hi skill; rerun with --force to remove it anyway", name)
		default:
			return fmt.Errorf("%s isn't installed here; see hi skill ls", name)
		}
	}
	if err := confirm(stdin, stdout, options.yes, fmt.Sprintf("Remove %s from %s?", strings.Join(options.words, ", "), skillWhere(options.global))); err != nil {
		return err
	}
	for _, name := range options.words {
		if err := removeSkillFiles(base, name); err != nil {
			return err
		}
		lock.remove(name)
	}
	if len(lock.skills) == 0 && !fileExists(lock.path) {
		fmt.Fprintf(stdout, "Removed %s.\n", strings.Join(options.words, ", "))
		return nil
	}
	if err := lock.write(); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Removed %s.\n", strings.Join(options.words, ", "))
	return nil
}
