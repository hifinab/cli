package main

import (
	"bytes"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

//go:embed skills/hi/SKILL.md
var hiSkill []byte

const skillMarker = "<!-- Written by hi"

// The skill is written once in the Agent Skills location that Codex and
// others read. Claude Code (2.1.283) only reads .claude/skills, so that
// location is a relative symlink to the same folder.
var (
	skillDirectory    = filepath.Join(".agents", "skills", "hi")
	claudeSkillLink   = filepath.Join(".claude", "skills", "hi")
	claudeSkillTarget = filepath.Join("..", "..", ".agents", "skills", "hi")
)

func runSkill(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("hi skill", flag.ContinueOnError)
	flags.SetOutput(stderr)
	global := flags.Bool("global", false, "install for every project in your home directory")
	printOnly := flags.Bool("print", false, "print the skill instead of writing it")
	force := flags.Bool("force", false, "replace a hi skill that hi did not write")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: hi skill [--global] [--print] [--force]")
		return 2
	}
	if *printOnly {
		stdout.Write(skillContent())
		return 0
	}

	base, err := os.Getwd()
	if *global {
		base, err = os.UserHomeDir()
	}
	if err != nil {
		return exitCode(err, stderr)
	}
	written, err := writeSkill(base, *force)
	for _, path := range written {
		fmt.Fprintf(stdout, "Wrote %s\n", path)
	}
	if err != nil {
		return exitCode(err, stderr)
	}
	if *global {
		fmt.Fprintln(stdout, "Claude Code, Codex, and other agents now know how to use hi in every project.")
	} else {
		fmt.Fprintln(stdout, "Agents started in this folder now know how to use hi. Commit the files to share them.")
	}
	fmt.Fprintln(stdout, "Rerun `hi skill` after updating hi to refresh them.")
	return 0
}

// skillContent is the embedded skill with a marker naming the hi version,
// which also identifies files hi may overwrite.
func skillContent() []byte {
	marker := fmt.Sprintf("%s %s; rerun `hi skill` to update. -->\n", skillMarker, version)
	_, body, found := bytes.Cut(hiSkill, []byte("\n---\n"))
	if !found {
		return hiSkill
	}
	frontmatter := hiSkill[:len(hiSkill)-len(body)]
	return append(append(append([]byte{}, frontmatter...), []byte("\n"+marker)...), body...)
}

func writeSkill(base string, force bool) ([]string, error) {
	var written []string
	path := filepath.Join(base, skillDirectory, "SKILL.md")
	if err := checkSkillOwner(path, force); err != nil {
		return written, err
	}
	link := filepath.Join(base, claudeSkillLink)
	if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink == 0 {
		// A folder, from an earlier hi skill or written by hand.
		if err := checkSkillOwner(filepath.Join(link, "SKILL.md"), force); err != nil {
			return written, err
		}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return written, err
	}
	if err := os.WriteFile(path, skillContent(), 0o644); err != nil {
		return written, err
	}
	written = append(written, path)

	if target, err := os.Readlink(link); err == nil && target == claudeSkillTarget {
		return append(written, link+" -> "+claudeSkillTarget), nil
	}
	if err := os.RemoveAll(link); err != nil {
		return written, err
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return written, err
	}
	if err := os.Symlink(claudeSkillTarget, link); err != nil {
		return written, err
	}
	return append(written, link+" -> "+claudeSkillTarget), nil
}

// checkSkillOwner refuses to replace a skill file hi did not write.
func checkSkillOwner(path string, force bool) error {
	existing, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || force {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(existing), skillMarker) {
		return fmt.Errorf("%s exists and was not written by hi; rerun with --force to replace it", path)
	}
	return nil
}
