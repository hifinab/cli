package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Bundles: bundles/<name>.json names skills from skills.sh by source and
// commit, with what each needs installed, which the skills themselves
// don't say. hi skill update --bundles moves the commits. See
// docs/specs/approved/hi_skills_sh.md#bundles.

const skillBundleDir = "bundles"

type skillBundle struct {
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Note        string        `json:"note,omitempty"`
	Skills      []bundleSkill `json:"skills"`
}

type bundleSkill struct {
	Source string       `json:"source"`
	Skill  string       `json:"skill"`
	Commit string       `json:"commit"`
	Needs  *bundleNeeds `json:"needs,omitempty"`
}

// bundleNeeds are closed: each key is one fixed install step, so a bundle
// can't run a shell of its own.
type bundleNeeds struct {
	Layer    string            `json:"layer,omitempty"`
	Apt      []string          `json:"apt,omitempty"`
	Pip      []string          `json:"pip,omitempty"`
	Npm      []string          `json:"npm,omitempty"`
	Browsers []string          `json:"browsers,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Network  *bundleNetwork    `json:"network,omitempty"`
	GPU      bool              `json:"gpu,omitempty"`
}

type bundleNetwork struct {
	Mode   string   `json:"mode"`
	Hosts  []string `json:"hosts,omitempty"`
	Reason string   `json:"reason,omitempty"`
}

var (
	bundleCommitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	bundleAptPattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*$`)
	bundlePipPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9_,-]+\])?==[0-9][A-Za-z0-9.+!-]*$`)
	bundleNpmPattern    = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*@[0-9][A-Za-z0-9.+-]*$`)
	bundleEnvPattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

// readSkillBundle reads and checks one bundle file in this repository's
// bundles/, where every skill comes from a repository at a commit.
func readSkillBundle(path string) (skillBundle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return skillBundle{}, err
	}
	return parseSkillBundle(data, path, false)
}

// parseSkillBundle checks one bundle file. An unknown key or a package that
// isn't a plain pinned name refuses the whole bundle. With ownSkills, a
// skill's source may be ".", the bundle source's own skills/<name>, as in a
// team or local source.
func parseSkillBundle(data []byte, label string, ownSkills bool) (skillBundle, error) {
	var bundle skillBundle
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&bundle); err != nil {
		return bundle, fmt.Errorf("%s: %v", label, err)
	}
	if err := bundle.check(ownSkills); err != nil {
		return bundle, fmt.Errorf("%s: %v", label, err)
	}
	return bundle, nil
}

func (b skillBundle) check(ownSkills bool) error {
	if !skillNamePattern.MatchString(b.Name) {
		return fmt.Errorf("the name %q isn't a plain name", b.Name)
	}
	if b.Description == "" || len(b.Skills) == 0 {
		return fmt.Errorf("%s needs a description and at least one skill", b.Name)
	}
	seen := map[string]bool{}
	for _, skill := range b.Skills {
		if !skillNamePattern.MatchString(skill.Skill) || seen[skill.Skill] {
			return fmt.Errorf("the skill %q is missing, not a plain name, or listed twice", skill.Skill)
		}
		seen[skill.Skill] = true
		if skill.Skill == "hi" {
			return fmt.Errorf("the hi skill is in every bundle already; leave it out")
		}
		switch {
		case skill.Source == "." && ownSkills:
			if skill.Commit != "" {
				return fmt.Errorf("%s: a skill from this source (\".\") has no commit of its own", skill.Skill)
			}
		case skill.Source == ".":
			return fmt.Errorf("%s: only a team or local source has skills of its own (\".\")", skill.Skill)
		default:
			source, err := parseSkillSource(skill.Source)
			if err != nil || (source.Kind != "github" && source.Kind != "git") || source.Skill != "" {
				return fmt.Errorf("%s: the source %q isn't owner/repo or a git URL", skill.Skill, skill.Source)
			}
			if skill.Commit != "" && !bundleCommitPattern.MatchString(skill.Commit) {
				return fmt.Errorf("%s: the commit %q isn't a full commit hash", skill.Skill, skill.Commit)
			}
		}
		if skill.Needs != nil {
			if err := skill.Needs.check(); err != nil {
				return fmt.Errorf("%s: %v", skill.Skill, err)
			}
		}
	}
	return nil
}

func (n bundleNeeds) check() error {
	switch n.Layer {
	case "", "base", "light", "heavy":
	default:
		return fmt.Errorf("layer is base, light, or heavy, not %q", n.Layer)
	}
	for _, list := range []struct {
		key     string
		values  []string
		pattern *regexp.Regexp
		want    string
	}{
		{"apt", n.Apt, bundleAptPattern, "a package name"},
		{"pip", n.Pip, bundlePipPattern, "name==version"},
		{"npm", n.Npm, bundleNpmPattern, "name@version"},
	} {
		for _, value := range list.values {
			if !list.pattern.MatchString(value) {
				return fmt.Errorf("%s: %q isn't %s", list.key, value, list.want)
			}
		}
	}
	for _, browser := range n.Browsers {
		// chromium comes from Playwright, chrome from agent-browser install.
		if browser != "chromium" && browser != "chrome" {
			return fmt.Errorf("browsers: %q isn't chromium or chrome", browser)
		}
	}
	for key := range n.Env {
		if !bundleEnvPattern.MatchString(key) || key == "PATH" || strings.HasPrefix(key, "HI_") {
			return fmt.Errorf("env: %q can't be set by a bundle", key)
		}
	}
	if n.Network != nil {
		switch n.Network.Mode {
		case "locked", "dev", "open":
		default:
			return fmt.Errorf("network: mode is locked, dev, or open, not %q", n.Network.Mode)
		}
	}
	return nil
}

func (n *bundleNeeds) summary() string {
	if n == nil {
		return "nothing beyond the base image"
	}
	var parts []string
	for _, list := range []struct {
		key    string
		values []string
	}{{"apt", n.Apt}, {"pip", n.Pip}, {"npm", n.Npm}, {"browsers", n.Browsers}} {
		if len(list.values) > 0 {
			parts = append(parts, list.key+" "+strings.Join(list.values, " "))
		}
	}
	if n.Network != nil {
		parts = append(parts, "network "+n.Network.Mode)
	}
	if len(parts) == 0 {
		return "nothing beyond the base image"
	}
	return strings.Join(parts, " · ")
}

func writeSkillBundle(path string, bundle skillBundle) error {
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// skillUpdateBundles moves each bundle skill to its source's newest commit,
// showing what changed and the audits, as hi skill update does, and
// rewrites the bundle files. It runs in the folder with bundles/, which is
// hi's own repository or a team's template source.
func skillUpdateBundles(options skillOptions, stdin io.Reader, stdout io.Writer) error {
	files, _ := filepath.Glob(filepath.Join(skillBundleDir, "*.json"))
	if len(files) == 0 {
		return fmt.Errorf("no bundles here (bundles/<name>.json); run it in the folder that has them")
	}
	sort.Strings(files)
	wanted := map[string]bool{}
	for _, word := range options.words {
		wanted[word] = true
	}
	newest := map[string]string{}
	changes, skipped, found := 0, 0, 0
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		// A team source's bundles may name its own skills.
		bundle, err := parseSkillBundle(data, file, true)
		if err != nil {
			return err
		}
		dirty := false
		for i, skill := range bundle.Skills {
			if len(wanted) > 0 && !wanted[bundle.Name] && !wanted[skill.Skill] {
				continue
			}
			found++
			if skill.Source == "." {
				fmt.Fprintf(stdout, "%s: %s is this source's own skill; nothing to move\n", bundle.Name, skill.Skill)
				continue
			}
			result, err := updateBundleSkill(bundle.Name, skill, newest, options, stdin, stdout)
			if err != nil {
				return err
			}
			switch result.outcome {
			case "changed":
				changes++
			case "skipped":
				changes++
				skipped++
			case "updated", "recorded":
				if result.outcome == "updated" {
					changes++
				}
				bundle.Skills[i].Commit = result.entry.Commit
				dirty = true
			}
		}
		if dirty {
			if err := writeSkillBundle(file, bundle); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "Wrote %s\n", file)
		}
	}
	switch {
	case found == 0:
		return fmt.Errorf("no bundle or bundle skill named %s", strings.Join(options.words, ", "))
	case options.check && changes > 0:
		return exitStatusError{code: 1, message: fmt.Sprintf("%s can be updated; hi skill update --bundles updates them", plural(changes, "bundle skill"))}
	case skipped > 0:
		return exitStatusError{code: 1, message: fmt.Sprintf("%s not updated", plural(skipped, "bundle skill"))}
	case changes == 0:
		fmt.Fprintln(stdout, "Every bundle skill is up to date.")
	}
	return nil
}

func updateBundleSkill(bundleName string, skill bundleSkill, newest map[string]string, options skillOptions, stdin io.Reader, stdout io.Writer) (skillUpdateResult, error) {
	label := fmt.Sprintf("%s: %s (%s)", bundleName, skill.Skill, skill.Source)
	source, err := parseSkillSource(skill.Source)
	if err != nil {
		return skillUpdateResult{}, err
	}
	key := source.URL + " " + source.Ref
	if newest[key] == "" {
		if newest[key], err = resolveSkillCommit(source); err != nil {
			return skillUpdateResult{}, err
		}
	}
	commit := newest[key]
	if commit == skill.Commit {
		fmt.Fprintf(stdout, "%s: up to date\n", label)
		return skillUpdateResult{outcome: "current"}, nil
	}
	commits := []string{commit}
	if skill.Commit != "" {
		commits = []string{skill.Commit, commit}
	}
	root, cleanup, err := fetchSkillCommits(source, commits...)
	if err != nil && len(commits) > 1 {
		// The old commit may be gone from the repository.
		commits = []string{commit}
		root, cleanup, err = fetchSkillCommits(source, commit)
	}
	if err != nil {
		return skillUpdateResult{}, err
	}
	defer cleanup()
	found, err := discoverSkills(root, source.Path)
	if err != nil {
		return skillUpdateResult{}, err
	}
	var current foundSkill
	for _, candidate := range found {
		if candidate.Name == skill.Skill {
			current = candidate
		}
	}
	if current.Dir == "" {
		return skillUpdateResult{}, fmt.Errorf("%s: %s no longer has the skill %s; take it out of %s", label, skill.Source, skill.Skill, bundleName)
	}
	pathInRepo, _ := filepath.Rel(root, current.Dir)
	pathInRepo = filepath.ToSlash(pathInRepo)
	pinned := skillUpdateResult{entry: skillLockEntry{Commit: commit}}

	// The old files, beside the new ones, to compare.
	oldDir := ""
	if len(commits) > 1 {
		oldTree, err := os.MkdirTemp("", "hi-skill-old-")
		if err != nil {
			return skillUpdateResult{}, err
		}
		defer os.RemoveAll(oldTree)
		if _, err := skillGit(root, "--work-tree="+oldTree, "checkout", skill.Commit, "--", pathInRepo); err == nil {
			oldDir = filepath.Join(oldTree, filepath.FromSlash(pathInRepo))
		}
		skillGit(root, "reset", "-q")
	}
	if oldDir != "" {
		oldHash, _ := skillFolderHash(oldDir)
		newHash, _ := skillFolderHash(current.Dir)
		if oldHash == newHash {
			if options.check {
				fmt.Fprintf(stdout, "%s: unchanged at %s; hi skill update --bundles records the commit\n", label, shortCommit(commit))
				return skillUpdateResult{outcome: "current"}, nil
			}
			fmt.Fprintf(stdout, "%s: unchanged, now pinned at %s\n", label, shortCommit(commit))
			pinned.outcome = "recorded"
			return pinned, nil
		}
	}

	date := skillCommitDate(root)
	header := label
	switch {
	case skill.Commit == "":
		header += ": not pinned; pin at " + shortCommit(commit)
	default:
		header += fmt.Sprintf(": %s → %s", shortCommit(skill.Commit), shortCommit(commit))
	}
	if !date.IsZero() {
		header += " (" + date.Format("2006-01-02") + ")"
	}
	fmt.Fprintln(stdout, header)
	canDiff := oldDir != "" && printSkillNumstat(root, skill.Commit, commit, pathInRepo, stdout)
	if oldDir == "" {
		fmt.Fprintf(stdout, "  License: %s\n", firstNonEmpty(current.License, "not stated"))
		fmt.Fprintf(stdout, "  Files: %s\n", skillFiles(current.Dir))
	}
	mentions := skillMentions(current.Dir)
	if oldDir != "" {
		before := map[string]bool{}
		for _, mention := range skillMentions(oldDir) {
			before[mention] = true
		}
		var added []string
		for _, mention := range mentions {
			if !before[mention] {
				added = append(added, mention)
			}
		}
		if len(added) > 0 {
			fmt.Fprintf(stdout, "  New mentions: %s; check the bundle's needs\n", strings.Join(added, ", "))
		}
	} else if len(mentions) > 0 {
		fmt.Fprintf(stdout, "  Mentions: %s\n", strings.Join(mentions, ", "))
	}
	fmt.Fprintf(stdout, "  Needs: %s\n", skill.Needs.summary())
	audits, note := lookupAudits(source, []string{skill.Skill})
	if note != "" {
		fmt.Fprintf(stdout, "  Audits: %s\n", note)
	} else {
		fmt.Fprintf(stdout, "  Audits: %s\n", skillAuditSummary(audits[skill.Skill]))
	}
	if options.check {
		return skillUpdateResult{outcome: "changed"}, nil
	}
	if err := checkSkillRisk(skill.Skill, audits[skill.Skill], options, stdin, stdout); err != nil {
		fmt.Fprintf(stdout, "  Skipped: %v\n", err)
		return skillUpdateResult{outcome: "skipped"}, nil
	}
	if !confirmSkillUpdate(root, skill.Commit, commit, pathInRepo, canDiff, options, stdin, stdout) {
		return skillUpdateResult{outcome: "skipped"}, nil
	}
	fmt.Fprintf(stdout, "  Pinned %s at %s.\n", skill.Skill, shortCommit(commit))
	pinned.outcome = "updated"
	return pinned, nil
}

// printSkillNumstat lists the files a skill changed between two commits,
// and says whether git could compare them.
func printSkillNumstat(root, from, to, pathInRepo string, stdout io.Writer) bool {
	stat, err := skillGit(root, "diff", "--numstat", from, to, "--", pathInRepo)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(strings.TrimSpace(stat), "\n") {
		if fields := strings.Fields(line); len(fields) == 3 {
			path := strings.TrimPrefix(fields[2], pathInRepo+"/")
			fmt.Fprintf(stdout, "  %s +%s −%s\n", path, fields[0], fields[1])
		}
	}
	return true
}

// confirmSkillUpdate asks before an update, offering the diff; without a
// terminal it needs --yes.
func confirmSkillUpdate(root, from, to, pathInRepo string, canDiff bool, options skillOptions, stdin io.Reader, stdout io.Writer) bool {
	if options.yes {
		return true
	}
	if !isTerminal(stdin) {
		fmt.Fprintf(stdout, "  Skipped: rerun with --yes to update without a terminal.\n")
		return false
	}
	for {
		if canDiff {
			fmt.Fprint(stdout, "  Update? [Y/n/d: show the diff] ")
		} else {
			fmt.Fprint(stdout, "  Update? [Y/n] ")
		}
		answer := readSkillAnswer(stdin)
		if answer == "d" && canDiff {
			diff, _ := skillGit(root, "diff", from, to, "--", pathInRepo)
			fmt.Fprint(stdout, diff)
			continue
		}
		return answer != "n" && answer != "no"
	}
}
