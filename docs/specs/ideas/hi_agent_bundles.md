# `hi agent` bundles and task files

Status: Draft (proposal by Iman Habib, 2026-10-07)

Dependencies: `hi agent` (release 1, v0.28.0), `hi box` (the proxy, the
base image, Dockerfile builds), `hi init` and `hi server` template sources
(signed, mirrored, cached), `hi skill` (where agents find skills), and
`hi q` for the planner in release 3.

## Goal

Make `hi agent` useful for work that isn't code: browsing, gathering
information, and writing documents, in any folder, including an empty one
that isn't a git repository. The agent gets named sets of skills, bundles,
and a box with exactly the tools those skills need.

```sh
mkdir gpu-prices && cd gpu-prices
hi agent --bundle web,office "Find the five largest Nordic GPU cloud providers and write a comparison into this folder"
```

Today `hi agent` is shaped around code: a git repository, a worktree, a
report of changed files from `git diff`. A research task has no repository;
the folder is just where the results land.

Two smaller changes ship first because everything else builds on them and
they are useful alone: tasks from markdown files, and folders that aren't git
repositories.

Not in this spec: a public marketplace or third-party registry (see
skills.sh in the roadmap, v0.31.0), skills that need the host's desktop, its
browser sessions, or devices other than the Strix Halo GPU, and scheduled or
long-running agents; a session still ends with one report.

## Commands

```text
hi agent [agent] <brief.md>                   the file's contents are the task
hi agent [agent] -                            the task from stdin
hi agent [agent] --task-file <path> ...       a task file with any name
hi agent [agent] --bundle a,b "<task>"        attach bundles
hi agent ... --plan                           resolve and print the plan; start nothing
hi agent ... --spec <path>                    use a saved plan; no planner

hi bundle ls                                  bundles, with their source
hi bundle show <name>                         its skills, what each needs, network
hi bundle prune                               remove images unused for 30 days
```

`--bundle` takes a comma-separated list and can be repeated. Every existing
`hi agent` and `hi box` option still applies.

## Task files (release 1)

The task is joined from the command-line words today (`startAgent` in
`agent.go`). Long briefs are written as files, and
`hi agent "$(cat task.md)"` is easy to get wrong.

Rules, in order:

1. **Exactly one task word ending in `.md`** (any case) is a file name. Two
   or more words are always a plain task, so
   `hi agent "fix the typo in README.md"` is unaffected.
2. **The file exists** (relative to the current folder, or absolute): its
   contents are the task.
3. **It doesn't exist:** hi stops and names the path, rather than send
   the file name to the agent as the task.
4. **`-` alone** reads the task from stdin.
5. **`--task-file <path>`** for other names, such as `brief.txt`.

- hi reads the file on the host before the box is made, so an uncommitted
  brief works although the worktree starts from the last commit.
- An empty file, or one over 1 MB, is refused.
- The task goes to the agent on stdin instead of as an argument, which
  removes Linux's 128 KB limit on one argument (`MAX_ARG_STRLEN`). Claude
  Code's `-p` reads a piped prompt, and `codex exec -` reads stdin; both to
  be checked against the installed versions.
- The report and `--json` get `task_file`, the path used.

### Front matter (release 2)

A brief may carry the options `hi agent` already takes:

```markdown
---
agent: claude
bundles: [web, office]
network: open
---
# Nordic GPU providers

Find the five largest GPU cloud providers in the Nordics...
```

Flags override front matter. Front matter only accepts options the command
line accepts, and anything that widens the box (`network`, `allow`, `data`,
`gpu`) is asked about as if it came from `devcontainer.json`, unless the same
option was also given as a flag. A brief can come from anywhere, including
another agent, so it must not open the network on its own.

## Folders that aren't git repositories (release 1)

`hi agent` in a folder without git works in the folder itself, as
`hi box --here` does today, instead of refusing. The first line says so:

```text
Box gpu-prices-1: claude in ~/work/gpu-prices (not a git repository: it works in place), network dev.
```

- **Changed files** come from a manifest of the folder taken before and
  after the run: path, size, modification time, and a hash for files whose
  size or time changed. Added, changed, and removed files are listed.
- **No copy is taken.** A copy of an arbitrary folder can be huge, and the
  first use is an empty or nearly empty folder. hi warns when the folder has
  more than 1,000 files or 1 GB, and suggests a git repository or a new
  folder.
- **hi's own files** (`.hifin/`) are left out of the list.
- In a git repository nothing changes: a worktree, a branch, and `git diff`.

## Bundles and skills (release 2)

### Where they live

Skills already have a home: the built-in source (`templates/` in this
repository) and the team's template sources served by `hi server`, both
with `skills/<name>/SKILL.md`, signed and cached as
[hi_init.md](../approved/hi_init.md#where-templates-and-skills-live)
describes. Bundles use the same sources, so there is no new server command,
no new signing, and no new cache:

```text
<source>/
  bundles/web.json
  skills/browser/SKILL.md
  skills/browser/requires.json
  skills/web-extract/SKILL.md
  skills/web-extract/requires.json
```

A local folder in `HI_BUNDLES_DIR` (default `~/.local/share/hi/bundles/`)
with the same layout is the person's own and isn't signed. When names clash,
local wins over the team's, and the team's over built-in. `hi bundle ls` and
the plan line show the source.

`bundle.json` only names skills:

```json
{
  "name": "web",
  "description": "Browse the web, extract content, and write findings to files.",
  "skills": ["browser", "web-extract", "markdown-report"]
}
```

A bundle never names an image. Only skills have requirements, so bundles
stack without conflicts.

### `requires.json`

```json
{
  "description": "Drive headless Chromium with Playwright to visit pages, click, and read them.",
  "layer": "heavy",
  "apt": ["fonts-noto"],
  "npm": ["playwright@1.56.0"],
  "browsers": ["chromium"],
  "network": {"mode": "open", "reason": "browses arbitrary sites"},
  "gpu": false
}
```

The keys are closed: `description`, `layer` (`base`, `heavy`, `light`),
`apt`, `pip`, `npm`, `browsers` (Playwright's `install --with-deps`), `env`,
`network` (`mode`, `hosts`, `reason`), and `gpu`. Each key maps to one fixed
install step, so no manifest can run its own shell. An unknown key, or a
package name that isn't a plain name and version, refuses the whole bundle.
Versions must be pinned, or the image changes under a cached tag.

A skill without `requires.json` needs nothing beyond the base image.

### From skills to an image

hi turns the selected skills into a Dockerfile and builds it with the box's
engine, as it already does for a project's Dockerfile
(`ensureProjectImage`). It doesn't write `devcontainer.json`: the box only
reads a few of its keys, and a Dockerfile is what gets built in the end.

The same set of skills always produces the same bytes, so the build cache
works:

1. The box's base image, the one every box uses today.
2. `base` skills' packages, then `heavy` skills', then `light` skills',
   each sorted by skill name, with package lists sorted.
3. No skill files: they go in at run time (below), because they change most
   often and need no build.

hi tags the image `hi-agent:<hash of the Dockerfile>`, and a run whose tag
exists skips the build. The plan says which:

```text
Plan: skills browser, web-extract, markdown-report (3 of 6 attached)
Image: hi-agent:7f3a91c (cached)
Network: open (browser: browses arbitrary sites)
Folder: ~/work/gpu-prices (not a git repository: it works in place)
```

### Skills in the box

The selected skills are copied into the box's own home folder, in
`~/.claude/skills/<name>` and `~/.agents/skills/<name>`, never into the
project or the folder. So a run leaves no skill files in the results, and the
project's own `.claude/skills` still work alongside them. Codex's skill
folder to be checked against the installed version.

### Network

The box's network is the widest mode any selected skill asks for, plus their
hosts. Anything wider than the preset you asked for (default `dev`) is
shown with each skill's reason and asked about, unless `--network` already
allows it. Without a terminal, it needs `--network`; agents calling agents
never widen the network by choosing a bundle.

`--data` with an `open` network is refused in this release: the data token
stays at the proxy, but an agent reading untrusted pages could be told to
read the team's data through the proxy and post it anywhere.

## The planner (release 3)

A cheap model reads the task and the catalog of attached skills (name and
description) and returns only a JSON list of skill names with a reason
each. Unknown names are dropped; code, never the model, builds the image.
The model is the one `hi q` uses, which is the team's through `hi server`
when connected.

- `--plan` prints the plan and starts nothing.
- The plan is saved in the box's state folder, and the report gives its
  path; `--spec <path>` repeats a run with exactly those skills.
- The planner only chooses among the attached bundles.
- If the agent needs a skill that was attached but not chosen, it says so in
  its report's summary block (`status: blocked`); there is no rebuild in the
  middle of a run.

Release 2 installs every skill in the attached bundles. Release 3 is built
only if images from whole bundles turn out to be too slow to build or too
large; see Findings.

## The report

The same report as today, with:

| Field         | What it is                                                     |
|---------------|----------------------------------------------------------------|
| `task_file`   | The brief's path, when the task came from a file               |
| `bundles`     | The attached bundles, with their source                        |
| `skills`      | The skills installed in the box                                |
| `image`       | `hi-agent:<hash>`, and whether it was cached                   |
| `changed_files` | From git, or from the folder's manifest outside git          |

## Releases

1. **Task files and folders without git.** `.md` briefs, `-`, and
   `--task-file`; the task on stdin; agents in any folder, with changed files
   from a manifest. Done when an empty folder and a brief file give a correct
   report with both agents.
2. **Bundles.** `bundle.json` and `requires.json` in the built-in, team,
   and local sources; the Dockerfile generator and `hi-agent:<hash>` images;
   skills in the box's home; the network check; `hi bundle ls`, `show`, and
   `prune`; front matter. Every skill in a bundle is installed. Done when
   `web` builds once and the second run starts from the cache, and a
   team bundle runs on a device that has never seen it.
3. **The planner**, if needed: `--plan`, `--spec`, choosing skills by
   model. Done when an attached but unrelated skill is reliably left out.

## Risks

- **Prompt injection from the web.** A browsing agent reads untrusted text
  and has an open network. The box limits what it can reach (the folder
  only, no tokens inside, read-only git hooks and config), but anything in
  the folder can leave. Hence the network question, and no `--data` with
  `open`.
- **Working in place.** Outside git there is no branch to throw away; an
  agent can delete or overwrite files in the folder. The report lists them,
  but can't undo them.
- **Supply chain.** Bundles install npm and pip packages. Pinned versions
  and signed team sources help; a local bundle is trusted as the person's
  own.
- **Unpinned base packages.** `apt` versions move with the base image, so
  the same hash can mean slightly different tools after `hi box` updates its
  base image. The hash includes the base image's digest.

## Open questions

- [ ] Is "bundle" the right word? `hi init` already calls a server source's
  signed download a bundle. That use is internal, so the user-facing word
  can stay, but the specs should say "source archive" there.
- [ ] Should the `hi` skill become a built-in bundle that `hi agent` always
  attaches, so agents in boxes know how to call `hi`?
- [ ] Do workspaces pin bundle versions, like `.hifin/template.json` pins
  templates, or does a run always take the source's current commit?
- [ ] How do bundles relate to skills.sh (v0.31.0)? A skill from skills.sh,
  pinned to a commit, could be one more local skill in a bundle.
- [ ] Are skills that need an API key in scope? If so, through `--secret` or
  through the hi server, never inside a bundle.

## Findings

From checking the proposal against the code and specs on 2026-10-07:

- **Agents already choose skills.** Claude Code and Codex read only each
  skill's name and description up front and load the rest when a task needs
  it. So the planner saves nothing on the agent's side; it only makes the
  image smaller. A cached image of a whole bundle costs one build, and a
  wrong pick by the planner costs a failed run. Hence the planner moved to
  an optional release 3.
- **Team skills are already signed and served.** Template sources give
  `skills/<name>/SKILL.md`, signing against the server key pinned at
  `hi connect`, and a cache per commit. The proposal's
  `hi server bundles add` and separate signing aren't needed.
- **`hi box` builds Dockerfiles, not devcontainer features.** It reads
  `image`, `build.dockerfile`, `containerEnv`, `postCreateCommand`, and
  `customizations.hi` from `devcontainer.json`. A generated Dockerfile fits
  what exists; a generated `devcontainer.json` would only be read for its
  Dockerfile.
- **`hi box --here` already works in place.** Agents refuse a folder
  without git today only because they default to a worktree.
- **`apt install chromium` doesn't work on Ubuntu images**, where it's a
  snap. Playwright's own browser download does, hence the `browsers` key.
- **The `research` name is taken:** it's a private template in
  `hifinab/templates`. A built-in bundle should be named differently, such as
  `web`, or local-over-team precedence will confuse people.
- **Writing `.hifin/agent-spec.json` into the folder** would add a file to
  every result folder; the plan goes in the box's state folder instead.
