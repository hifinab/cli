package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// An agent outside a git repository, or with --here, works in the folder
// itself, with no branch to throw away. hi lists the folder's files before
// the run, and the report's changed files are the difference from the
// folder after it. See docs/specs/ideas/hi_agent_bundles.md.

// folderSnapshotLimit is how many files hi lists; beyond it, the changes
// can't all be listed.
const folderSnapshotLimit = 200_000

// folderWarnFiles and folderWarnBytes are when hi suggests a smaller
// folder for an agent that works in place.
const (
	folderWarnFiles = 1000
	folderWarnBytes = 1 << 30
)

// folderSnapshot is each file's size, modification time, and mode, by its
// path relative to the folder.
type folderSnapshot struct {
	Files    map[string][3]int64 `json:"files"`
	Complete bool                `json:"complete"`
	Bytes    int64               `json:"bytes"`
}

// snapshotFolder lists the files under root, without .git, and without
// following symbolic links.
func snapshotFolder(root string) folderSnapshot {
	snapshot := folderSnapshot{Files: map[string][3]int64{}, Complete: true}
	filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			if entry.Name() == ".git" && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if len(snapshot.Files) >= folderSnapshotLimit {
			snapshot.Complete = false
			return filepath.SkipAll
		}
		info, err := entry.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		snapshot.Files[filepath.ToSlash(rel)] = [3]int64{info.Size(), info.ModTime().UnixNano(), int64(info.Mode())}
		snapshot.Bytes += info.Size()
		return nil
	})
	return snapshot
}

func folderSnapshotPath(name string) string { return boxStateFile(name, "files.json") }

// saveFolderSnapshot lists the folder before an agent works in it, and
// warns when the folder is large.
func saveFolderSnapshot(name, root string, stdout io.Writer) error {
	snapshot := snapshotFolder(root)
	if len(snapshot.Files) > folderWarnFiles || snapshot.Bytes > folderWarnBytes {
		fmt.Fprintf(stdout, "Note: the agent works in %s itself, which has %d files (%s), with no branch to throw away; a new folder or a git repository is safer.\n",
			root, len(snapshot.Files), formatDataSize(snapshot.Bytes))
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return os.WriteFile(folderSnapshotPath(name), data, 0o600)
}

// folderChange is one file that was added, changed, or removed.
type folderChange struct {
	Path   string
	Status string // added, changed, or removed
}

// folderChanges compares the folder now with its list from the start. ok
// is false without a list; complete is false when there were too many
// files to compare them all.
func folderChanges(name, root string) (changes []folderChange, ok, complete bool) {
	data, err := os.ReadFile(folderSnapshotPath(name))
	var before folderSnapshot
	if err != nil || json.Unmarshal(data, &before) != nil {
		return nil, false, false
	}
	after := snapshotFolder(root)
	for path, was := range before.Files {
		now, found := after.Files[path]
		switch {
		case !found:
			changes = append(changes, folderChange{path, "removed"})
		case now != was:
			changes = append(changes, folderChange{path, "changed"})
		}
	}
	for path := range after.Files {
		if _, found := before.Files[path]; !found {
			changes = append(changes, folderChange{path, "added"})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, true, before.Complete && after.Complete
}
