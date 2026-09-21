# `hi services` specification

Status: Draft

Dependencies: service and hardware detectors introduced by `hi doctor`.

## Goal

Show one compact, read-only view of managed runtime services without duplicating
health logic.

## Command

```text
hi services
hi services --json
```

## Service view

The initial view includes Docker, NetBird, SSH, pending reboot state, and Strix
GPU readiness when applicable. Each row reports stable identifier, installed
state, enabled state, active state, concise detail, and a repair command only
when safe.

`hi services` is a focused projection of doctor results. It must call shared
detectors rather than reinterpret command output independently.

## Safety and output

The command never starts, stops, enables, or restarts a service. JSON uses a
versioned schema and reports the same facts as human output. Missing services
are distinguishable from installed but inactive services.

## Acceptance criteria

1. Running the command causes no service or filesystem mutation.
2. Results agree with `hi doctor` for every shared detector.
3. Missing, disabled, inactive, active, and failed states are distinct.
4. JSON and human modes represent the same service state.
5. Output contains no credentials or private configuration values.
