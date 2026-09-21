# `hi repo doctor` specification

Status: Draft

Dependencies: `.hifin/template.json` and the template manifests introduced by
`hi init`.

## Goal

Detect repository-template drift and broken project contracts without changing
the repository by default.

## Commands

```text
hi repo doctor
hi repo doctor --json
hi repo doctor --fix
```

## Checks

Checks are selected by template identity and cover required instructions,
documentation and specification paths, lockfiles, generated-file hashes or
managed regions, declared format/test/build commands, broken local documentation
links, accidentally tracked secrets, and oversized tracked data.

A check distinguishes user-owned drift from safely regenerable content. Unknown
or missing template metadata is reported; it is never guessed from filenames.

## Fix mode

Inspection is the default. `--fix` first prints a plan and requests confirmation.
It may only recreate missing managed files or deterministic managed regions.
Conflicting user-owned content remains an error with a manual resolution path.

## Safety

Do not modify Git state, stage files, create commits, fetch remotes, execute
project code during ordinary inspection, or reveal suspected secret values.
Secret findings report path and rule only.

## Acceptance criteria

1. Inspection is side-effect free.
2. Human and JSON output share stable check identifiers and statuses.
3. Missing metadata is not inferred silently.
4. Fix mode changes only explicitly managed content after confirmation.
5. User-owned conflicts are never overwritten.
6. Secret checks never print the matched secret.
