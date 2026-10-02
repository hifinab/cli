---
title: Ask for a command
description: Type what you want in plain words with hi q and get one shell command back to run, copy, or explain, from Claude Code, an OpenAI-compatible endpoint, or the Anthropic API.
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
  enter run · c copy · ? explain · esc cancel
```

Press **Enter** to run it in your shell, in the current folder. Press **c**
to copy it, **?** to have each part explained, or **Esc** to cancel. A
question that needs no command gets a short answer instead:
`hi q how do I see which ports are open`.

## Choose a model

The first time, `hi q` uses what it finds, in this order:

1. The model saved by `hi q --setup`.
2. `HI_Q_BASE_URL` and `HI_Q_MODEL`, with `HI_Q_API_KEY` if the endpoint
   needs a key.
3. `OPENAI_API_KEY` (and `OPENAI_BASE_URL` if set), then
   `OPENROUTER_API_KEY`, then `ANTHROPIC_API_KEY`.
4. Claude Code, if it is installed. It uses your Claude sign-in and the Haiku
   model; an answer takes a few seconds, because each one starts Claude Code.

If none of these are there, a menu opens. Run `hi q --setup` to choose again:

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
kept in `~/.config/hi/q-key`, readable only by you. `hi q --status` shows
which model is in use and where it came from. Use
`--provider openai|openrouter|anthropic|claude`
or `--model <name>` to change them for one question.

A model that can't call tools, such as some small local ones, is asked for
its answer as JSON instead, so it still works.

## What is sent

With each question, `hi q` sends a short description of where you are:

- your system, shell, and whether the core tools are GNU or BSD;
- the current folder, its first 50 names, and the git branch and state;
- which useful tools are installed, such as `rg`, `jq`, or `docker`;
- the last 20 commands from your shell's history file;
- anything piped into `hi q`.

Values that look like keys, tokens, or passwords are removed first, and
history lines that mention a password or secret are left out. File contents
are never sent. Run `hi q --context` to see exactly what goes out, or add
`--no-context` to send only your question.

History comes from the history file, so the last few commands of the shell
you are typing in may be missing until the shell writes them. Shell
integration in a later release fixes this.

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

Your shell expands `*`, `?`, and quotes before `hi q` sees the prompt, so
`hi q what is in *.log` may send the matching file names instead. Put
the prompt in single quotes when it has these characters:

```sh
hi q 'delete the *.tmp files older than a week'
```
