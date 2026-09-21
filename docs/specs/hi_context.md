# `hi context` specification

Status: Draft

Dependencies: `hi doctor` detectors and `hi init` template metadata.

## Goal

Produce concise, redacted context that a human or agent can use without scanning
the machine or repository repeatedly.

## Commands

```text
hi context
hi context --json
```

## Contents

Context includes operating system and architecture, machine profile, relevant
runtime and tool versions, managed service summary, hardware profile, repository
root, template and version, current Git branch, clean/dirty state, and configured
remote host names without embedded credentials.

Human output is intentionally compact. JSON uses a versioned schema and the same
underlying facts. Unavailable facts carry an explicit reason instead of guessed
values.

## Redaction and safety

Never include environment variables, tokens, setup keys, cookies, credential
paths, remote URL userinfo, file contents, diffs, commit messages, or process
arguments. The command is read-only, invokes no sudo, and performs no network
requests.

## Acceptance criteria

1. Human and JSON output describe the same context.
2. Repository information is omitted cleanly outside a repository.
3. Template details come only from validated metadata.
4. Git remotes are normalized without credentials.
5. Secret-bearing fixtures produce no leaked values.
6. The command performs no writes or network calls.
