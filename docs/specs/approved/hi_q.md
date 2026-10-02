# `hi q` specification

Status: Approved (2026-10-02). Release 1 shipped in v0.19.0 and release 2
in v0.20.0; release 3 is reshaped: project notes and the local-model recipe shipped
in v0.22.0, and the Codex and opencode backends are deferred.

Dependencies: none required. It can use `hi context` facts, a model served
by `hi model serve`, and the bundled skill from `hi skill` if they exist.

## Goal

A small AI helper for the terminal. You type what you want in plain words and
get back a shell command you can check, run, edit, or throw away:

```text
$ hi q move all the md files in this folder to a new folder called notes
  mkdir -p notes && mv -- *.md notes/
  moves 4 files: README.md, plan.md, todo.md, ideas.md
  [enter] run   [e] edit   [c] copy   [?] explain   [esc] cancel
```

`hi q` with no prompt opens a short chat in the same terminal, for when the
task takes a few steps. It is not a coding agent. It doesn't edit projects or
run for long, and it can only use a few terminal tools. It should start
quickly, stream the reply, and get out of the way.

It works with whatever the user already has. A signed-in Claude Code or Codex
is used with no extra setup, and any OpenAI-compatible endpoint can be set
by hand. When nothing is found, a short menu on first run sets one up.

## Commands

```text
hi q <prompt...>              one question, one proposed command
hi q                          interactive chat; /exit or Ctrl-D leaves
hi q -c [prompt...]           continue the last conversation
<cmd> 2>&1 | hi q <prompt>    use piped output as context ("why did this fail?")
hi q --explain '<command>'    explain a command without running anything
hi q --print <prompt...>      print only the command, for scripts and $(...)
hi q --setup                  choose or change the provider
hi q --status                 provider, model, and whether shell integration is on
hi q --context                show exactly what would be sent, then exit
hi shell-init bash|zsh|fish   shell integration; add eval "$(hi shell-init bash)" to the rc file
```

Words are always the question, and hi's own actions are options before it.
The first argument that doesn't start with `-` begins the question, so
`hi q status of the log file` asks the model rather than running a
`status` subcommand; v0.19.0 had `setup`, `status`, and `context` as words
and caught such questions. `--` ends the options.

Options: `--provider <name>`, `--model <name>`, `--yes` (run without asking,
but only commands classed as read-only), `--no-context` (send only the
prompt).

Quoting: the shell reads the line before hi sees it, so `(` is a syntax
error and `*`, `?`, and `'` can break or glob. A function can't help, since
the shell parses its arguments first. With shell integration on, the Enter
key rewrites a line that starts with `hi q ` or `q ` to `hi q -- '<the
rest, quoted>'` before the shell reads it: a zsh `accept-line` widget, and
in bash a `bind -x` function that Enter runs before `accept-line`. Lines
whose question starts with `-`, `'`, or `"` are left alone. Without
integration, `hi q` with no arguments opens the chat, where nothing is
parsed by the shell.

## Answer flow

1. hi collects context (below) and sends it with the prompt.
2. The model may call tools to look around, such as listing files or reading
   `--help`. Read-only calls run without asking and appear as one dim line
   each.
3. The model ends with either a **command** (a single line or a short
   script, a one-line reason, and a risk level) or a plain **answer** when
   the user only asked a question.
4. hi shows the command and waits:
   - **Enter** runs it in the user's shell, in the current folder, with the
     output streamed. If it fails, the exit status and the end of standard
     error go back to the model, which can suggest a fix. That is one turn,
     and the user confirms again. Standard output stays on the terminal, so
     colours and pagers work; only standard error is kept.
   - With shell integration, a command that changes the shell itself (`cd`,
     `export`, `source`, an assignment) is handed to the shell's wrapper and
     runs there, ending the turn. Without it, hi says such a command can't
     change the current shell.
   - **e** puts the command on the shell's command line to edit with zsh
     integration (`print -z`). Bash can't fill the next prompt from a
     program, so there, and without integration, it opens `$EDITOR` and
     shows the edited command again, checked afresh.
   - **c** copies it (OSC 52, so it also works over SSH).
   - **?** explains each part.
5. Every command that runs goes into the shell's history as the command
   itself, not as `hi q ...`, so the user can find it later with Ctrl-R.

## Context

Small and on by default. `hi q --context` prints all of it, and `--no-context`
sends none of it.

| Fact | Source |
|---|---|
| OS, distribution, shell, GNU or BSD core tools | `uname`, `/etc/os-release`, `$SHELL`, `ls --version` |
| Current folder and a short listing | up to 50 entries, names and types only |
| Git | branch, clean or dirty, repository root |
| Recent commands | last 20 history lines, redacted |
| Last exit status and the command that set it | shell integration |
| Recent terminal output | the tmux pane's last 100 lines (`tmux capture-pane`), only inside tmux |
| Piped input | stdin when it isn't a terminal, up to 32 KB, head and tail kept |
| Installed tools that matter | `rg`, `fd`, `jq`, `docker`, `git`, `uv`, ..., only names |

A separate process can't see the shell's history in memory, so history comes
from shell integration. `hi shell-init` adds a prompt hook (`PROMPT_COMMAND`
in bash, `precmd` in zsh) that writes the last exit status and the last 30
commands to `~/.local/state/hi/q/shell-<pid>`, in a folder only the user can
read. hi trusts `HI_Q_STATE` only inside that folder, and leaves files for
the wrapper (`.ran`, `.run`, `.edit`) only when its parent process is the
shell itself, so a script can't leave a command for the shell to run. Without integration, hi
reads the end of `$HISTFILE`, which can be stale or empty. `hi q --status`
says which one is in use.

hi can only see the output of earlier commands inside tmux, which `hi
install` already provides. Outside tmux the user pipes it in. Recording all
output through a wrapper, as Butterfish does, is out of scope.

**Redaction** runs before anything leaves the machine. It removes values in
`export X=`, `-p`/`--password`/`--token` style flags, `Authorization:`
headers, URL userinfo, and known key shapes (`sk-`, `ghp_`, `hf_`,
`xox[bp]-`, AWS keys, private key blocks). History lines that start with a
space are already dropped by `HISTCONTROL=ignoreboth`. Lines that contain the
words `password` or `secret` are left out entirely. It never sends file
contents unless the model reads a file through a tool, which the user sees.

## Tools

These are the only tools the model gets. hi runs them, never the provider.

| Tool | Runs without asking | Purpose |
|---|---|---|
| `list(path, pattern?)` | yes | names, sizes, and types; no recursion beyond 2 levels or 200 entries |
| `read(path, lines?)` | yes, within the current folder and below, up to 200 lines; elsewhere asks | read a config or log |
| `help(command)` | yes | `--help`, then `man -P cat` trimmed, for the installed version |
| `which(command)` | yes | is it installed, and where |
| `run(command)` | only if classed read-only | look before proposing, e.g. `git status`, `du -sh *`, `find . -name '*.md'` |
| `propose(command, reason, risk)` | ends the turn | the answer the user confirms |

**Risk classes**, decided by hi from the parsed command (`mvdan.cc/sh`
parses it, so pipes, `&&`, `$(...)`, and redirects are all checked). The
model's own risk label can only raise the class, never lower it:

- **read-only**: an allowlist of commands with safe flags (`ls`, `cat`,
  `head`, `grep`, `rg`, `find` without `-delete`/`-exec`, `git status|log|diff`,
  `du`, `df`, `ps`, `jq`, ...) and no output redirect.
- **changes files**: anything else in the user's own folders. It needs
  Enter. Where possible hi shows the effect first, for example which files a
  glob in `mv`, `rm`, or `cp` matches.
- **dangerous**: `sudo`, `rm -r` outside the current folder, `dd`, `mkfs`,
  `chmod -R`/`chown -R` on `/` or `~`, `curl ... | sh`, `git push --force`,
  `git reset --hard`, `docker system prune`, writes to `/etc`, and anything
  that touches `~/.ssh` or hi's own config. These are shown in red and need
  `yes` to be typed. `--yes` never covers them.

Commands run in the user's shell with their environment, because the point
is to act on their machine. They don't run in a sandbox; confirmation is the
safeguard. [hi_box.md](../ideas/hi_box.md) is the place for anything
unattended.

### Shell know-how

The system prompt is short and fixed, and versioned with hi. It covers
quoting, `--` before file arguments, `-print0`/`xargs -0`, `set -euo
pipefail` in scripts, GNU vs BSD flags (from the context), preferring
`mkdir -p` and `mv -n` over overwriting, one command over a script when
possible, and saying so when a request is ambiguous rather than guessing at
a destructive reading. It also includes a short summary of hi's own commands,
taken from the bundled `skills/hi/SKILL.md`, so `hi q rent me an h100 for an
hour` can propose `hi compute ...`.

A project can add notes in `.hifin/q.md` at the repository's root (or the
current folder outside git), such as how to run its tests or where its logs
are. The repository writes them, so hi asks once before sending them,
offering to show them, and asks again when their content changes, as
direnv does; the decision per file and content hash is kept in
`~/.local/state/hi/q/notes.json`. Without a terminal, undecided notes are
not sent. Up to 4 KB go with each question, redacted like the rest.

## Providers

hi uses the first of these that works, and `hi q --status` names it:

1. `--provider`, or the provider saved by `hi q --setup`.
2. `HI_Q_BASE_URL` / `HI_Q_API_KEY` / `HI_Q_MODEL`.
3. `OPENAI_API_KEY` (with `OPENAI_BASE_URL` if set), then
   `OPENROUTER_API_KEY`, then `ANTHROPIC_API_KEY`.
4. A local OpenAI-compatible server: `hi model serve`, Ollama on
   `localhost:11434`, or llama.cpp/vLLM on a configured port.
5. Claude Code, if `claude` is installed and signed in.
6. Codex, if `codex` is installed and signed in.
7. None of these: the first-run menu.

There are two kinds of backends:

**API backends** (OpenAI-compatible chat completions with tool calls, and
Anthropic's Messages API) are the main path. They are fast, stream tokens,
and use native tool calling. One OpenAI-compatible client covers OpenAI,
OpenRouter, Groq, Together, Ollama, llama.cpp, vLLM, LM Studio, and the
team's own `hi model serve`. Anthropic gets its own small client so the user
can paste an Anthropic key directly. Both are written with the standard
library; no SDKs.

**Agent CLI backends** reuse a subscription the user already has. hi runs
the agent with its own tools off and asks for structured output, so the
agent only plans and hi keeps the tools and the confirmation:

```text
claude -p --tools "" --json-schema <step schema> --output-format json \
  --no-session-persistence --model haiku --system-prompt <hi's prompt>
codex exec --sandbox read-only --ephemeral --skip-git-repo-check \
  --output-schema <step schema file> -o <file>
```

Each reply is one step: a tool call, a `propose`, or an answer. hi runs the
tool and calls the agent again with the transcript so far. This is slower:
each call starts a new process, a few seconds with Claude Code. hi says so
once and recommends an API key or local model for daily use. opencode
(`opencode run --format json`) can be a third later.

### First-run menu

`huh` forms, inline, not full-screen, like the other hi menus:

```text
hi q needs a model. Found:
  > Claude Code (signed in)          uses your Claude subscription, slower start
    Codex (signed in)                uses your ChatGPT sign-in, slower start
    Local model at localhost:11434   llama3.2 and 3 others
    OpenAI-compatible endpoint...    URL, key, and model
    Anthropic API key...
```

Choosing the endpoint asks for the base URL, the key (masked, with
`readSecret`), and the model, chosen from the endpoint's `/models` list when
it has one. hi makes one small test call before saving. The choice goes to
`~/.config/hi/q.json` and the key to `~/.config/hi/q-key`, mode 0600, as the
compute keys are stored. `hi q --setup` reopens the menu.

## Interactive chat

`hi q` with no prompt prints a `q›` prompt below the current line and keeps
a conversation going with the same tools and confirmations. It isn't
full-screen, and the terminal's scrollback keeps everything. Slash commands:
`/context`, `/provider`, `/model`, `/clear`, `/exit`. Up and down recall
earlier prompts. The conversation is saved to `~/.local/state/hi/q/last.json`
so `hi q -c` can continue it. Only the last one is kept.

## Lightweight by design

- One Go file group (`q*.go`) in the existing binary. The only new
  dependency is a shell parser, `mvdan.cc/sh/v3/syntax`.
- No daemon and no index. Context is collected fresh each time and capped at
  about 4,000 tokens.
- With an API backend, the first token should arrive in about the time of
  one network round trip plus the model's own latency. Collecting context
  must take under 50 ms; any probe that might hang has a 200 ms timeout.
- The default model is a fast, cheap one (Haiku, GPT mini, or the local
  model), not the provider's largest.

## Log

Each command that runs is appended to `~/.local/state/hi/q/log.jsonl` with
the time, folder, prompt, command, risk class, and exit status, but not its
output. The log stays local. It doesn't go to `hi server`, because people
won't use the tool for small things if everything they type gets sent to the
team.

## Prior art

Not re-checked for this draft:

- **shell-gpt** (`sgpt --shell`), **aichat** (`-e`), **llm-cmd**, and
  **ai-shell** turn a prompt into a command with an execute/edit/cancel step.
  They need their own API key configured and have no tool loop.
- **GitHub Copilot in the CLI** and **Warp** do the same tied to their own
  account or terminal.
- **Butterfish** wraps the whole shell to capture output as context.

What hi adds: it uses the Claude Code or Codex sign-in the user already has,
it can look around before proposing a command, hi classifies risk itself
rather than trusting the model, and it ships in the binary the team already
installs.

## Releases

1. `hi q <prompt>`, `--print`, `--explain`, piped input, the OpenAI-compatible
   and Anthropic backends, the Claude Code backend (so a machine with only a
   Claude sign-in works from the start), env and config providers, the
   first-run menu, risk classes, and the run/copy/cancel step.
2. `hi shell-init` (history, exit status, edit on the command line, the `q`
   function), tmux output, tools, and the interactive chat with `-c`.
3. The Codex backend, local model discovery, and `hi model serve`
   integration.
4. Project notes in `.hifin/q.md`, and opencode.

## Risks

- A wrong command that looks right. The risk classes, the glob preview, and
  confirmation reduce this, but they don't make it safe; the user still has
  to read the command.
- Secrets in history or piped output that the redaction misses go to the
  provider. `hi q --context` and `--no-context` let the user check.
- A malicious file, log, or README that the model reads can carry
  instructions. Read-only tools limit what that can do. Anything that
  changes files always needs confirmation, and dangerous commands need `yes`
  to be typed.
- Using a Claude or ChatGPT subscription through the agent CLIs this way
  might go against their terms or limits. `claude -p` is Claude Code's own
  documented non-interactive mode, run by the signed-in user, so release 1
  uses it; Codex needs the same check before release 3.

## Open questions

1. The name: `hi q` alone, or should `hi shell-init` also add a short `q` or
   `??` function? `q` clashes with the `q` text-as-SQL tool on some
   machines.
2. ~~Whether `claude -p --tools ""` with `--json-schema` is fast enough.~~
   Measured 2026-10-02 with Claude Code 2.1.287 and Haiku: 4.4 s inside
   Claude Code, 6.4 s wall time, and the structured output is filled.
   Usable, but not fast. `--bare` cannot be used: it skips the subscription
   sign-in and fails with "Not logged in".
3. Whether Codex's `exec` honours `--output-schema` with tools off, or still
   tries to run commands itself under its read-only sandbox.
4. Whether to support fish in the first version, or bash and zsh only.
5. Whether `--yes` should exist at all, or read-only tool calls are enough.
6. Which model `hi model serve` should recommend for this: small enough to
   answer in under a second on the Strix Halo, and good at tool calls.
