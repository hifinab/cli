package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A repository can give hi q notes in .hifin/q.md: how to run its tests,
// where its logs are, what not to run. The repository writes them, so they
// are used only after the user allows them, once per file content, as
// direnv does for .envrc.

const (
	qNotesFile  = ".hifin/q.md"
	qNotesLimit = 4 << 10
)

// qProjectNotes is the notes file of the repository or folder hi q runs
// in.
type qProjectNotes struct {
	path string
	data []byte
}

// findQProjectNotes looks in the git repository's root, or else the
// current folder.
func findQProjectNotes() *qProjectNotes {
	root := qProbe("git", "rev-parse", "--show-toplevel")
	if root == "" {
		root, _ = os.Getwd()
	}
	path := filepath.Join(root, qNotesFile)
	data, err := os.ReadFile(path)
	if err != nil || len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	return &qProjectNotes{path: path, data: data}
}

func (n *qProjectNotes) sum() string {
	digest := sha256.Sum256(n.data)
	return hex.EncodeToString(digest[:])
}

func (n *qProjectNotes) lines() int {
	return strings.Count(strings.TrimRight(string(n.data), "\n"), "\n") + 1
}

// text is what the model gets: capped and redacted.
func (n *qProjectNotes) text() string {
	text := strings.TrimSpace(string(n.data))
	if len(text) > qNotesLimit {
		text = text[:qNotesLimit] + "\n[... cut ...]"
	}
	return redactQText(text)
}

// qNotesDecisions is ~/.local/state/hi/q/notes.json: for each notes file,
// the content the user allowed or refused.
type qNotesDecisions map[string]struct {
	Sum     string `json:"sum"`
	Allowed bool   `json:"allowed"`
}

func loadQNotesDecisions() qNotesDecisions {
	decisions := qNotesDecisions{}
	if path, err := qStatePath("notes.json"); err == nil {
		if data, err := os.ReadFile(path); err == nil {
			json.Unmarshal(data, &decisions)
		}
	}
	return decisions
}

// decision is "allowed", "refused", or "" when the user hasn't decided on
// this content.
func (n *qProjectNotes) decision() string {
	entry, ok := loadQNotesDecisions()[n.path]
	switch {
	case !ok || entry.Sum != n.sum():
		return ""
	case entry.Allowed:
		return "allowed"
	}
	return "refused"
}

func (n *qProjectNotes) decide(allowed bool) {
	path, err := qStatePath("notes.json")
	if err != nil {
		return
	}
	decisions := loadQNotesDecisions()
	decisions[n.path] = struct {
		Sum     string `json:"sum"`
		Allowed bool   `json:"allowed"`
	}{n.sum(), allowed}
	data, _ := json.MarshalIndent(decisions, "", "  ")
	os.MkdirAll(filepath.Dir(path), 0o700)
	os.WriteFile(path, append(data, '\n'), 0o600)
}

// askQProjectNotes asks once whether to use new or changed notes. v shows
// them first.
func askQProjectNotes(notes *qProjectNotes, keys io.Reader, out io.Writer) {
	for {
		fmt.Fprintf(out, "  This repository has notes for hi q in %s (%d lines), written by the repository.\n  Send them with your questions? [y/N/v to view] ", qNotesFile, notes.lines())
		answer, err := readLine(keys)
		answer = strings.ToLower(strings.TrimSpace(answer))
		switch {
		case answer == "v" || answer == "view":
			for _, line := range strings.Split(strings.TrimRight(string(notes.data), "\n"), "\n") {
				fmt.Fprintln(out, "  │ "+line)
			}
			continue
		case answer == "y" || answer == "yes":
			notes.decide(true)
			fmt.Fprintln(out, "  Allowed. hi q asks again if the file changes.")
		case err == nil || answer != "":
			notes.decide(false)
			fmt.Fprintln(out, "  Not used. hi q asks again if the file changes.")
		}
		return
	}
}
