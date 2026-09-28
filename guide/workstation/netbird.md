---
title: NetBird network
description: Enroll a machine in the Hifin NetBird network with a setup key that never lands in shell history, and manage the connection afterwards.
---

[NetBird](https://netbird.io) connects Hifin machines in a private network.
`hi net` enrolls a machine without exposing the setup key: it never appears in
shell history, process lists, or output.

`hi install` installs NetBird first; `hi net` says so if it is missing.

## Enroll a machine

```sh
hi net
```

1. Paste the setup key when asked. Nothing is shown as you type.
2. Choose the machine's name in the network. Press Enter to use its hostname.

`hi` writes the key to a temporary file only you can read, runs
`netbird up --setup-key-file … --hostname …`, and deletes the file whether
enrollment succeeds, fails, or is interrupted.

> The old form `hi net <setup-key>` is refused, because it would put the key
> in your shell history.
{: .warning}

## Enroll from a script

Provide the key through a protected file or an environment variable:

```sh
chmod 600 netbird-key.txt
hi net --setup-key-file netbird-key.txt

HI_NETBIRD_SETUP_KEY="$KEY_FROM_YOUR_SECRET_STORE" hi net
```

`hi` refuses a key file that other users can read.

## Manage the connection

```sh
hi net status       # netbird status, unchanged
hi net down         # disconnect
hi net reconnect    # disconnect and reconnect with the stored enrollment
```

`reconnect` does not need the setup key again.
