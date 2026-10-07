# `hi skill` with skills.sh

Status: Draft (2026-10-07)

Dependencies: `hi skill` (v0.7.1, which this replaces and keeps working),
`hi init` (layers name skills), `hi agent` bundles
([hi_agent_bundles.md](hi_agent_bundles.md), which this changes), `hi server`
template sources (team skills, release 3), [skills.sh](https://skills.sh),
and `git`.

## Goal

Get skills the way most agent users already do, from
[skills.sh](https://skills.sh), and keep them up to date, without hi writing
and maintaining generic skills itself.

```sh
hi skill find "excel"
hi skill add anthropics/skills --skill xlsx
hi skill update
```

Today `hi skill` writes one skill, `hi`, embedded in the binary. For
bundles, ten more skills were written by hand (browse, Word, Excel, PDF,
charts, and others), each pinned to package versions. Keeping those current
is work that thousands of people already do on skills.sh, often by the
makers of the tools themselves (Anthropic for documents, DuckDB, Hugging
Face, Vercel's browser). hi should use their skills and keep only what is
about hi.

What hi keeps maintaining is small: the `hi` skill, and for bundles, a list
of which skills to use at which commit and what each needs installed.

## What skills.sh offers

skills.sh is an index of skills that live in git repositories, mostly on
GitHub, ranked by installs, with security audits from five partners. Its own
CLI, `npx skills` ([vercel-labs/skills](https://github.com/vercel-labs/skills),
read at commit `48dc9e8`), installs them for 78 agents.

| Endpoint | Auth | What it gives | Used by |
|---|---|---|---|
| `GET https://skills.sh/api/search?q=&limit=&owner=` | none | `skills: [{id, source, skillId, name, installs}]` | `npx skills find` |
| `GET https://www.skills.sh/tele/audit?source=&skills=a,b` | none | per skill, per partner (`ath`, `socket`, `snyk`, …): `risk` (`safe` to `critical`), `alerts`, `analyzedAt` | `npx skills add` |
| `GET https://skills.sh/api/v1/skills/…` (search, leaderboard, files, hash, audits) | Vercel OIDC token of a Vercel project | the documented API | Vercel projects |

The documented API isn't usable by a CLI: its token only exists inside a
Vercel project. The two open endpoints are undocumented; an open issue asks
for a documented credential-free search
([#1825](https://github.com/vercel-labs/skills/issues/1825), no reply). So:

- **skills.sh is used only for finding skills and for audits.** The skill
  itself always comes from its git repository, at a commit hi records. If
  skills.sh changes or is down, `hi skill add owner/repo` and
  `hi skill update` still work; `find` and audits say they're unavailable.
- **hi sends no install telemetry.** `npx skills` reports every install to
  skills.sh; hi doesn't. A search sends the query, and an audit lookup sends
  public repository and skill names. `DO_NOT_TRACK=1` turns both lookups
  off, as it does for `npx skills`.

## Commands

`hi skill` stays singular, like `hi box`, `hi agent`, and `hi data`; `hi
skills` is accepted as the same command, since `npx skills` users will type
it.

```text
hi skill                         write or update the hi skill here (as today)
hi skill find [query]            search skills.sh: name, source, installs, audits
hi skill add <source>            install skills from a repository at its current commit
    [--skill a,b] [--global] [--yes] [--accept-risk] [--ref <branch|tag>]
hi skill ls [--global] [--json]  installed skills: source, commit, audits, changes
hi skill show <name|source>      files, license, commit, audits, what it runs
hi skill update [name...]        move skills to their source's newest commit
    [--check] [--global] [--yes]
hi skill rm <name>... [--global]
```

`hi skill --global`, `--print`, and `--force` keep working, and `hi skill`
without arguments still writes the `hi` skill: it's what the guide, the
templates, and agents already run.

## Sources

The same forms `npx skills add` accepts, minus npm packages:

| Form | Example |
|---|---|
| `owner/repo` (GitHub) | `anthropics/skills` |
| `owner/repo/skill` (as skills.sh shows it) | `anthropics/skills/xlsx` |
| A repository URL, GitHub, GitLab, or any git | `https://gitlab.com/org/skills`, `git@github.com:org/skills.git` |
| A folder in a repository | `https://github.com/owner/repo/tree/main/skills/web` |
| A local folder | `./my-skills` |
| A built-in skill | `hi` |

A repository with several skills asks which to install, in a terminal; with
`--skill a,b`, or `--yes` for all of them, it doesn't ask. Skills are found
as `npx skills` finds them: `SKILL.md` at the root, or under `skills/`,
`.agents/skills/`, `.claude/skills/`, and the other agents' folders.

## Installing

`hi skill add anthropics/skills --skill xlsx`:

1. **Resolve the commit.** `git ls-remote` gives the commit of the default
   branch (or `--ref`). git uses the person's own credentials, so private
   repositories work as they do for `git clone`.
2. **Fetch that commit.** A shallow clone into a temporary folder; nothing
   in it runs.
3. **Find the skill** and read its `SKILL.md` frontmatter: `name`,
   `description`, `license`, `allowed-tools`.
4. **Show what is about to be installed**, and ask in a terminal:

   ```text
   xlsx from anthropics/skills at 3f9c2e1 (2026-10-02)
     Spreadsheets: open, edit, create, and convert .xlsx, .csv, and .tsv files.
     License: Proprietary (LICENSE.txt): for use with Anthropic's services
     Files: SKILL.md, 4 scripts (Python), 1 reference
     Audits: Agent Trust Hub safe · Socket safe · Snyk medium (2026-09-15)
     Mentions: openpyxl, pandas, markitdown, LibreOffice
   Install into .agents/skills/xlsx, linked from .claude/skills/xlsx? [Y/n]
   ```

   "Mentions" lists tools the skill text expects, from a fixed list hi
   knows (Python packages, `npx`, browsers, LibreOffice, pandoc, API keys),
   so a missing one doesn't come as a surprise; hi installs nothing for it.
5. **Write the skill** to `.agents/skills/<name>/`, which Codex and other
   agents read, and link `.claude/skills/<name>` to it, as `hi skill` and
   `npx skills` do today. With `--global`, the same under the home folder
   (`~/.agents/skills`, `~/.claude/skills`).
6. **Record it** in the lock file.

A skill whose name is taken by one hi didn't install (no lock entry) is
left alone unless `--force`.

## The lock file

In a project, hi uses the lock file `npx skills` writes, `skills-lock.json`
at the project's root, so a team using either tool sees the same skills,
and commits it. hi adds the commit, which `npx skills` doesn't record:

```json
{
  "version": 1,
  "skills": {
    "xlsx": {
      "source": "anthropics/skills",
      "sourceType": "github",
      "skillPath": "skills/xlsx/SKILL.md",
      "computedHash": "9b1e…",
      "commit": "3f9c2e1d…",
      "license": "Proprietary"
    }
  }
}
```

- `computedHash` is SHA-256 over the skill's files, sorted by path, each
  path followed by its contents, as `npx skills` computes it. `npx skills`
  sorts with JavaScript's `localeCompare`, hi by bytes; for the rare skill
  whose file names differ only in case the two disagree, and the other tool
  reinstalls the same files once.
- Entries without `commit`, from `npx skills`, are shown as unpinned, and
  `hi skill update` pins them.
- If `npx skills update` moves a skill, the files no longer match the
  commit; `hi skill ls` says so, and `hi skill update` records the new
  commit after showing the change.

Globally, hi keeps its own list in `~/.config/hi/skills.json` with the same
entries, and leaves `npx skills`' global lock alone.

## Updating

`hi skill update` checks each skill's source for a newer commit
(`git ls-remote`), and for each one that changed:

```text
xlsx (anthropics/skills) 3f9c2e1 → 8a0d417, 12 days newer
  SKILL.md +14 −3, scripts/recalc.py +40 −12
  Audits: Agent Trust Hub safe · Socket safe · Snyk low
Update? [Y/n/d (show the diff)]
```

- `--check` only lists what would change, and exits with 1 if anything
  would; for CI and agents.
- Skills from a local folder are copied again; the built-in `hi` skill
  follows the `hi` binary, as it does today.
- An update never runs anything from the skill.

## Trust

A skill is instructions an agent follows without asking, often with
scripts it runs. A bad one is a prompt injection that stays in the project.
So:

- **Pinned.** Every skill is installed and updated at a recorded commit;
  nothing changes until `hi skill update`, and the change is shown first.
- **Audits shown, risk asked.** skills.sh's partner audits are shown on
  `find`, `add`, `show`, and `update`. When any partner says `high` or
  `critical`, hi asks with "no" as the default; without a terminal it
  refuses unless `--accept-risk`. A skill with no audit yet says so. On
  2026-10-07 even Anthropic's `docx` had a Socket `critical` (two alerts),
  so this is a question, never a silent block.
- **What it runs.** `show` lists scripts by language, `allowed-tools` from
  the frontmatter, and the tools the text mentions.
- **Licenses shown.** Anthropic's document skills (`docx`, `xlsx`, `pptx`,
  `pdf`) are proprietary, for use with Anthropic's services; hi never ships
  them, it downloads them from their repository when the person asks, like
  `npx skills` does, and shows the license first.
- **Popularity isn't trust.** Many repositories on skills.sh are copies of
  others with large install counts (`101-skills/superpowers`,
  `qu-skills/superpowers`, and more on 2026-10-07). `find` marks a skill's
  owner as verified when it's the owner of the tool or company it's about,
  from a short list hi keeps (`anthropics`, `vercel-labs`, `duckdb`,
  `huggingface`, `openai`, …), and lists those first.
- **Agents ask.** The `hi` skill tells agents to ask the person before
  `hi skill add` or `update`, like other commands that change the project.

## Built-in skills

The only built-in skill is `hi`: it's about hi and changes with it, so it
stays embedded in the binary. The ten hand-written skills for bundles
(`browse`, `web-extract`, `cite-sources`, `word-docs`, `spreadsheets`,
`pdf-read`, `convert-docs`, `tables`, `charts`, `hi-data`) are removed
once bundles use skills.sh; `hi-data`'s content moves into the `hi`
skill, which already covers `--data`.

`hifinab/cli` is public and keeps the skill at `skills/hi/SKILL.md`, so
`npx skills add hifinab/cli` already installs it; skills.sh lists it once
someone does. That covers the first item of the roadmap's v0.31.0.

## Bundles

Bundles stay hi's own, but they list skills by source and commit instead
of holding skill files, and they keep what the skills don't say: what
each needs installed. External skills have no machine-readable
requirements (`agent-browser` installs itself with `npm i -g` at run time;
Anthropic's `xlsx` expects openpyxl, pandas, markitdown, and LibreOffice to
be there), so the requirements stay with hi:

```json
{
  "name": "office",
  "description": "Read and write Word, Excel, PowerPoint, and PDF files.",
  "skills": [
    {
      "source": "anthropics/skills", "skill": "xlsx", "commit": "3f9c2e1d…",
      "needs": {"layer": "heavy", "apt": ["libreoffice-calc-nogui"],
                "pip": ["openpyxl==3.1.5", "pandas==3.0.6", "markitdown==0.1.8"]}
    }
  ]
}
```

In the box, hi fetches each skill at its commit into the box's home, as
`hi skill add` does, cached by commit under `~/.cache/hi/skills/`. A
maintainer moves the commits with `hi skill update --bundles` in this
repository, which shows each change and audit, like `update`, and rewrites
the bundle files. The `needs` follow the skill by hand, which is the
remaining upkeep; it's small, and a changed skill that mentions a new tool
shows up in the update's "Mentions".

Candidates for the built-in bundles, to check when bundles are built:

| Bundle | Skills |
|---|---|
| `web` | `vercel-labs/agent-browser` (browser automation, 975,694 installs; its skill loads instructions from the installed CLI, so they match its version) |
| `office` | `anthropics/skills`: `docx`, `xlsx`, `pptx`, `pdf` (proprietary; see Trust) |
| `data` | `duckdb/duckdb-skills`: `query`, `read-file`, `convert-file`; `anthropics/knowledge-work-plugins`: `data-visualization` |

The `hi` skill is added to every bundle, and `cite-sources` has no
first-party counterpart yet; it stays a question below.

## Inside a box

`hi skill` works in a box like anywhere else, through the proxy: GitHub
is in the `dev` preset, and skills.sh would be added to it. Skills that
a box's agent needs come from bundles, which hi installs before the box
starts, so an agent never needs `hi skill add` mid-task.

## With `hi server` (release 3)

- **Team skills** from the template sources the server already mirrors and
  signs: `hi skill add team/<name>` (or `<source>/<name>` when two sources
  clash), recorded with the source's commit.
- **Allowed sources** per group in `policy.json` (`skill_sources`), so a
  group can be limited to the team's sources and a list of owners. An agent
  enrolled with `hi connect --agent` then can't add a skill from anywhere.

## Releases

1. **`hi skill` with sources.** `find`, `add`, `ls`, `show`, `update`
   (with `--check`), `rm`; GitHub, git, and local sources; the shared
   `skills-lock.json`; audits and the risk question; `hi skills` as an
   alias. `hi skill` alone unchanged.
2. **Bundles from skills.sh.** Bundle files list skills by source, commit,
   and needs; `hi skill update --bundles`; the hand-written skills removed,
   and `hi-data` folded into `hi`.
3. **Team skills and policy** through `hi server`.

## Risks

- **Undocumented endpoints.** Search and audits can change without notice.
  Both are optional: installing and updating need only git.
- **A trusted skill turns bad.** A repository can be sold, hijacked, or
  changed. Pins, shown diffs, and audits on update limit this; nothing
  updates by itself.
- **Proprietary skills.** Anthropic's document skills are licensed for use
  with Anthropic's services. Using them with Codex in a bundle may not be
  allowed; bundles that include them should say so, and could pick Claude
  Code when attached.
- **Two tools, one lock file.** If `npx skills` changes its format, hi
  might misread it. hi reads only the fields above, and keeps entries it
  doesn't understand.

## Open questions

- [ ] Should `find` show only verified owners by default, with `--all` for
  the rest?
- [ ] Is a `cite-sources`-style skill worth keeping built in, or is there a
  good first-party research skill (Tavily's needs an API key)?
- [ ] Should `layer.json`'s `skills` in templates accept `owner/repo/skill`
  with a commit, so templates get skills.sh skills too?
- [ ] Do we publish more of hi's knowledge as skills, for example a
  separate `hi-compute` skill, to keep the `hi` skill short?

## Findings

From 2026-10-07:

- skills.sh's documented API needs a Vercel OIDC token
  ([API reference](https://www.skills.sh/docs/api),
  [changelog](https://vercel.com/changelog/the-skills-sh-api-is-now-available)).
  `npx skills` uses `/api/search` and `/tele/audit` without one; both
  answered from this machine.
- `npx skills` keeps a project lock, `skills-lock.json`, with `source`,
  `sourceType`, `ref`, `skillPath`, and `computedHash`, but no commit; its
  global lock is `~/.agents/.skill-lock.json` with GitHub tree hashes.
  Project installs go to `.agents/skills` with links from
  `.claude/skills`, the same as `hi skill`.
- `npx skills` sends install, update, remove, and search events to
  `skills.sh/tele/t` unless `DISABLE_TELEMETRY` or `DO_NOT_TRACK` is set.
- Searches for each bundle's area found first-party skills from Anthropic,
  Vercel, DuckDB, and Hugging Face, and many copied repositories with high
  install counts.
- Anthropic's `docx`, `xlsx`, `pptx`, and `pdf` carry a proprietary license;
  most of `anthropics/skills` (for example `webapp-testing`) is Apache 2.0.
- `hifinab/cli` is public, so its `skills/` folder is visible to
  `npx skills add hifinab/cli` today, including the ten hand-written
  bundle skills.
