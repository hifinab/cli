# `hi user` specification

Status: In development

Implementation progress: the existing `hi adduser <name>` flow and render/video
group assignment are released. The `hi user` command structure, user and group
inspection, login history, sudo management, and account removal are pending.

Dependencies: existing `hi adduser <name>` behavior, Ubuntu user and group
management, and the system login records.

## Goal

Provide one command namespace for inspecting users and login activity, managing
group and sudo access, creating users, and removing users safely.

## Commands

```text
hi user add <name>
hi user add <name> --sudo
hi user sudo <name>
hi user remove <name>
hi user groups <name>
hi user groups <name> --add <group>
hi user groups <name> --remove <group>
hi user list
hi user log
hi user log <name>
```

`hi user add <name>` creates an account with GPU access through the render and
video groups. `--sudo` also adds the new account to the sudo group.

`hi user sudo <name>` grants sudo access to an existing account. It does not
otherwise change the account.

`hi user remove <name>` removes the account and its home directory. It does not
remove user-owned files outside the home directory.

`hi user groups <name>` lists the account's primary and supplementary groups.
`--add <group>` adds an existing group, and `--remove <group>` removes a
supplementary group. Removing the account's primary group is rejected.

`hi user list` shows regular user accounts with their username, UID, home
directory, login shell, group memberships, and sudo status. System accounts are
excluded.

`hi user log` shows which users are currently logged in and the most recent
login recorded for each regular user. `hi user log <name>` shows the retained
login sessions for one user, including login time, logout time or active status,
terminal, remote host when recorded, and session duration.

## Flow

Read-only commands do not request sudo when the required system records are
already readable. They report unavailable or missing login data explicitly
rather than presenting it as no activity.

Mutating commands validate the username, account state, and required groups
before sudo. They print the exact account and groups to be changed before
mutation. Account creation runs Ubuntu's interactive account creation and then
adds the selected groups. Granting sudo validates that the account already
exists before adding it to the sudo group.

Group changes validate that both the account and group exist. Adding an existing
membership or removing an absent membership reports that no change is needed.

Removal prints a destructive warning that names both the account and its home
directory. The user must explicitly approve the removal prompt before the
account or home directory is deleted.

## Safety

Adding an existing account, granting sudo to a missing account, and changing
groups for a missing account fail without changes. Granting sudo to an account
that already has it reports that no change is needed.

Group removal cannot remove the account's primary group. Login output never
invents unavailable timestamps, remote hosts, logout events, or durations.

Removal fails before mutation if the account does not exist or its home
directory cannot be safely identified. Cancellation and preflight failure cause
no changes. Output never includes passwords, private keys, or shadow data.

## Acceptance criteria

1. `hi user add <name>` preserves the existing GPU-user behavior.
2. `hi user add <name> --sudo` creates the account and grants sudo access.
3. `hi user sudo <name>` grants sudo access to an existing account.
4. `hi user remove <name>` names the account and home directory in a destructive
   warning and requires explicit confirmation.
5. Confirmed removal deletes the account and its home directory.
6. `hi user groups <name>` distinguishes the primary group from supplementary
   groups.
7. Group addition and removal produce the requested membership and treat an
   already-satisfied request as a no-op.
8. The primary group cannot be removed with `hi user groups`.
9. `hi user list` shows regular users and excludes system accounts.
10. `hi user log` identifies current logins and each user's most recent recorded
    login.
11. `hi user log <name>` shows the retained session details for that user.
12. Missing or unavailable login data is distinguished from no login activity.
13. Invalid usernames, account states, groups, and options fail before mutation.
14. Cancellation causes no changes.
15. Completion reports required logout or login steps when relevant.
