package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"mvdan.cc/sh/v3/syntax"
)

// The shell integration is a few lines that `hi shell-init` prints and the
// rc file evaluates. In the user's own shell it:
//   - writes the last 30 commands and the last exit status to a state file
//     before each prompt, so hi q sees the history the shell holds in memory;
//   - wraps hi, so commands hi q ran land in the shell's history, commands
//     that change the shell (cd, export, source) run in the shell itself,
//     and in zsh a command to edit lands on the prompt;
//   - adds q as a short name for hi q, unless a q command already exists;
//   - quotes the rest of a line that starts with "hi q " or "q " before the
//     shell reads it, so ( ) * ? and ' in a question need no quotes.
//
// The state files live in ~/.local/state/hi/q/shell-<pid>[.ran|.run|.edit].

const qShellMarker = "hi shell-init"

const qBashInit = `# hi shell integration for bash (hi q --setup added it to ~/.bashrc).
if [[ $- == *i* && -z ${__hi_q_loaded-} ]]; then
__hi_q_loaded=1
export HI_Q_SHELL=bash HI_Q_PID=$$
export HI_Q_STATE="${XDG_STATE_HOME:-$HOME/.local/state}/hi/q/shell-$$"
mkdir -p -m 700 "${HI_Q_STATE%/*}" 2>/dev/null

__hi_q_prompt() {
  local exit_status=$?
  { printf 'status %s\n' "$exit_status"; HISTTIMEFORMAT= builtin history 30; } >| "$HI_Q_STATE" 2>/dev/null
  return $exit_status
}
case ";${PROMPT_COMMAND-};" in
  *__hi_q_prompt*) ;;
  *) PROMPT_COMMAND="__hi_q_prompt${PROMPT_COMMAND:+;$PROMPT_COMMAND}" ;;
esac

hi() {
  local exit_status c
  command rm -f -- "$HI_Q_STATE.run" "$HI_Q_STATE.ran" "$HI_Q_STATE.edit"
  command hi "$@"
  exit_status=$?
  if [[ -f $HI_Q_STATE.ran ]]; then
    while IFS= read -r -d '' c; do __hi_q_remember "$c"; done < "$HI_Q_STATE.ran"
    command rm -f -- "$HI_Q_STATE.ran"
  fi
  if [[ -f $HI_Q_STATE.run ]]; then
    c=$(< "$HI_Q_STATE.run")
    command rm -f -- "$HI_Q_STATE.run"
    __hi_q_remember "$c"
    eval -- "$c"
    return
  fi
  return $exit_status
}

# history -s replaces the newest entry, which is the hi q line itself, so
# it is put back first.
__hi_q_remember() {
  local line
  line=$(HISTTIMEFORMAT= builtin history 1)
  line=${line#"${line%%[![:space:]]*}"}
  line=${line#*[[:space:]][[:space:]]}
  builtin history -s -- "$line"
  builtin history -s -- "$1"
}

if ! type q >/dev/null 2>&1; then
  __hi_q_short=1
  q() { hi q "$@"; }
fi

if (( BASH_VERSINFO[0] >= 4 )); then
  __hi_q_rewrite() {
    local line=$READLINE_LINE prefix rest
    case $line in
      "hi q "*) prefix="hi q"; rest=${line#hi q } ;;
      "q "*) [[ -n ${__hi_q_short-} ]] || return 0; prefix=q; rest=${line#q } ;;
      *) return 0 ;;
    esac
    case $rest in ""|-*|\'*|\"*) return 0 ;; esac
    rest=${rest//\'/\'\\\'\'}
    READLINE_LINE="$prefix -- '$rest'"
    READLINE_POINT=${#READLINE_LINE}
  }
  for __hi_q_map in emacs vi-insert; do
    bind -m "$__hi_q_map" -x '"\e[9001~": __hi_q_rewrite'
    bind -m "$__hi_q_map" '"\e[9002~": accept-line'
    bind -m "$__hi_q_map" '"\C-m": "\e[9001~\e[9002~"'
    bind -m "$__hi_q_map" '"\C-j": "\e[9001~\e[9002~"'
  done
  unset __hi_q_map
fi
fi
`

const qZshInit = `# hi shell integration for zsh (hi q --setup added it to ~/.zshrc).
if [[ -o interactive && -z ${__hi_q_loaded-} ]]; then
__hi_q_loaded=1
export HI_Q_SHELL=zsh HI_Q_PID=$$
export HI_Q_STATE="${XDG_STATE_HOME:-$HOME/.local/state}/hi/q/shell-$$"
mkdir -p -m 700 "${HI_Q_STATE%/*}" 2>/dev/null

__hi_q_precmd() {
  local exit_status=$?
  { print -r -- "status $exit_status"; fc -ln -30 2>/dev/null } >| "$HI_Q_STATE" 2>/dev/null
  return $exit_status
}
precmd_functions=(__hi_q_precmd ${precmd_functions:#__hi_q_precmd})

hi() {
  local exit_status c
  command rm -f -- "$HI_Q_STATE.run" "$HI_Q_STATE.ran" "$HI_Q_STATE.edit"
  command hi "$@"
  exit_status=$?
  if [[ -f $HI_Q_STATE.ran ]]; then
    while IFS= read -r -d $'\0' c; do print -s -r -- "$c"; done < "$HI_Q_STATE.ran"
    command rm -f -- "$HI_Q_STATE.ran"
  fi
  if [[ -f $HI_Q_STATE.edit ]]; then
    c=$(< "$HI_Q_STATE.edit")
    command rm -f -- "$HI_Q_STATE.edit"
    print -z -r -- "$c"
  fi
  if [[ -f $HI_Q_STATE.run ]]; then
    c=$(< "$HI_Q_STATE.run")
    command rm -f -- "$HI_Q_STATE.run"
    print -s -r -- "$c"
    eval "$c"
    return
  fi
  return $exit_status
}

if ! whence q >/dev/null 2>&1; then
  __hi_q_short=1
  q() { hi q "$@" }
fi

if [[ ${widgets[accept-line]-} == user:* ]]; then
  zle -A accept-line __hi_q_previous_accept_line
  __hi_q_accept=__hi_q_previous_accept_line
else
  __hi_q_accept=.accept-line
fi
__hi_q_accept_line() {
  local prefix rest
  if [[ $BUFFER == "hi q "* ]]; then
    prefix="hi q"; rest=${BUFFER#hi q }
  elif [[ -n ${__hi_q_short-} && $BUFFER == "q "* ]]; then
    prefix=q; rest=${BUFFER#q }
  fi
  if [[ -n $prefix && -n $rest && $rest != [-\'\"]* ]]; then
    BUFFER="$prefix -- ${(qq)rest}"
  fi
  zle $__hi_q_accept
}
zle -N accept-line __hi_q_accept_line
fi
`

func runShellInit(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: hi shell-init bash|zsh\n\nAdd to ~/.bashrc:  eval \"$(hi shell-init bash)\"\nor run hi q --setup, which asks first.")
		return 2
	}
	switch args[0] {
	case "bash":
		io.WriteString(stdout, qBashInit)
	case "zsh":
		io.WriteString(stdout, qZshInit)
	default:
		fmt.Fprintf(stderr, "hi: shell-init supports bash and zsh, not %q\n", args[0])
		return 2
	}
	return 0
}

// qShellLink is the integration of the shell that started hi, when there
// is one.
type qShellLink struct {
	state string
	shell string
	// direct is true when hi is the shell's own child, so its wrapper reads
	// the files hi leaves; a script or pipeline in between gets history only.
	direct bool
}

func currentQShellLink() *qShellLink {
	state, shell := os.Getenv("HI_Q_STATE"), os.Getenv("HI_Q_SHELL")
	if state == "" || (shell != "bash" && shell != "zsh") {
		return nil
	}
	base, err := qStatePath("")
	if err != nil || !strings.HasPrefix(filepath.Clean(state), filepath.Clean(base)+string(filepath.Separator)+"shell-") {
		return nil
	}
	link := &qShellLink{state: state, shell: shell}
	if pid, err := strconv.Atoi(os.Getenv("HI_Q_PID")); err == nil && pid == os.Getppid() {
		link.direct = true
	}
	qCleanShellStates(base)
	return link
}

// handOff leaves a command for the shell's wrapper: "run" runs it in the
// shell, "edit" puts it on the prompt.
func (l *qShellLink) handOff(kind, command string) error {
	if !l.direct {
		return errors.New("not started by the shell directly")
	}
	return os.WriteFile(l.state+"."+kind, []byte(command), 0o600)
}

// recordRan leaves a command hi ran for the shell to add to its history.
func (l *qShellLink) recordRan(command string) {
	if !l.direct {
		return
	}
	file, err := os.OpenFile(l.state+".ran", os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer file.Close()
	file.WriteString(command + "\x00")
}

var qBashHistoryLine = regexp.MustCompile(`^\s*\d+\*?\s\s(.*)$`)

// history returns the commands the shell wrote before its last prompt,
// oldest first, and the last exit status.
func (l *qShellLink) history() (lines []string, status int, ok bool) {
	file, err := os.Open(l.state)
	if err != nil {
		return nil, 0, false
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	status = -1
	for scanner.Scan() {
		line := scanner.Text()
		if value, found := strings.CutPrefix(line, "status "); found && status == -1 && len(lines) == 0 {
			status, _ = strconv.Atoi(strings.TrimSpace(value))
			continue
		}
		if l.shell == "bash" {
			if match := qBashHistoryLine.FindStringSubmatch(line); match != nil {
				lines = append(lines, match[1])
			} else if len(lines) > 0 {
				lines[len(lines)-1] += "\n" + line
			}
			continue
		}
		lines = append(lines, strings.TrimSpace(line))
	}
	return lines, status, true
}

// qCleanShellStates removes the state files of shells that have exited.
func qCleanShellStates(base string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, entry := range entries {
		name, found := strings.CutPrefix(entry.Name(), "shell-")
		if !found {
			continue
		}
		pidText, _, _ := strings.Cut(name, ".")
		pid, err := strconv.Atoi(pidText)
		if err != nil || pid <= 0 {
			continue
		}
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			os.Remove(filepath.Join(base, entry.Name()))
		}
	}
}

// qShellStateCommands change the shell itself, so they only work when the
// shell runs them.
var qShellStateCommands = map[string]bool{
	"cd": true, "pushd": true, "popd": true, "export": true, "unset": true, "source": true,
	".": true, "alias": true, "unalias": true, "set": true, "shopt": true, "setopt": true,
	"unsetopt": true, "ulimit": true, "umask": true, "exec": true, "conda": true,
	"deactivate": true, "nvm": true, "pyenv": true, "rbenv": true, "hash": true, "declare": true,
	"typeset": true, "readonly": true, "local": true, "history": true, "fc": true,
}

// qNeedsShell is true when a command only makes sense in the user's own
// shell, such as cd or export, outside a subshell or pipeline.
func qNeedsShell(command string) bool {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil {
		return false
	}
	needs := false
	var visit func(stmt *syntax.Stmt)
	visit = func(stmt *syntax.Stmt) {
		if stmt == nil || needs {
			return
		}
		switch cmd := stmt.Cmd.(type) {
		case *syntax.CallExpr:
			if len(cmd.Args) == 0 && len(cmd.Assigns) > 0 {
				needs = true
				return
			}
			if len(cmd.Args) > 0 && qShellStateCommands[qWordText(cmd.Args[0])] {
				needs = true
			}
		case *syntax.DeclClause:
			needs = true
		case *syntax.BinaryCmd:
			if cmd.Op == syntax.AndStmt || cmd.Op == syntax.OrStmt {
				visit(cmd.X)
				visit(cmd.Y)
			}
		case *syntax.Block:
			for _, inner := range cmd.Stmts {
				visit(inner)
			}
		case *syntax.IfClause:
			for clause := cmd; clause != nil; clause = clause.Else {
				for _, inner := range clause.Then {
					visit(inner)
				}
			}
		}
	}
	for _, stmt := range file.Stmts {
		visit(stmt)
	}
	return needs
}

// qShellRC is the rc file for the user's shell, or "" for other shells.
func qShellRC() (shell, path string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ""
	}
	switch shell = qShellName(); shell {
	case "bash":
		return shell, filepath.Join(home, ".bashrc")
	case "zsh":
		return shell, filepath.Join(firstNonEmpty(os.Getenv("ZDOTDIR"), home), ".zshrc")
	}
	return shell, ""
}

// qShellInstalled returns the rc file that already loads the integration.
func qShellInstalled() string {
	_, path := qShellRC()
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), qShellMarker) {
		return ""
	}
	return path
}

func qShellLines(shell string) string {
	return fmt.Sprintf("\n# hi q: fresh history, questions without quotes, and cd in this shell.\n# Remove these lines to turn it off.\ncommand -v hi >/dev/null 2>&1 && eval \"$(hi shell-init %s)\"\n", shell)
}

// offerQShell asks once to add the integration to the rc file.
func offerQShell(ui menuUI) error {
	shell, path := qShellRC()
	if path == "" {
		ui.note(fmt.Sprintf("Shell integration supports bash and zsh; your shell is %s.", shell))
		return nil
	}
	if installed := qShellInstalled(); installed != "" {
		ui.note("Shell integration is already in " + installed + ".")
		return nil
	}
	card := "Adds to " + path + ":" + qShellLines(shell) + "\nhi q then sees your recent commands, takes questions\nwithout quotes (q count lines (and subfolders)), and\nruns cd and export in your shell."
	answer, err := ui.confirm("Set up the shell for hi q?", strings.TrimRight(card, "\n"), false)
	if err != nil || !answer {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.WriteString(qShellLines(shell)); err != nil {
		return err
	}
	ui.note(fmt.Sprintf("Added to %s. Open a new terminal, or run: source %s", path, path))
	return nil
}
