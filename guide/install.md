---
title: Install hi
description: Install the hi binary on Linux, check it works, and sign in to a compute provider.
---

`hi` is a single static binary for Linux on amd64 and arm64. Installing it
needs no root access; it goes to `~/.local/bin/hi`.

## Install the CLI

```sh
curl -fsSL https://hifin.sh/install.sh | sh
```

The installer downloads the latest release, checks its SHA-256 checksum, and
installs it. If `~/.local/bin` is not on your `PATH` yet, it adds it to
`~/.profile` for new shells and prints the full path to use right away:

```text
Installed hi to /home/you/.local/bin/hi
Added /home/you/.local/bin to PATH in /home/you/.profile for new shells.
Run it now without opening a new shell:
  ~/.local/bin/hi install
```

Check it:

```sh
hi version
```

## Set up a new workstation in one step

On a fresh Ubuntu 26.04 machine, pass the `hi` command to run after
installing with `sh -s --`. This installs `hi` and immediately opens the
`hi install` menu in the same terminal, so you can pick the tools:

```sh
curl -fsSL https://hifin.sh/install.sh | sh -s -- install
```

See [Set up a workstation](/guide/workstation/) for the tools on offer.

## Installer options

| Variable         | Effect                                                         |
|------------------|----------------------------------------------------------------|
| `HI_INSTALL_DIR` | Install somewhere other than `~/.local/bin`                    |
| `HI_VERSION`     | Install a specific release, such as `v0.7.0`, instead of latest |

```sh
curl -fsSL https://hifin.sh/install.sh | HI_VERSION=v0.7.0 sh
```

## Sign in to a compute provider

To rent machines with `hi compute`, sign in to at least one provider. The
provider pages walk through each step, including the SSH key setup that
shells and tunnels need.

```sh
hi login colab      # Google Colab: opens a browser sign-in
hi login hf         # Hugging Face: runs hf auth login
hi compute providers
```

```text
colab    ready
hf       ready
```

- [Set up Google Colab](/guide/compute/colab/)
- [Set up Hugging Face Jobs](/guide/compute/hugging-face/)

`hi install` also offers both providers' command-line tools, the Colab CLI
and the `hf` CLI (`hi install colab hf`). If you only installed `hi` itself, `hi login` tells you how
to install the one it needs.

## Update

```sh
hi update            # install the latest release
hi update --check    # only say whether a newer release exists
hi update --version v0.7.0
hi update --restart  # and restart a hi server service without asking
```

`hi update` downloads the release for your machine, checks its SHA-256
checksum, and replaces the binary in one step; if anything fails, the old
binary stays as it was. `--version` can also go back to an older release.
After updating, refresh any [agent skills](/guide/reference/skill/) you wrote
with `hi skill update`.

`hi update` arrived in v0.7.1. Older versions update once with the installer:

```sh
curl -fsSL https://hifin.sh/install.sh | sh
```

## Remove

Delete the binary and its local state:

```sh
rm ~/.local/bin/hi
rm -r ~/.local/state/hi ~/.config/hi
```
