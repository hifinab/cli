# `hi net` specification

Status: Released in v0.5.0

Dependencies: NetBird CLI with `--setup-key-file`; existing hostname prompt.

## Goal

Enroll and manage a NetBird peer without placing setup keys in shell history or
process arguments.

## Commands

```text
hi net
hi net status
hi net down
hi net reconnect
```

## Enrollment flow

Bare `hi net`:

1. Verifies that NetBird is installed.
2. Reads the setup key from a no-echo terminal prompt.
3. Explains that the next value is the NetBird device hostname.
4. Shows the machine hostname as the default and accepts an override.
5. Writes the key to a temporary file with mode `0600`.
6. Runs `netbird up --setup-key-file <path> --hostname <name>`.
7. Removes the key file on success, failure, signal, or interruption.

Automation may provide a documented protected key file or environment value.
Repository configuration must never contain the key. The legacy positional-key
form should be removed as a clean security cutover.

## Lifecycle commands

`status`, `down`, and `reconnect` delegate to stable NetBird CLI operations and
preserve output. `reconnect` must not request a setup key for an already enrolled
peer.

## Safety

Do not print the key, include it in errors, retain it in temporary directories,
or pass it as a command argument. Hostname validation occurs before creating the
key file. Temporary-file creation failure stops before NetBird runs.

## Acceptance criteria

1. Terminal entry does not echo the setup key.
2. NetBird receives a setup-key file and explicit hostname.
3. The temporary file is mode `0600` and removed on every exit path.
4. The setup key is absent from process arguments and command previews.
5. Blank hostname input uses the displayed machine hostname.
6. Lifecycle subcommands return NetBird's observable success or failure.
