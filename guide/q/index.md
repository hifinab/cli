---
title: Ask for a command
description: Type what you want in plain words with hi q, or chat with it, and get a shell command back to run, edit, copy, or explain. The model can look at files first, and works through Claude Code, OpenRouter, an OpenAI-compatible endpoint, or the Anthropic API.
---

`hi q` turns a request in plain words into one shell command for your
machine. You see the command before anything runs:

```text
$ hi q move all the files in this folder ending with md to a new folder called notes
╭─────────────────────────────────────╮
│ mkdir -p notes && mv -- *.md notes/ │
╰─────────────────────────────────────╯
  Creates a folder called notes and moves the markdown files into it.
  *.md matches 3 files: README.md, my notes.md, plan.md
  changes files
  enter run · e edit · c copy · ? explain · esc cancel
```

Press **Enter** to run it in your shell, in the current folder. Press **e**
to edit it first, **c** to copy it, **?** to have each part explained, or
**Esc** to cancel. A question that needs no command gets a short answer
instead: `hi q how do I see which ports are open`.

Answers are shown with terminal styles: bold, coloured code, and code
blocks without their Markdown fences. When the output goes to a file or a
pipe, the Markdown is left as written.

If the command fails, `hi q` offers to ask for a fix: press Enter and the
model gets the exit status and the end of the error output, and proposes
something else. It tries up to three times.

## Chat

`hi q` on its own opens a prompt below the current line. Ask, run what it
proposes, and follow up; the model remembers the conversation, including
which commands ran and how they ended:

```text
$ hi q
hi q · anthropic/claude-haiku-4.5 at openrouter.ai · /help · Ctrl-D leaves
q› how many md files are here (not counting txt)?
There are 2 Markdown files here: a.md and b.md.
q› move them into a folder called docs
╭───────────────────────────────────────────╮
│ mkdir -p docs && mv -n -- a.md b.md docs/ │
╰───────────────────────────────────────────╯
```

What you type there never passes through your shell, so `(`, `*`, `?`, and
quotes need no escaping. The arrow keys edit the line and recall earlier
questions. `/clear` starts a new conversation, `/context` shows what was
sent about the folder, `/model` names the model, and `/exit` or Ctrl-D
leaves.

`hi q -c` reopens the last conversation, from a chat or a single question,
and `hi q -c <question>` asks one follow-up. Only the last conversation is
kept, in `~/.local/state/hi/q/last.json`.

## How it looks around

Before it proposes, the model can look, and each look shows as one dim line:

```text
$ hi q what Go version does this module need
  · read go.mod
The module needs Go 1.24.0.
```

It has five tools, all read-only, which `hi` runs itself:

| Tool    | What it does                                                         |
|---------|----------------------------------------------------------------------|
| `list`  | Names and sizes in a folder, two levels deep at most                 |
| `read`  | Up to 200 lines of a file in or below the current folder; it asks you first for files elsewhere |
| `help`  | A command's `--help` or man page, for the version installed here     |
| `which` | Whether commands are installed, and where                            |
| `run`   | A read-only command such as `wc -l` or `git status`, for 10 seconds at most |

`run` takes only commands `hi` classes as read-only (see below); anything
else is refused, and the model has to propose it to you instead. `read`
never opens credential files, such as `.env`, `~/.ssh`, `.netrc`, or `hi`'s
own settings. What a tool returns is redacted like the rest of the context.

## Choose a model

The first time, `hi q` uses what it finds, in this order:

1. The model saved by `hi q --setup`.
2. `HI_Q_BASE_URL` and `HI_Q_MODEL`, with `HI_Q_API_KEY` if the endpoint
   needs a key.
3. The [hi server](/guide/compute/managed/#use-the-teams-model-with-hi-q)
   this device is connected to, when it serves a model: no key needed. If
   it can't be reached, `hi q` falls back to the next of these and says so.
4. `OPENAI_API_KEY` (and `OPENAI_BASE_URL` if set), then
   `OPENROUTER_API_KEY`, then `ANTHROPIC_API_KEY`.
5. Claude Code, if it is installed. It uses your Claude sign-in and the Haiku
   model; an answer takes a few seconds, because each one starts Claude Code.

If none of these are there, a menu opens. Run `hi q --setup` to choose again:

- **Your team's hi server**, first and recommended when this device is
  connected to one that serves a model.
- **Claude Code**: no key needed.
- **OpenRouter**: one key from [openrouter.ai/keys](https://openrouter.ai/keys)
  for models from Anthropic, OpenAI, Google, DeepSeek, and others. Only
  models that can call tools are listed, with fast, cheap ones such as
  `anthropic/claude-haiku-4.5` and `google/gemini-2.5-flash` first; press
  `/` to filter.
- **An OpenAI-compatible endpoint**: OpenAI, Groq, Ollama
  (`http://localhost:11434/v1`), vLLM, llama.cpp, LM Studio, or a model you
  serve with `hi compute serve`. Give the base URL, the key (not asked for
  local addresses), and the model, chosen from the endpoint's list when it
  has one.
- **An Anthropic API key**.

`hi q` tries the choice with one small request before saving it. The key is
kept in `~/.config/hi/q-key`, readable only by you. When you run
`hi q --setup` again for the same provider, Enter at the key prompt keeps
the saved key, and the model in use is first in the list. `hi q --status` shows
which model is in use and where it came from. Use
`--provider server|openai|openrouter|anthropic|claude`
or `--model <name>` to change them for one question.

A model that can't call tools, such as some small local ones, is asked for
its answer as JSON instead, so it still works.

## What is sent

With each question, `hi q` sends a short description of where you are:

- your system, shell, and whether the core tools are GNU or BSD;
- the current folder, its first 50 names, and the git branch and state;
- which useful tools are installed, such as `rg`, `jq`, or `docker`;
- the last 20 commands from your shell, and the exit status of the last one
  with shell integration;
- inside tmux, the last 100 lines of the pane, so you can ask about output
  that is already on the screen;
- anything piped into `hi q`.

Values that look like keys, tokens, or passwords are removed first, and
history lines that mention a password or secret are left out. File contents
are never sent. Run `hi q --context` to see exactly what goes out, or add
`--no-context` to send only your question.

Without shell integration, history comes from the history file, which
bash writes only when a shell exits, so the commands of the shell you are
typing in are missing.

## Project notes

A repository can tell `hi q` things the folder listing doesn't show, in
`.hifin/q.md` at its root:

```markdown
- Check a change with `make check`; never run `make deploy`.
- Logs are in `var/log/`, newest last.
- The database is Postgres in Docker: `docker compose exec db psql`.
```

The repository writes this file, so `hi q` asks once before using it,
offering to show it first, and asks again whenever it changes, as direnv
does. Your answer is kept in `~/.local/state/hi/q/notes.json`. Without a
terminal to ask in, new notes are not used. The notes, up to 4 KB, then go
with each question, and `hi q --context` shows them.

## Shell integration

`hi q --setup` offers to add two lines to `~/.bashrc` or `~/.zshrc`, once,
after showing them:

```sh
# hi q: fresh history, questions without quotes, and cd in this shell.
# Remove these lines to turn it off.
command -v hi >/dev/null 2>&1 && eval "$(hi shell-init bash)"
```

In a new terminal, this gives `hi q`:

- **Your current history and the last exit status.** Before each prompt
  the shell writes its last 30 commands to a file only you can read.
- **Questions without quotes.** A line that starts with `hi q ` or `q ` is
  quoted before the shell reads it, so `q count lines (and subfolders)`
  works as typed. A line whose question starts with `-`, `'`, or `"` is
  left alone; use that to pass options, as in `hi q --print …`.
- **`q` as a short name** for `hi q`, unless your machine already has a `q`
  command.
- **Commands in your history.** What `hi q` runs is added to the shell's
  history, so Up and Ctrl-R find it.
- **`cd`, `export`, and `source` that work.** These change the shell
  itself, so `hi q` hands them to your shell to run instead of running
  them in a new one.
- **Edit on the prompt (zsh).** `e` puts the command on your prompt line.
  In bash, and without integration, `e` opens it in `$EDITOR`.

It works in bash 4 or later and zsh. `hi shell-init bash` prints the whole
script, and `hi q --status` says whether it is on. To turn it off, delete
the lines from the rc file.

## How hi judges a command

`hi` parses every proposed command itself, including pipes, `&&`, `$(...)`,
`bash -c`, `xargs`, and `find -exec`, and sorts it into one of three
classes. The model's own label can make a class stricter, never looser.

| Class             | Examples                                                     | To run              |
|-------------------|--------------------------------------------------------------|---------------------|
| **read-only**     | `ls`, `du -sh *`, `git log`, `find` without `-delete`        | Enter               |
| **changes files** | `mv`, `mkdir`, `sed -i`, `git commit`, a `>` redirect         | Enter               |
| **dangerous**     | `sudo`, `rm -r` outside the current folder, `dd`, `mkfs`, `curl … \| sh`, `git push --force`, `git reset --hard`, `docker system prune`, writes to `/etc` or `~/.ssh` | Enter, then type `yes` |

For `mv`, `cp`, `rm`, `ln`, `chmod`, and `chown`, every glob shows what it
matches before you decide. A command `hi` can't parse counts as dangerous.

These checks catch the usual mistakes; they don't make a command safe.
Read what you run. Commands run as you, with your environment, not in a
sandbox.

Every command that runs is recorded in `~/.local/state/hi/q/log.jsonl`
with the time, folder, prompt, class, and exit status, but not its output.
The log stays on your machine.

## Pipes and scripts

Pipe output in to ask about it:

```sh
make test 2>&1 | hi q why does this fail
journalctl -u docker -n 100 | hi q what is wrong
```

The start and end of the input are kept, up to 32 KB. The confirmation then
comes from the terminal, so you can still press Enter to run the fix.

`--print` prints only the command, for scripts and for editing it first:

```sh
hi q --print find large files over 1 GB in my home folder
```

`--explain` explains a command without running anything:

```sh
hi q --explain 'tar -xzvf archive.tar.gz -C /tmp --strip-components=1'
```

`--yes` runs read-only commands without asking; anything else still waits
for Enter.

## Options and questions

Words are always the question: `hi q status of the log file` asks the
model, and so does `hi q setup a python venv`. hi's own options start with
`--` and go before the question, such as `hi q --status` or
`hi q --print …`. To ask something that starts with a dash, put `--` first:
`hi q -- --strip-components in tar?`

## Quoting

Without shell integration, your shell reads the line before `hi q` does:
`(` stops it with a syntax error, and `*` and `?` may turn into file names.
Put such a question in single quotes, or type it in the chat:

```sh
hi q 'delete the *.tmp files older than a week (but keep logs)'
```
