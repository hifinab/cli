# `hi apply` specification

Status: Draft

Dependencies: stable `hi doctor` detectors and independently usable mutation
commands for every supported resource.

## Goal

Converge a machine from a strict, versioned declaration while preserving a clear
plan, observable component behavior, and honest failure semantics.

## Commands

```text
hi apply --dry-run
hi apply
hi apply --file <path> --dry-run
hi apply --file <path>
```

The default file is `hi.yaml` in the current directory.

## Initial schema

```yaml
version: 1
machine:
  profile: workstation
  hostname: quant-01

software:
  docker: true
  netbird: true
  github-cli: true
  omp: true

hardware:
  profile: strix

auth:
  github: required
```

The parser rejects unknown fields, duplicate keys, invalid enum values, and
unsupported schema versions. Missing optional sections mean unmanaged, not
false or remove. Destructive absence semantics are prohibited.

## State and planning

Each field maps to one detector and, when mutable, one independently callable
component operation. Planning reads actual state, compares it with declared
state, and emits ordered operations. It must not declare convergence based only
on files written by a previous run.

The plan includes resource, observed state, desired state, action, privilege
requirement, restart/reboot impact, and dependency. Stable ordering resolves
prerequisites before dependents—for example host prerequisites before software,
software before services, and provider CLIs before delegated authentication.

`--dry-run` performs parsing, detection, validation, and planning only. It never
requests sudo, opens authentication, installs packages, writes files, restarts
services, or pulls images.

## Execution

Before mutation, print the complete plan and request confirmation when attached
to a terminal. Execute one operation at a time and verify its postcondition with
the same detector used for planning. Stop after the first failed operation.

The result reports completed, failed, skipped, and remaining operations. Package,
service, account, firmware, and authentication changes are not described as
rolled back when reliable rollback does not exist. A later run must safely
re-detect state and continue from remaining drift.

## Secrets and authentication

Secrets never appear as literal YAML values. Enrollment references protected
files or named environment inputs whose values are not printed or persisted.
Interactive GitHub and OMP authentication remains delegated to their native
commands. Non-interactive apply reports required login as unresolved unless an
explicit supported credential source is configured outside the declaration.

## File and ownership safety

Normalize the configuration path before use. Reject group/world-writable secret
references and unsafe ownership transitions. Generated system files use secured
temporary siblings and atomic replacement. Existing unmanaged configuration is
reported as a conflict rather than overwritten.

## Schema evolution

The top-level version is mandatory. New optional fields may be added only when
older binaries reject or safely ignore them according to a documented schema
rule; the initial rule is strict rejection. Breaking meaning requires a new
schema version and an explicit migration preview.

Do not add a generic package-manager or provider plug-in layer before repeated
supported resources demonstrate a stable common contract.

## Acceptance criteria

1. Unknown, duplicate, or invalid configuration fails before state detection or
   mutation.
2. Dry-run is side-effect free and shows the exact dependency-ordered plan.
3. No-op declarations produce an empty plan and successful result.
4. Apply verifies each operation's postcondition before continuing.
5. Failure stops dependent operations and reports completed and remaining work.
6. A repeated run resumes from actual drift without replaying completed work.
7. Secret values never appear in YAML, output, logs, or process previews.
8. Unmanaged resources remain unchanged.
9. Schema-version errors include the supported versions and no migration occurs
   implicitly.
