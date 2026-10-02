# `hi notes` specification

Status: Draft

Dependencies: `hi server` (devices, users, groups, policy, Slack, audit),
`hi ask` (accepting a note is a question to a person), `hi q` project notes
in `.hifin/q.md`, `hi skill`, and optionally `hi server ai` for search.

## Goal

What one agent learns should reach the next agent, and the next teammate's
agent, once a person agrees it is true. Today it doesn't:

- A Claude Code session finds that `test_prices` fails before 09:00 UTC
  because of a timezone fixture, works around it, and the knowledge ends
  with the session.
- The same lesson is learned again on a colleague's laptop the next week.
- Things that aren't about any one repository have no home at all: "the
  `prices` replica lags about 15 minutes", "RunPod A40s in EU-RO are usually
  sold out before 10:00", "use `uv run`, never `pip`, on the workstations".

`hi notes` is a small, reviewed, shared memory for the team's agents.
Agents propose notes, a person accepts them, and agents look them up when
they need them. It is deliberately not an automatic memory that writes
whatever the agent thinks: an unreviewed note that is wrong is worse than
none, because every later agent trusts it.

## Commands

```text
hi notes search "<words>"          accepted notes that match, most relevant first
hi notes propose "<note>"          propose a note; a person accepts it in Slack
    [--project | --team] [--evidence <text|file|->] [--files <paths>]
hi notes ls [--project | --team] [--mine] [--pending]
hi notes show <id>
hi notes edit|retire <id>          the author, the project's owners, or an admin
hi notes export [--project]        write accepted project notes into the repository
```

- **Project notes** belong to a repository, identified by its normalized
  `origin` URL, and are found from anywhere inside a clone. This is the
  default inside a git repository.
- **Team notes** belong to the team and apply everywhere. They are visible
  per group, as `template_sources` already is in `policy.json`, so a note
  about internal data never reaches the students group.
- `--evidence` says why the note is true: the failing command and its
  output, a link, a commit. A reviewer sees it next to the note.
- `--files` ties the note to paths in the repository. When those files
  change enough later, the note is marked possibly stale (see Staying true).

## Review

A proposal goes to the owner first for an agent's note, through `hi ask`:

```text
📝 Claude Code (for iman) proposes a note for hifinab/prices-feed:
   "test_prices fails before 09:00 UTC: the fixture builds 'today' in
   Europe/Stockholm. Run with TZ=UTC or after 09:00."
   ▸ evidence (8 lines)
   [ Accept ]  [ Edit… ]  [ Reject ]
```

- An agent's own person can accept project notes for repositories they work
  on. Team notes need an approver.
- **Edit…** opens a form with the text; the edited text is what gets
  accepted. Notes are short: 400 characters at most, one fact each, so a
  reviewer reads them in seconds and a search result is easy to trust.
- Rejected proposals are kept for 30 days so the same proposal isn't asked
  again, then removed.
- A person can add a note directly with `hi notes propose`; theirs still
  goes to review for team notes and is accepted at once for projects they
  work on.

## Search

`hi notes search` is what agents call. It returns at most 5 notes with their
ID, scope, age, author, and evidence link, in a compact form an agent reads
quickly:

```text
n-41  project  3w  iman via Claude Code  ✓ sara
      test_prices fails before 09:00 UTC: fixture uses Europe/Stockholm. Run with TZ=UTC.
n-07  team     4m  sara                  ✓ sara
      The prices replica lags ~15 min; read from prices-primary for anything intraday.
```

The first version ranks with plain word matching (BM25) on the server, which
is enough for a few hundred notes and needs no model. Embedding search
through the team's model is a later step, only if word matching misses.

Notes are looked up, not loaded into every prompt. Research on repository
instruction files found that context files raise cost by over 20% without
making agents succeed more often, and that only specific, non-obvious
content helps (ETH Zurich and LogicStar, arXiv 2602.11988, already cited in
the `hi init` spec). So the skill teaches: search notes before
investigating something that looks environmental (a flaky test, a slow
query, a missing permission), and before asking a person with `hi ask`.
`hi q` searches notes for each question and adds at most two matching ones
to its context.

## Staying true

Every note has a **review date**, 90 days after it was accepted by default.
Near it, the weekly Slack report lists the notes due, each with **Still
true** and **Retire** buttons for the person who accepted it. A note nobody
confirms within 30 more days is retired, not deleted, and stops appearing
in searches.

A project note with `--files` is checked on each search against the
repository's current state: when those files have changed by more than a
small threshold since the note's commit, the result says `possibly stale:
fixtures/clock.py changed since` and the agent is told to verify it before
relying on it. When an agent finds a note wrong, it proposes a retirement
with evidence, reviewed like a new note.

## Export to the repository

Notes on the server are for knowledge that changes, or that shouldn't be in
the repository. Knowledge that should be permanent belongs in the
repository itself, reviewed with the code. `hi notes export` writes the
project's accepted notes into a managed block in `.hifin/q.md` (which
`hi q` already reads, and which `AGENTS.md` can point to), for a person to
commit. A note that was exported is marked so, and searches show the
repository as its source.

## Privacy and security

- Notes are text written by agents and accepted by people; they can contain
  internal details. They stay on the server, in `notes.jsonl` in its state
  folder, and are served only to devices in the groups allowed to see them.
- Notes are data, not instructions. A note can't run anything, and the skill
  tells agents to treat a note like any other hint: check it when it
  matters. The review step is the defence against a note planted by a
  prompt-injected agent.
- The audit log records proposals, decisions, edits, and retirements with
  who and when.

## Protocol

Signed `/v1` routes: `GET /v1/notes?q=&repo=` searches, `POST /v1/notes`
proposes, `GET /v1/notes/{id}` shows one, `POST /v1/notes/{id}/edit` and
`/retire`. Review decisions arrive through Slack or `hi server notes
accept|reject <id>`.

## Releases

1. Project and team notes, proposals reviewed through `hi ask`, word search,
   `ls`, `show`, `retire`, and the skill rule.
2. Review dates and the weekly report, staleness from `--files`, group
   visibility, and `hi q` using notes.
3. `export`, and embedding search through the team's model if word search
   isn't enough.

## Risks

- **Too many notes.** Agents propose freely; people tire of reviewing. The
  skill asks for notes only about things that cost real time to find out
  and aren't in the code or docs; the report counts proposals per agent.
- **Wrong notes trusted.** Review, evidence, review dates, and staleness
  checks reduce it; they don't remove it.
- **A second place for knowledge** next to `AGENTS.md`, `.hifin/q.md`, and
  the agents' own memories. The rule of thumb in the docs: permanent and
  about the code goes in the repository; changing, environmental, or
  team-wide goes in notes; personal stays in the agent's own memory.

## Open questions

1. Whether accepting project notes should need someone other than the
   agent's own person. Requiring a second person is safer but slower; the
   proposal trusts the person who watched the agent learn it.
2. Whether `hi notes` should also read Claude Code's own memory files and
   offer to promote entries, instead of agents proposing from scratch.
3. Whether team notes should be visible to agents enrolled on their own
   (`hi connect --agent`) by default.

## Findings

Checked on 2026-10-02.

| Product | Shared with the team | Review | Staying true |
|---|---|---|---|
| Claude Code | `CLAUDE.md` through git; auto memory is per machine and per repository | Through git for `CLAUDE.md`; none for auto memory | Never expires; `/doctor prompt-audit` finds stale or conflicting instructions |
| Codex | `AGENTS.md` through git, 32 KiB combined | Through git | Manual |
| Cursor | Project rules (git) and team rules from the dashboard | Admins | Manual |
| Devin Knowledge | Organization-wide, pinned to no repository, one, or all | Devin suggests, a person approves | Manual |
| GitHub Copilot Memory | Repository facts shared; preferences private | Only people with write access create repository facts | Citations checked against the current branch before use; unused for 28 days, deleted |
| Letta | Memory blocks shared across agents, optionally read-only | Programmatic | Programmatic |

Copilot Memory is closest to this proposal, and two of its ideas are worth
taking: checking a fact's citations against the current code before using
it (here, `--files`), and expiring facts nobody uses. A rule that retires
notes no search has returned for 90 days could complement review dates.
Devin's "suggest, a person approves" is the review step proposed here. None
of them have notes that span repositories with group visibility and Slack
review.

## Sources

- https://code.claude.com/docs/en/memory
- https://developers.openai.com/codex/guides/agents-md
- https://cursor.com/docs/rules
- https://docs.devin.ai/product-guides/knowledge
- https://docs.github.com/en/copilot/concepts/agents/copilot-memory
- https://docs.letta.com/guides/agents/memory-blocks
- https://arxiv.org/abs/2602.11988
