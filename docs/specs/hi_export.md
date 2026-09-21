# `hi export` specification

Status: Draft

Dependencies: `hi doctor`, `hi services`, and their shared detectors.

## Goal

Create a portable, redacted machine-state bundle for support and remote review.

## Commands

```text
hi export
hi export --output <path>
```

## Contents

The versioned JSON report contains platform and architecture, installed `hi`
version, package and tool versions, service state, pending reboot, user group
readiness, NetBird connection state without peer secrets, and Strix hardware
results when applicable. Every fact records its detector and collection error.

The default filename includes a UTC timestamp and is created in the current
directory with mode `0600`.

## Redaction

Never collect environment variables, command histories, setup keys, provider
tokens, cookies, browser data, SSH material, password databases, full process
arguments, or arbitrary configuration files. Usernames, hostnames, IP addresses,
and repository remotes are excluded unless a future explicit flag documents the
privacy impact.

## Safety

Export is read-only and never invokes sudo. Write to a temporary sibling file,
flush it, and rename atomically. Refuse to overwrite an existing output path.

## Acceptance criteria

1. Export uses the same check results as doctor and services.
2. The report has a documented schema version and UTC creation time.
3. Default and explicit output paths are mode `0600`.
4. Existing files are never overwritten.
5. A fixture containing representative secrets produces no leaked value.
6. Partial collection failures are represented without discarding other facts.
