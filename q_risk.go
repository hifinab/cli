package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

// qRisk is how much a proposed command can change. hi decides it by parsing
// the command; the model's own label can only raise it.
type qRisk int

const (
	qReadOnly qRisk = iota
	qChanges
	qDangerous
)

func (r qRisk) String() string {
	switch r {
	case qReadOnly:
		return "read-only"
	case qChanges:
		return "changes files"
	default:
		return "dangerous"
	}
}

func parseQRisk(label string) qRisk {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "read-only", "readonly", "read_only", "safe":
		return qReadOnly
	case "dangerous", "destructive":
		return qDangerous
	default:
		return qChanges
	}
}

// qAssessment is hi's view of a command: its risk, why, and what its globs
// match, so the user sees which files a mv or rm will touch.
type qAssessment struct {
	risk    qRisk
	reasons []string
	matches []string
}

func (a *qAssessment) raise(risk qRisk, reason string) {
	if risk > a.risk {
		a.risk = risk
	}
	if risk == qDangerous && reason != "" {
		for _, existing := range a.reasons {
			if existing == reason {
				return
			}
		}
		a.reasons = append(a.reasons, reason)
	}
}

// qReadOnlyCommands never write files on their own. Some only with checks
// on their flags, below.
var qReadOnlyCommands = map[string]bool{
	"ls": true, "cat": true, "head": true, "tail": true, "less": true, "more": true,
	"grep": true, "egrep": true, "fgrep": true, "rg": true, "fd": true, "fdfind": true,
	"wc": true, "uniq": true, "cut": true, "tr": true, "du": true, "df": true,
	"ps": true, "pwd": true, "echo": true, "printf": true, "which": true, "type": true,
	"whereis": true, "file": true, "stat": true, "tree": true, "jq": true, "date": true,
	"uname": true, "whoami": true, "id": true, "env": true, "printenv": true,
	"hostname": true, "free": true, "uptime": true, "lsblk": true, "ss": true,
	"netstat": true, "basename": true, "dirname": true, "realpath": true,
	"readlink": true, "sha256sum": true, "sha1sum": true, "md5sum": true, "diff": true,
	"cmp": true, "column": true, "man": true, "test": true, "[": true, "true": true,
	"false": true, "nproc": true, "lscpu": true, "lspci": true, "lsusb": true,
	"tldr": true, "xxd": true, "hexdump": true, "od": true, "strings": true,
	"base64": true, "cd": true, "seq": true, "sleep": true, "command": true,
	"history": true, "locate": true, "pgrep": true, "lsof": true, "nl": true,
	"comm": true, "join": true, "paste": true, "fold": true, "rev": true, "tac": true,
	"cal": true, "getent": true, "groups": true, "last": true, "w": true, "who": true,
	"dig": true, "host": true, "nslookup": true, "ping": true, "rocminfo": true,
	"nvidia-smi": true, "amd-smi": true, "rocm-smi": true, "glxinfo": true,
}

// qDangerousCommands can wreck a machine or its data in one line.
var qDangerousCommands = map[string]string{
	"sudo": "runs as root", "doas": "runs as root", "su": "runs as root",
	"dd": "writes raw devices", "shred": "destroys files", "wipefs": "wipes disks",
	"fdisk": "changes partitions", "parted": "changes partitions", "sfdisk": "changes partitions",
	"shutdown": "turns the machine off", "reboot": "restarts the machine",
	"poweroff": "turns the machine off", "halt": "turns the machine off",
	"kill": "stops processes", "killall": "stops processes", "pkill": "stops processes",
	"crontab": "changes scheduled jobs", "eval": "runs code hi cannot check",
	"chattr": "changes file attributes",
}

var qShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true}

// assessCommand classifies a shell command. home is the user's home folder,
// used to spot targets outside the current one.
func assessCommand(command, home string) qAssessment {
	var assessment qAssessment
	assessCommandInto(&assessment, command, home, 0)
	return assessment
}

func assessCommandInto(a *qAssessment, command, home string, depth int) {
	if depth > 3 {
		a.raise(qDangerous, "nested too deeply to check")
		return
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		a.raise(qDangerous, "hi could not parse it to check")
		return
	}
	syntax.Walk(file, func(node syntax.Node) bool {
		switch n := node.(type) {
		case *syntax.Stmt:
			for _, redirect := range n.Redirs {
				assessRedirect(a, redirect, home)
			}
		case *syntax.BinaryCmd:
			if n.Op == syntax.Pipe || n.Op == syntax.PipeAll {
				if qFetches(n.X) && qRunsInput(n.Y) {
					a.raise(qDangerous, "runs a script straight from the internet")
				}
			}
		case *syntax.CallExpr:
			assessCall(a, qWords(n.Args), home, depth)
		case *syntax.FuncDecl:
			a.raise(qChanges, "")
		case *syntax.DeclClause:
			a.raise(qChanges, "")
		}
		return true
	})
}

func assessRedirect(a *qAssessment, redirect *syntax.Redirect, home string) {
	switch redirect.Op {
	case syntax.RdrOut, syntax.AppOut, syntax.RdrAll, syntax.AppAll, syntax.ClbOut:
	default:
		return
	}
	target := qWordText(redirect.Word)
	if target == "/dev/null" || target == "/dev/stdout" || target == "/dev/stderr" {
		return
	}
	a.raise(qChanges, "")
	if reason := qSensitivePath(target, home); reason != "" {
		a.raise(qDangerous, reason)
	}
}

// qWords turns call arguments into text, keeping $VAR and globs as written.
func qWords(words []*syntax.Word) []string {
	out := make([]string, 0, len(words))
	for _, word := range words {
		out = append(out, qWordText(word))
	}
	return out
}

func qWordText(word *syntax.Word) string {
	if word == nil {
		return ""
	}
	if literal := word.Lit(); literal != "" {
		return literal
	}
	var text strings.Builder
	syntax.NewPrinter().Print(&text, word)
	return strings.Trim(text.String(), `"'`)
}

func qCallName(stmt *syntax.Stmt) string {
	if stmt == nil {
		return ""
	}
	if call, ok := stmt.Cmd.(*syntax.CallExpr); ok && len(call.Args) > 0 {
		return filepath.Base(qWordText(call.Args[0]))
	}
	return ""
}

func qFetches(stmt *syntax.Stmt) bool {
	if binary, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
		return qFetches(binary.X) || qFetches(binary.Y)
	}
	name := qCallName(stmt)
	return name == "curl" || name == "wget"
}

func qRunsInput(stmt *syntax.Stmt) bool {
	if binary, ok := stmt.Cmd.(*syntax.BinaryCmd); ok {
		return qRunsInput(binary.X)
	}
	name := qCallName(stmt)
	return qShells[name] || strings.HasPrefix(name, "python") || name == "perl" || name == "ruby" || name == "node" || name == "sudo"
}

func assessCall(a *qAssessment, args []string, home string, depth int) {
	if len(args) == 0 {
		return
	}
	name := filepath.Base(args[0])
	rest := args[1:]
	if reason, ok := qDangerousCommands[name]; ok {
		a.raise(qDangerous, name+" "+reason)
		return
	}
	if strings.HasPrefix(name, "mkfs") {
		a.raise(qDangerous, name+" formats a disk")
		return
	}
	for _, arg := range rest {
		if strings.Contains(arg, ".ssh") {
			if !qReadOnlyCommands[name] {
				a.raise(qDangerous, "touches ~/.ssh")
			}
		}
		if strings.Contains(arg, ".config/hi") && !qReadOnlyCommands[name] {
			a.raise(qDangerous, "touches hi's own settings")
		}
	}

	switch name {
	case "sh", "bash", "zsh", "dash", "ksh":
		for i, arg := range rest {
			if arg == "-c" && i+1 < len(rest) {
				assessCommandInto(a, rest[i+1], home, depth+1)
				return
			}
		}
		a.raise(qChanges, "")
	case "xargs":
		inner := qSkipFlags(rest)
		if len(inner) == 0 {
			return
		}
		assessCall(a, inner, home, depth)
	case "nice", "time", "timeout", "nohup", "stdbuf", "watch":
		inner := qSkipFlags(rest)
		if name == "timeout" && len(inner) > 0 {
			inner = inner[1:]
		}
		assessCall(a, inner, home, depth)
	case "find":
		assessFind(a, rest, home, depth)
	case "sed":
		if qHasFlag(rest, "-i", "--in-place") {
			a.raise(qChanges, "")
		}
	case "awk", "gawk", "mawk":
		for _, arg := range rest {
			if strings.Contains(arg, "system(") || strings.Contains(arg, ">") || strings.Contains(arg, "|") {
				a.raise(qChanges, "")
			}
		}
	case "sort":
		if qHasFlag(rest, "-o", "--output") {
			a.raise(qChanges, "")
		}
	case "git":
		assessGit(a, rest)
	case "docker", "podman":
		assessDocker(a, rest)
	case "systemctl":
		if len(rest) > 0 && !qOneOf(qFirstWord(rest), "status", "list-units", "list-unit-files", "list-timers", "is-active", "is-enabled", "is-failed", "show", "cat") {
			a.raise(qChanges, "")
		}
	case "journalctl":
		if qHasFlag(rest, "--vacuum-size", "--vacuum-time", "--vacuum-files", "--rotate", "--flush") {
			a.raise(qChanges, "")
		}
	case "ip":
		for _, arg := range rest {
			if qOneOf(arg, "add", "del", "delete", "set", "flush", "change", "replace") {
				a.raise(qChanges, "")
			}
		}
	case "rm", "rmdir", "unlink":
		a.raise(qChanges, "")
		recursive := name == "rm" && qHasRecursive(rest)
		for _, target := range qOperands(rest) {
			if recursive {
				if reason := qOutside(target, home); reason != "" {
					a.raise(qDangerous, "rm -r "+reason)
				}
			}
			if reason := qSensitivePath(target, home); reason != "" {
				a.raise(qDangerous, reason)
			}
		}
	case "chmod", "chown", "chgrp":
		a.raise(qChanges, "")
		if qHasRecursive(rest) {
			for _, target := range qOperands(rest) {
				if target == "/" || target == "~" || target == "~/" || target == "$HOME" || target == home {
					a.raise(qDangerous, name+" -R on "+target)
				}
			}
		}
	default:
		if !qReadOnlyCommands[name] {
			a.raise(qChanges, "")
			for _, target := range qOperands(rest) {
				if reason := qSensitivePath(target, home); reason != "" {
					a.raise(qDangerous, reason)
				}
			}
		}
	}

	if qOneOf(name, "mv", "cp", "rm", "ln", "chmod", "chown", "trash", "trash-put") {
		for _, arg := range rest {
			if strings.HasPrefix(arg, "-") || !strings.ContainsAny(arg, "*?[") {
				continue
			}
			a.matches = append(a.matches, qGlobPreview(arg))
		}
	}
}

func assessFind(a *qAssessment, args []string, home string, depth int) {
	for i, arg := range args {
		switch arg {
		case "-delete":
			a.raise(qChanges, "")
			for _, target := range args[:i] {
				if strings.HasPrefix(target, "-") {
					break
				}
				if target == "." || target == "./" {
					continue
				}
				if reason := qOutside(target, home); reason != "" {
					a.raise(qDangerous, "find -delete "+reason)
				}
			}
		case "-exec", "-execdir", "-ok", "-okdir":
			var inner []string
			for _, word := range args[i+1:] {
				if word == ";" || word == `\;` || word == "+" {
					break
				}
				inner = append(inner, word)
			}
			assessCall(a, inner, home, depth)
		case "-fprint", "-fprintf", "-fls", "-fprint0":
			a.raise(qChanges, "")
		}
	}
}

func assessGit(a *qAssessment, args []string) {
	sub := qFirstWord(args)
	rest := args
	for len(rest) > 0 && rest[0] != sub {
		rest = rest[1:]
	}
	if len(rest) > 0 {
		rest = rest[1:]
	}
	switch sub {
	case "status", "log", "diff", "show", "blame", "ls-files", "rev-parse", "describe", "shortlog", "grep", "reflog", "ls-remote", "cat-file", "whatchanged":
	case "branch", "tag", "remote", "stash", "config", "worktree":
		if len(qOperands(rest)) > 0 || qHasFlag(rest, "-d", "-D", "--delete", "-m", "-M", "--unset", "--add") {
			a.raise(qChanges, "")
		}
		if sub == "stash" && qOneOf(qFirstWord(rest), "drop", "clear") {
			a.raise(qDangerous, "git stash "+qFirstWord(rest)+" throws work away")
		}
	case "push":
		a.raise(qChanges, "")
		for _, arg := range rest {
			if arg == "-f" || arg == "--force" || strings.HasPrefix(arg, "--force-with-lease") || (strings.HasPrefix(arg, "+") && len(arg) > 1) {
				a.raise(qDangerous, "git push --force rewrites shared history")
			}
		}
	case "reset":
		a.raise(qChanges, "")
		if qHasFlag(rest, "--hard") {
			a.raise(qDangerous, "git reset --hard throws away changes")
		}
	case "clean":
		a.raise(qChanges, "")
		for _, arg := range rest {
			if (strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.Contains(arg, "f")) || arg == "--force" {
				a.raise(qDangerous, "git clean -f deletes untracked files")
			}
		}
	case "checkout", "restore":
		a.raise(qChanges, "")
		if qHasFlag(rest, "-f", "--force") || qOneOf(".", rest...) {
			a.raise(qDangerous, "git "+sub+" can throw away changes")
		}
	default:
		a.raise(qChanges, "")
	}
}

func assessDocker(a *qAssessment, args []string) {
	sub := qFirstWord(args)
	switch sub {
	case "ps", "images", "logs", "inspect", "version", "info", "stats", "top", "port", "history", "search":
	case "system", "volume", "image", "container", "network", "builder", "compose":
		action := ""
		if words := qOperands(args); len(words) > 1 {
			action = words[1]
		}
		switch {
		case action == "prune":
			a.raise(qDangerous, "docker "+sub+" prune deletes data")
		case qOneOf(action, "ls", "list", "inspect", "df", "ps", "logs", "images", "config", "history"):
		default:
			a.raise(qChanges, "")
		}
	default:
		a.raise(qChanges, "")
		if sub == "run" {
			for _, arg := range args {
				if arg == "--privileged" || strings.HasPrefix(arg, "/:") || strings.HasPrefix(arg, "-v=/:") {
					a.raise(qDangerous, "docker run with host access")
				}
			}
		}
	}
}

// qOutside says why a recursive delete target is outside the current folder,
// or returns "".
func qOutside(target, home string) string {
	switch {
	case target == "." || target == "./" || target == "*" || target == "./*" || target == ".*":
		return "on the whole current folder"
	case strings.HasPrefix(target, "/"):
		return "on " + target
	case target == "~" || strings.HasPrefix(target, "~/") || strings.HasPrefix(target, "$HOME") || strings.HasPrefix(target, "${HOME}"):
		return "in the home folder"
	case target == ".." || strings.HasPrefix(target, "../") || strings.Contains(target, "/../"):
		return "outside the current folder"
	case strings.HasPrefix(target, "$"):
		return "on a path from a variable"
	case home != "" && strings.HasPrefix(target, home):
		return "in the home folder"
	}
	return ""
}

func qSensitivePath(target, home string) string {
	clean := strings.TrimPrefix(target, "~")
	if home != "" {
		clean = strings.TrimPrefix(clean, home)
	}
	switch {
	case strings.HasPrefix(target, "/etc") || strings.HasPrefix(target, "/boot") || strings.HasPrefix(target, "/usr") || strings.HasPrefix(target, "/dev/sd") || strings.HasPrefix(target, "/dev/nvme"):
		return "writes system files in " + target
	case strings.Contains(clean, ".ssh"):
		return "touches ~/.ssh"
	case strings.Contains(clean, ".config/hi"):
		return "touches hi's own settings"
	}
	return ""
}

func qGlobPreview(pattern string) string {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return fmt.Sprintf("%s is not a valid pattern", pattern)
	}
	switch len(matches) {
	case 0:
		return pattern + " matches nothing"
	case 1:
		return pattern + " matches 1 file: " + matches[0]
	}
	shown := matches
	more := ""
	if len(shown) > 8 {
		more = fmt.Sprintf(", and %d more", len(shown)-8)
		shown = shown[:8]
	}
	return fmt.Sprintf("%s matches %d files: %s%s", pattern, len(matches), strings.Join(shown, ", "), more)
}

func qSkipFlags(args []string) []string {
	for i, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return args[i:]
		}
	}
	return nil
}

func qFirstWord(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return ""
}

func qOperands(args []string) []string {
	var out []string
	operands := false
	for _, arg := range args {
		if arg == "--" && !operands {
			operands = true
			continue
		}
		if !operands && strings.HasPrefix(arg, "-") && arg != "-" {
			continue
		}
		out = append(out, arg)
	}
	return out
}

func qHasFlag(args []string, flags ...string) bool {
	for _, arg := range args {
		for _, flag := range flags {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return true
			}
			// -i.bak style suffixes on short flags.
			if len(flag) == 2 && flag[0] == '-' && strings.HasPrefix(arg, flag) && !strings.HasPrefix(arg, "--") {
				return true
			}
		}
	}
	return false
}

func qHasRecursive(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--recursive" {
			return true
		}
		if strings.HasPrefix(arg, "-") && !strings.HasPrefix(arg, "--") && strings.ContainsAny(arg, "rR") {
			return true
		}
	}
	return false
}

func qOneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
