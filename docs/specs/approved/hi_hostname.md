# `hi hostname` specification

Status: In development

Implementation progress: hostname prompting, validation, `hostnamectl`, and
`/etc/hosts` updates exist in workstation installation; the standalone
`hi hostname` command is pending.

Dependencies: existing hostname validation and `/etc/hosts` update behavior.

## Goal

Read or change the system hostname without rerunning workstation installation.

## Commands

```text
hi hostname
hi hostname <name>
```

Bare invocation prints the current hostname. The named form validates the value,
shows the current and requested names, asks for confirmation, obtains sudo once,
updates the hostname through `hostnamectl`, and updates matching host entries
without deleting unrelated aliases or comments.

## Validation

Accept dot-separated DNS-style labels containing letters, digits, and internal
hyphens, with a 64-character total limit and 63-character label limit. Reject
empty labels, leading/trailing hyphens, whitespace, and control characters.

## Safety

A matching name is a successful no-op. Validate and render the proposed change
before sudo. Write `/etc/hosts` through a secured temporary file and atomic
replacement. Failure to update either hostname or hosts is reported explicitly;
do not claim full success after a partial change.

## Acceptance criteria

1. Bare invocation prints only the current hostname in script-friendly form.
2. Invalid values fail before sudo or mutation.
3. Cancellation leaves hostname and hosts unchanged.
4. Existing aliases and comments survive the hosts update.
5. A matching hostname causes no writes.
6. Successful output states whether a new login or reboot is needed.
