# `hi doctor` specification

Status: Draft

Dependencies: existing workstation and Strix verification helpers.

## Goal

Inspect machine state without mutation and give humans and automation one
consistent definition of healthy.

## Commands

```text
hi doctor
hi doctor --json
```

## Checks

The initial check set covers:

- Supported operating system and architecture
- Pending reboot and active user groups
- `hi` version and `~/.local/bin` path availability
- Docker package, Compose command, and service
- NetBird command, service, and connection status
- GitHub CLI and authentication status
- OMP command and authentication availability without exposing accounts or keys
- uv, pipx, Node.js, npm, tmux, and btop
- Strix ROCm packages, devices, commands, and GPU detection when applicable

Each check has a stable identifier, status (`pass`, `warn`, `fail`, or
`pending`), concise evidence, and an exact repair command when one is safe.

## Output

Human output is grouped and actionable. JSON output contains schema version,
timestamp, machine profile, aggregate status, and the same checks. JSON field
names and status meanings are a compatibility contract.

## Safety

Doctor never invokes sudo, starts services, installs packages, changes files,
opens login flows, or prints credentials. Timeouts bound commands that can hang.
Missing optional hardware is not a failure unless the active profile requires
it.

## Acceptance criteria

1. Running doctor causes no machine-state changes.
2. Human and JSON modes report the same check results.
3. Every failure has evidence and, when possible, one repair action.
4. Strix-only checks run only for the Strix profile or detected Strix setup.
5. Output contains no environment values, tokens, setup keys, or cookies.
6. Exit status distinguishes healthy, warning/pending, and failed states.
