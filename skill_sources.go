package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Skills from git repositories and folders, at recorded commits, in the
// folders and lock file npx skills uses. See
// docs/specs/approved/hi_skills_sh.md.

// skillLockFile is the project lock file npx skills writes; hi adds the
// commit to each entry.
const skillLockFile = "skills-lock.json"

// skillSource is where skills come from.
type skillSource struct {
	Kind   string // github, git, local, or builtin
	Source string // owner/repo, a URL, a folder, or hi
	URL    string // what git fetches
	Ref    string // a branch or tag; empty is the default branch
	Path   string // the folder in the repository to look in
	Skill  string // one skill, from owner/repo/skill
}

var (
	skillGitHubShort = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+)(?:/([A-Za-z0-9._:-]+))?$`)
	skillGitHubURL   = regexp.MustCompile(`^https?://(?:www\.)?github\.com/([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9._-]+?)(?:\.git)?(?:/tree/([^/]+)(?:/(.+?))?)?/?$`)
	skillNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
)

// skillGitBase is where owner/repo sources are cloned from; tests point it
// at local repositories.
func skillGitBase() string {
	return firstNonEmpty(os.Getenv("HI_SKILL_GIT_BASE"), "https://github.com/")
}

func parseSkillSource(arg string) (skillSource, error) {
	arg = strings.TrimSpace(arg)
	switch {
	case arg == "":
		return skillSource{}, usageError{"name a source: owner/repo, a repository URL, or a folder"}
	case arg == "hi":
		return skillSource{Kind: "builtin", Source: "hi"}, nil
	case arg == "." || arg == ".." || strings.HasPrefix(arg, "./") || strings.HasPrefix(arg, "../") ||
		strings.HasPrefix(arg, "/") || strings.HasPrefix(arg, "~"):
		path := arg
		if strings.HasPrefix(path, "~") {
			home, _ := os.UserHomeDir()
			path = filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return skillSource{}, err
		}
		if info, err := os.Stat(abs); err != nil || !info.IsDir() {
			return skillSource{}, fmt.Errorf("%s is not a folder", arg)
		}
		return skillSource{Kind: "local", Source: abs}, nil
	}
	if match := skillGitHubURL.FindStringSubmatch(arg); match != nil {
		repo := match[1] + "/" + match[2]
		return skillSource{Kind: "github", Source: repo, URL: skillGitBase() + repo + ".git", Ref: match[3], Path: match[4]}, nil
	}
	if strings.Contains(arg, "://") || strings.HasPrefix(arg, "git@") {
		return skillSource{Kind: "git", Source: arg, URL: arg}, nil
	}
	if match := skillGitHubShort.FindStringSubmatch(arg); match != nil {
		repo := match[1] + "/" + strings.TrimSuffix(match[2], ".git")
		return skillSource{Kind: "github", Source: repo, URL: skillGitBase() + repo + ".git", Skill: match[3]}, nil
	}
	return skillSource{}, usageError{fmt.Sprintf("%q is not a source: use owner/repo, owner/repo/skill, a repository URL, or a folder such as ./skills", arg)}
}

// ---------------------------------------------------------------------------
// git

// skillGit runs git without prompting for credentials, so a private
// repository fails instead of waiting for a password.
func skillGit(dir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=true")
	var out, errOut bytes.Buffer
	command.Stdout, command.Stderr = &out, &errOut
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(errOut.String())
		if message == "" {
			message = err.Error()
		}
		return "", errors.New(qFirstLine(message, err.Error()))
	}
	return out.String(), nil
}

// resolveSkillCommit is the commit a source's branch or tag points at now.
func resolveSkillCommit(source skillSource) (string, error) {
	ref := firstNonEmpty(source.Ref, "HEAD")
	out, err := skillGit("", "ls-remote", source.URL, ref)
	if err != nil {
		return "", fmt.Errorf("%s: %v", source.Source, err)
	}
	commit := ""
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		// An annotated tag's commit is on its ^{} line.
		if strings.HasSuffix(fields[1], "^{}") || commit == "" {
			commit = fields[0]
		}
	}
	if commit == "" {
		return "", fmt.Errorf("%s has no branch or tag %s", source.Source, ref)
	}
	return commit, nil
}

// fetchSkillCommits checks out the last of commits into a temporary folder,
// fetching the others too so they can be compared. Nothing in it runs.
func fetchSkillCommits(source skillSource, commits ...string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "hi-skill-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	fail := func(err error) (string, func(), error) {
		cleanup()
		return "", nil, fmt.Errorf("%s: %v", source.Source, err)
	}
	if _, err := skillGit(dir, "init", "-q"); err != nil {
		return fail(err)
	}
	want := commits[len(commits)-1]
	if _, err := skillGit(dir, append([]string{"-c", "protocol.version=2", "fetch", "-q", "--depth", "1", source.URL}, commits...)...); err != nil {
		// Some servers don't serve a commit by its hash; take the branch
		// and check it's still the one asked for.
		if _, err := skillGit(dir, "fetch", "-q", "--depth", "1", source.URL, firstNonEmpty(source.Ref, "HEAD")); err != nil {
			return fail(err)
		}
		if head, _ := skillGit(dir, "rev-parse", "FETCH_HEAD"); strings.TrimSpace(head) != want {
			return fail(fmt.Errorf("can't fetch commit %s", shortCommit(want)))
		}
	}
	if _, err := skillGit(dir, "-c", "advice.detachedHead=false", "checkout", "-q", want); err != nil {
		return fail(err)
	}
	return dir, cleanup, nil
}

// skillCommitDate is when a fetched commit was made.
func skillCommitDate(checkout string) time.Time {
	out, _ := skillGit(checkout, "log", "-1", "--format=%cI")
	date, _ := time.Parse(time.RFC3339, strings.TrimSpace(out))
	return date
}

// ---------------------------------------------------------------------------
// finding skills

// foundSkill is one SKILL.md and its folder.
type foundSkill struct {
	Name         string
	Description  string
	License      string
	AllowedTools string
	Dir          string // the skill's folder on disk
	Path         string // SKILL.md's path in its source, with slashes
}

// discoverSkills finds every SKILL.md under root/sub, as npx skills does:
// at the root, under skills/, or in an agent's skills folder.
func discoverSkills(root, sub string) ([]foundSkill, error) {
	start := filepath.Join(root, filepath.FromSlash(sub))
	if info, err := os.Stat(start); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("%s is not a folder in the source", firstNonEmpty(sub, "."))
	}
	var skills []foundSkill
	seen := map[string]bool{}
	err := filepath.WalkDir(start, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			rel, _ := filepath.Rel(start, path)
			if entry.Name() == ".git" || entry.Name() == "node_modules" || strings.Count(rel, string(filepath.Separator)) > 5 {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Name() != "SKILL.md" || entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		meta := parseSkillFrontmatter(data)
		dir := filepath.Dir(path)
		name := firstNonEmpty(meta["name"], filepath.Base(dir))
		if dir == root && meta["name"] == "" {
			return nil
		}
		if !skillNamePattern.MatchString(name) || seen[name] {
			return nil
		}
		seen[name] = true
		rel, _ := filepath.Rel(root, path)
		skills = append(skills, foundSkill{Name: name, Description: meta["description"], License: meta["license"],
			AllowedTools: meta["allowed-tools"], Dir: dir, Path: filepath.ToSlash(rel)})
		return nil
	})
	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	return skills, err
}

// parseSkillFrontmatter reads the YAML frontmatter's plain keys: one-line
// values, quoted or not, and folded or literal blocks.
func parseSkillFrontmatter(data []byte) map[string]string {
	meta := map[string]string{}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return meta
	}
	end := strings.Index(text[4:], "\n---")
	if end < 0 {
		return meta
	}
	lines := strings.Split(text[4:4+end], "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, ">") || strings.HasPrefix(value, "|") {
			var block []string
			for i+1 < len(lines) && (lines[i+1] == "" || lines[i+1][0] == ' ' || lines[i+1][0] == '\t') {
				i++
				block = append(block, strings.TrimSpace(lines[i]))
			}
			separator := " "
			if strings.HasPrefix(value, "|") {
				separator = "\n"
			}
			value = strings.TrimSpace(strings.Join(block, separator))
		} else if len(value) >= 2 && (value[0] == '"' && value[len(value)-1] == '"' || value[0] == '\'' && value[len(value)-1] == '\'') {
			if value[0] == '"' {
				var unquoted string
				if json.Unmarshal([]byte(value), &unquoted) == nil {
					value = unquoted
				} else {
					value = value[1 : len(value)-1]
				}
			} else {
				value = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
			}
		}
		meta[key] = value
	}
	return meta
}

// skillFolderHash is SHA-256 over the folder's files, sorted by path, each
// path followed by its contents, as npx skills computes computedHash.
func skillFolderHash(dir string) (string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && path != dir && (entry.Name() == ".git" || entry.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(paths, func(i, j int) bool { return filepath.ToSlash(paths[i]) < filepath.ToSlash(paths[j]) })
	hash := sha256.New()
	for _, path := range paths {
		rel, _ := filepath.Rel(dir, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		hash.Write([]byte(filepath.ToSlash(rel)))
		hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// ---------------------------------------------------------------------------
// installing

// skillBase is the folder skills go under: this project, or the home
// folder with --global.
func skillBase(global bool) (string, error) {
	if global {
		return os.UserHomeDir()
	}
	return os.Getwd()
}

func skillInstallDir(base, name string) string {
	return filepath.Join(base, ".agents", "skills", name)
}

func skillClaudeLink(base, name string) string {
	return filepath.Join(base, ".claude", "skills", name)
}

// installSkillFiles copies a skill into .agents/skills/<name> and links
// .claude/skills/<name> to it. Symbolic links in the skill are left out.
func installSkillFiles(base string, skill foundSkill) error {
	target := skillInstallDir(base, skill.Name)
	staging := target + ".hi-new"
	os.RemoveAll(staging)
	err := filepath.WalkDir(skill.Dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(skill.Dir, path)
		out := filepath.Join(staging, rel)
		switch {
		case entry.IsDir() && path != skill.Dir && (entry.Name() == ".git" || entry.Name() == "node_modules"):
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
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode()&0o111 != 0 {
			mode = 0o755
		}
		return os.WriteFile(out, data, mode)
	})
	if err != nil {
		os.RemoveAll(staging)
		return err
	}
	if err := os.RemoveAll(target); err != nil {
		os.RemoveAll(staging)
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		return err
	}
	link := skillClaudeLink(base, skill.Name)
	want := filepath.Join("..", "..", ".agents", "skills", skill.Name)
	if current, err := os.Readlink(link); err == nil && current == want {
		return nil
	}
	if err := os.RemoveAll(link); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return err
	}
	return os.Symlink(want, link)
}

// removeSkillFiles removes a skill's folder and Claude Code's link to it.
func removeSkillFiles(base, name string) error {
	link := skillClaudeLink(base, name)
	if info, err := os.Lstat(link); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			os.Remove(link)
		} else if err := os.RemoveAll(link); err != nil {
			return err
		}
	}
	return os.RemoveAll(skillInstallDir(base, name))
}

// skillInstalled reports whether something is at a skill's folder or link.
func skillInstalled(base, name string) bool {
	for _, path := range []string{skillInstallDir(base, name), skillClaudeLink(base, name)} {
		if _, err := os.Lstat(path); err == nil {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// the lock file

// skillLockEntry is the part of a lock entry hi reads and writes; other
// fields, from npx skills, are kept as they are.
type skillLockEntry struct {
	Source       string `json:"source"`
	SourceType   string `json:"sourceType"`
	SourceURL    string `json:"sourceUrl,omitempty"`
	Ref          string `json:"ref,omitempty"`
	SkillPath    string `json:"skillPath,omitempty"`
	ComputedHash string `json:"computedHash"`
	Commit       string `json:"commit,omitempty"`
	License      string `json:"license,omitempty"`
}

type skillLock struct {
	path    string
	version int
	skills  map[string]map[string]json.RawMessage
}

// skillLockPath is the project's skills-lock.json, or hi's own list for
// skills in the home folder.
func skillLockPath(global bool) (string, error) {
	if global {
		return filepath.Join(qConfigDirectory(), "skills.json"), nil
	}
	wd, err := os.Getwd()
	return filepath.Join(wd, skillLockFile), err
}

func readSkillLock(global bool) (*skillLock, error) {
	path, err := skillLockPath(global)
	if err != nil {
		return nil, err
	}
	lock := &skillLock{path: path, version: 1, skills: map[string]map[string]json.RawMessage{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return lock, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Version int                                   `json:"version"`
		Skills  map[string]map[string]json.RawMessage `json:"skills"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%s: %v; fix or remove it", path, err)
	}
	if file.Version > 0 {
		lock.version = file.Version
	}
	if file.Skills != nil {
		lock.skills = file.Skills
	}
	return lock, nil
}

func (l *skillLock) names() []string {
	var names []string
	for name := range l.skills {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (l *skillLock) entry(name string) (skillLockEntry, bool) {
	raw, ok := l.skills[name]
	if !ok {
		return skillLockEntry{}, false
	}
	var entry skillLockEntry
	data, _ := json.Marshal(raw)
	json.Unmarshal(data, &entry)
	return entry, true
}

// set records an entry, keeping fields hi doesn't know.
func (l *skillLock) set(name string, entry skillLockEntry) {
	fields := l.skills[name]
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	for _, key := range []string{"source", "sourceType", "sourceUrl", "ref", "skillPath", "computedHash", "commit", "license"} {
		delete(fields, key)
	}
	data, _ := json.Marshal(entry)
	var known map[string]json.RawMessage
	json.Unmarshal(data, &known)
	for key, value := range known {
		fields[key] = value
	}
	l.skills[name] = fields
}

func (l *skillLock) remove(name string) { delete(l.skills, name) }

func (l *skillLock) write() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(map[string]any{"version": l.version, "skills": l.skills}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(l.path, append(data, '\n'), 0o644)
}

// skillSource turns an entry back into a source to fetch from.
func (e skillLockEntry) skillSource(lockDir string) (skillSource, error) {
	switch e.SourceType {
	case "github":
		return skillSource{Kind: "github", Source: e.Source, URL: skillGitBase() + e.Source + ".git", Ref: e.Ref}, nil
	case "git":
		return skillSource{Kind: "git", Source: e.Source, URL: firstNonEmpty(e.SourceURL, e.Source), Ref: e.Ref}, nil
	case "local":
		path := e.Source
		if !filepath.IsAbs(path) {
			path = filepath.Join(lockDir, path)
		}
		return skillSource{Kind: "local", Source: path}, nil
	}
	return skillSource{}, fmt.Errorf("hi can't update skills from %s sources", firstNonEmpty(e.SourceType, "unknown"))
}

// skillDirInSource is the folder of an entry's skill in a checkout.
func (e skillLockEntry) skillDirInSource(root string) string {
	if e.SkillPath == "" {
		return ""
	}
	return filepath.Join(root, filepath.FromSlash(filepath.ToSlash(filepath.Dir(e.SkillPath))))
}

// readSkillAnswer is one answer to a question, in lower case.
func readSkillAnswer(stdin io.Reader) string {
	line, _ := readLine(stdin)
	return strings.ToLower(strings.TrimSpace(line))
}
