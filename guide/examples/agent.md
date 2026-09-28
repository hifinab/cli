---
title: Let your coding agent use a GPU
description: Give Claude Code, Codex, or omp the hi skill so they can run your code on rented GPUs - asking before they spend, and cleaning up after.
---

Coding agents can run `hi` like any other command. The `hi` agent skill
teaches them how, and the rules to follow: ask you before spending, preview
with `--dry-run`, always set `--max`, and stop what they start.

## 1. Add the skill to your project

```sh
cd my-project
hi skill
```

```text
Wrote /home/you/my-project/.agents/skills/hi/SKILL.md
Wrote /home/you/my-project/.claude/skills/hi -> ../../.agents/skills/hi
Agents started in this folder now know how to use hi. Commit the files to share them.
```

Commit both paths so everyone's agents get the skill. For every project on
your machine, use `hi skill --global` instead. [Agent skill](/guide/reference/skill/)
has the details.

## 2. Ask for what you want

Start your agent in the project and ask in plain words:

> Run `train.py` on a GPU with 24 GB of memory for 200 epochs and tell me
> the final loss.

> Serve Qwen3-8B on a cheap GPU for half an hour so I can test my app
> against it.

> Is anything still running on Colab or Hugging Face? Stop what you started.

## 3. What the agent does

Following the skill, the agent:

1. Chooses hardware from `hi compute hardware` and runs the command with
   `--dry-run`.
2. Tells you the provider, hardware, price per hour, maximum lifetime, and on
   Hugging Face who pays, and waits for your yes.
3. Runs the command with `--yes`. Agents have no terminal for `hi`'s own
   confirmation, so `--yes` is how your approval reaches `hi`.
4. Uses `--secret` for credentials and never prints tokens.
5. Stops what it started and checks `hi compute ls` when done.

> The skill tells agents never to add `--yes` without your approval. If your
> agent is allowed to run commands without asking, you are still protected by
> `--max`, but review its plans for paid hardware.
{: .warning}

## Keep the skill up to date

After updating `hi`, run `hi skill` again in each project, or
`hi skill --global`, to refresh it with the new commands.
