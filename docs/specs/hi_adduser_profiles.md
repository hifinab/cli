# `hi adduser` profiles specification

Status: Draft

Dependencies: existing `hi adduser <name>` behavior and Ubuntu group management.

## Goal

Make repeatable account roles explicit without silently granting privileges.

## Commands

```text
hi adduser <name>
hi adduser <name> --developer
hi adduser <name> --admin
```

The existing default remains GPU access through render and video groups.
`--developer` adds the documented development groups and shell configuration.
`--admin` includes developer behavior plus sudo access. Profile group lists must
be declared centrally and printed before mutation.

SSH key installation, if added, requires an explicit file argument and validates
its public-key form. The command never creates or imports private keys.

## Flow

Validate username, profile, required groups, existing account state, and optional
key before sudo. Print the exact account, groups, and files to be changed; ask
for confirmation; then run Ubuntu's interactive account creation and apply the
selected profile.

## Safety

Profiles are mutually exclusive in one invocation. Existing users are not
modified by `adduser`; a separate future command would own that behavior.
Cancellation and preflight failure cause no changes. Output never includes
passwords, private keys, or shadow data.

## Acceptance criteria

1. Default behavior remains compatible with the existing GPU-user command.
2. Developer and admin privileges are listed before confirmation.
3. Admin is the only profile that grants sudo access.
4. Invalid users, groups, profiles, and keys fail before mutation.
5. Existing accounts are not changed accidentally.
6. Completion reports required logout or login steps.
