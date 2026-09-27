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

// skillDirectories are where agents look for skills, relative to a project
// or home directory: Codex and the Agent Skills standard, then Claude Code.
var skillDirectories = []string{
	filepath.Join(".agents", "skills", "hi"),
	filepath.Join(".claude", "skills", "hi"),
}

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
	content := skillContent()
	var written []string
	for _, directory := range skillDirectories {
		path := filepath.Join(base, directory, "SKILL.md")
		existing, err := os.ReadFile(path)
		if err == nil && !force && !strings.Contains(string(existing), skillMarker) {
			return written, fmt.Errorf("%s exists and was not written by hi; rerun with --force to replace it", path)
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return written, err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return written, err
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			return written, err
		}
		written = append(written, path)
	}
	return written, nil
}
