# `hi box` specification

Status: Approved (2026-10-02). Release 1 is planned as v0.23.0. Decided
before approval: the name `hi box`, rootless Podman with `crun` (Docker as
the fallback, no Docker Sandboxes), `devcontainer.json` with
`customizations.hi`, and Claude Code's token added at the proxy outside the
box. Codex's ChatGPT sign-in still goes into the box in the first version:
its traffic to `chatgpt.com` is encrypted end to end, and whether Codex
accepts another base address for it is untested.

Dependencies: `hi install` (Podman as a new tool), the v0.1.0 workstation
setup (ROCm, render and video groups), `hi server` (policy, audit, Slack,
agent devices), `hi compute` for remote sandboxes, and `hi init` templates.

## Goal

One primitive for running work somewhere it can't hurt you: a **sandbox** is
a named, isolated environment with one project in it, no credentials, and a
network that only reaches what is allowed. It is local by default, can run on
a rented GPU instead, and is governed by the same policy, approvals, and
audit log as `hi compute`.

The first user is an AI agent running unattended: Claude Code or Codex with
approval prompts off, which is only safe inside a boundary. The same
primitive also serves people:

- **Agents, unattended.** `hi box claude "fix the flaky test"` runs to
  the end and leaves a branch to review.
- **Agents, in parallel.** Five sandboxes, each on its own worktree, try five
  approaches; you keep the best branch.
- **Untrusted code.** Try a teammate's branch, a downloaded repository, or a
  new package without it seeing `~/.ssh` or your tokens.
- **A clean environment.** Reproduce a bug or a CI failure from the
  project's image instead of from your machine's state.
- **A new teammate's first day.** The project's template defines the
  sandbox, so `hi box shell` gives a working environment at once.
- **A preview for the team.** A port in a sandbox gets a private HTTPS URL
  on NetBird, so a teammate can open the app you or an agent just built.
- **The same thing on a big GPU.** `--remote h100` starts the sandbox on a
  rented machine through `hi compute`, with the same image and network rules.

## What exists already

Checked 2026-10-01; the full findings and sources are at the end.

- **The agents' own sandboxes are good but partial.** Claude Code (bubblewrap
  on Linux) and Codex (bubblewrap and seccomp) confine the agent's shell
  commands and offer a domain allowlist. They do not hide credentials by
  default: both can read `~/.ssh`, `gh` tokens, and their own sign-in. Claude
  Code's MCP servers and hooks run outside its sandbox, and its docs say to
  run `--dangerously-skip-permissions` only inside a container or VM.
- **Docker Sandboxes (`sbx`)** is the closest product: a microVM per agent,
  deny-by-default egress, credentials added by a host proxy so they never
  enter the VM, and a clone mode. Its GPU support is NVIDIA-only VFIO, so it
  cannot use the Strix Halo iGPU, and it needs a Docker sign-in.
- **Hosted services** (boxd, Fly's Sprites, E2B, Daytona, Modal, Vercel) all
  converge on the same ideas: named sandboxes, checkpoints and forks,
  deny-by-default egress, credentials kept out of the sandbox, and a URL per
  port. Only Daytona and Modal offer GPUs, and only Daytona AMD.
- **Open-source wrappers** (container-use, packnplay, yolobox, claudebox,
  Trail of Bits' dev container) either mount the user's credentials into the
  container or have no egress control. None handles AMD GPUs.

What hi adds: the whole agent process is contained; credentials are absent
rather than listed one by one; egress fails closed; the AMD GPU works;
both agents use one command; and blocked requests, approvals, and activity go
through the team's server and Slack, which nothing else offers.

## Commands

```text
hi box claude [prompt]          start Claude Code in a sandbox for this project
hi box codex [prompt]           the same for Codex
hi box shell                    a shell in the project's sandbox
hi box run -- <command>         run one command and exit with its status
hi box ls                       sandboxes, state, agent, branch, age
hi box attach <name>            follow or take over a running agent
hi box diff <name>              what the sandbox changed
hi box stop|rm <name>
hi box checkpoint|restore <name> [checkpoint]
hi box fork <name> [n]
hi box allow <name> <domain>    widen the network for one sandbox
hi box url <name> <port>        a private HTTPS URL on NetBird
```

Options on start: `--name`, `--worktree` (the default for agents),
`--gpu` (the local AMD GPU), `--remote <hardware>`, `--network
open|dev|locked`, `--idle <duration>`, `--max <duration>`, `--image`.
With a prompt, the agent runs to completion in the background; without one
it is interactive. The name defaults to the project and a counter.

## Design

### Runtime

Rootless Podman with the `crun` runtime, added to `hi install` as a tool
(`hi install podman`, from v0.22.2; it also installs `uidmap` and `passt` and
adds a subordinate ID range when the user has none). It needs no daemon, maps
the user's UID into the container (`--userns=keep-id`), and an escape lands
as the user, not root. Docker is the fallback when Podman is missing; the
`docker` group is root-equivalent, so hi says so.

Two tiers, stated plainly in the output:

- **With `--gpu`**: a container with `/dev/kfd` and the render node, via
  `--group-add keep-groups`. The kernel is shared, and the GPU driver widens
  the attack surface. VM-grade isolation is impossible here: gVisor's GPU
  support is NVIDIA-only, and VFIO would take the only GPU away from the host.
- **Without the GPU**: the same container by default; gVisor (`runsc`) or a
  microVM as a stronger option later.

Every sandbox runs as a non-root user with `--cap-drop=ALL`,
`no-new-privileges`, and pid and memory limits.

### Image

One hi base image with Claude Code, Codex, git, uv, and Node.js, and a ROCm
variant built for gfx1151, both pinned by digest. A project can name its own
image or Dockerfile. hi reads a safe subset of an existing
`.devcontainer/devcontainer.json` (`image`, `build.dockerfile`,
`containerEnv`, and `postCreateCommand` run inside), and ignores the rest:
`initializeCommand` runs on the host, and `runArgs`, `mounts`, `privileged`,
and `capAdd` would widen the sandbox. `hi init` templates gain a sandbox
definition.

### Files

- The project, or a new git worktree of it, at the same absolute path, so a
  worktree's link to the main `.git` resolves.
- `.git/hooks` and `.git/config` mounted read-only on top; the agent commits
  but cannot plant hooks.
- A volume per sandbox for the agent's own settings (`CLAUDE_CONFIG_DIR`,
  `CODEX_HOME`).
- Nothing else from the home folder.

Work comes back as a branch. `hi box diff` shows it, flagging changes to
files that run on the host later: `Makefile`, `package.json` scripts,
`.envrc`, `.vscode/tasks.json`, CI workflows. Pushing happens on the host,
after review.

### Credentials

No SSH keys, GitHub token, cloud keys, or hi device key ever enter a
sandbox. The agent needs its own sign-in, in two steps:

1. First version: only the agent's token goes in (`claude setup-token` gives
   `CLAUDE_CODE_OAUTH_TOKEN`; Codex an API key or its own `auth.json`).
2. Later: the token stays outside too. The sandbox gets a placeholder, and
   hi's proxy adds the real header for the agent's API host only, as Docker
   Sandboxes, Sprites connectors, and boxd's host-bound secrets do. Pointing
   `ANTHROPIC_BASE_URL` at the proxy over plain HTTP avoids a CA inside the
   sandbox. This works with a Claude subscription too (open question 4).

The same proxy can later broker GitHub reads, Hugging Face downloads, and
`hi compute` itself: a sandboxed agent requests a GPU through a socket to hi
on the host, labelled `iman via sandbox fix-flaky-test` and approved like
any agent request, without holding a device key.

### Network

The sandbox sits on a network with no route out (`podman network create
--internal`). Its only way out is an HTTP CONNECT proxy built into `hi`,
which checks each host against an allowlist and resolves names itself. Tools
that ignore `HTTPS_PROXY` fail instead of escaping.

| Preset   | Allows                                                                                |
|----------|----------------------------------------------------------------------------------------|
| `locked` | The agent's API and sign-in hosts only                                                 |
| `dev`    | Default: `locked` plus package registries for the project's languages and GitHub reads |
| `open`   | Everything, still logged                                                               |

A repository can ask for more domains in `customizations.hi` of its
`devcontainer.json`, but cannot grant
them: the first use asks the user once, as direnv does, or the server policy
allows them per group. When an agent hits a blocked domain, the proxy records
it and, with a server, posts it to Slack: *"fix-flaky-test (iman via Claude
Code) wants files.pythonhosted.org — Allow once / Allow for project /
Deny"*. The rule reloads without restarting the sandbox.

Domain fronting can get past a host check without TLS inspection; the docs
say so. The allowlist limits accidents and casual exfiltration; it doesn't
make a hostile agent harmless. The agent can always send the project to an
allowed host.

### Lifecycle

- Background agents log to the box's container log (`hi box attach`); `attach`
  follows the log or opens the agent's terminal through tmux in the sandbox.
- `--idle` stops a sandbox whose agent and GPU have been idle, sharing the
  rule from [hi_compute_idle.md](../ideas/hi_compute_idle.md); `--max` caps it.
- Finished agent sandboxes are kept until `rm`, so the work can be checked.
  `hi box ls` shows what each one changed.
- With a linked Slack account, the owner gets a message when an agent
  finishes or gets stuck, with the diff summary.

### Checkpoints and forks

Filesystem only, as Sprites does: memory checkpoints need rootful CRIU and
are untested with the AMD GPU. `checkpoint` copies the sandbox's writable
layer and worktree (a reflink copy where the filesystem supports it, a plain
copy on ext4); up to 10 per sandbox, restored only after confirming. Before
an agent starts, hi takes one automatically. `fork <name> 3` makes three
copies on new branches, for trying approaches in parallel.

### URLs

`hi box url <name> 5173` maps a port to
`https://5173-<name>.<machine>.<netbird domain>` through a small reverse
proxy on the workstation, reachable only over NetBird. Making it public is a
separate action that goes through approval. Internal TLS and DNS over
NetBird is the fiddly part.

### Remote sandboxes

`--remote <hardware>` starts the sandbox's image on a rented machine through
`hi compute`, with the same mounts copied in, the same egress proxy running
on the machine, and `--max` as always. RunPod and Hugging Face already run
images; Shadeform runs them on its VMs; Colab cannot. It depends on pinned
images ([hi_compute_serve_pinning.md](../ideas/hi_compute_serve_pinning.md)) and on
runs over SSH ([hi_compute_ssh_run.md](../ideas/hi_compute_ssh_run.md)). The image
must suit the GPU: one Dockerfile with a build argument gives a `rocm` tag
for the workstation and a `cuda` tag for the cloud.

### With `hi server`

- Policy per group: allowed presets, extra domains, `--gpu`, and remote
  hardware.
- Every sandbox start, network decision, and stop goes to the audit log;
  running sandboxes appear in `hi server live` and the Slack App Home.
- `hi box share <name> <user>` gives a teammate a shell over NetBird SSH,
  removes the sandbox's agent token first, and records it.

## As built in release 1

Where v0.23.0 differs from the design above:

- **Agents come from the host.** The base image (Ubuntu 24.04 with git,
  Python, uv, Node.js, ripgrep, and build tools, built locally on first use
  and tagged by its Containerfile's hash) holds no agent. `claude` and
  Codex's package folder are mounted read-only from the host, so a box runs
  the versions the user has and nothing is downloaded per box.
- **The proxy is a second container**, `hi-box-<name>-proxy`, running the hi
  binary mounted read-only. It sits on the box's `--internal` network at a
  fixed address and on the engine's normal network. The box gets
  `--dns 127.0.0.1` and Podman networks `--disable-dns`, so name lookups in
  the box fail and only the proxy resolves names.
- **Claude Code's token stays outside from the start.** The proxy's second
  listener swaps the placeholder for the host's sign-in (re-read per
  request) or a token from `hi box token claude`. Codex's `auth.json` is
  copied into the box's home folder.
- **State** is in `~/.local/state/hi/box/<name>/`: `box.json`, the
  worktree, the box's home folder, `allow`, and `network.log`.
- **attach** follows a background agent's output with the engine's logs, and
  attaches to an interactive box's terminal; tmux inside the box is not used.
- **postCreateCommand** runs before the box's command each time the box
  starts, not once.
- Not in release 1: checkpoints, forks, URLs, `--idle`, `--max`,
  `--remote`, and anything from `hi server`.

## Releases

1. **Local agent sandboxes.** Podman in `hi install`; `claude`, `codex`,
   `shell`, `run`, `ls`, `attach`, `diff`, `stop`, `rm`; worktrees; no
   credentials but the agent's token; the built-in proxy with presets;
   `--gpu`; read-only git hooks.
2. **The team layer.** Slack approvals for blocked domains, audit, policy,
   App Home, finished-agent messages, and the token kept at the proxy.
3. **More of the hosted ideas.** Checkpoints, forks, URLs, `--idle`,
   sharing.
4. **Remote.** `--remote` through `hi compute`.

Each step is useful alone, and step 1 is roughly the size of a minor
release.

## Risks

- The agent can still send the project and its own token to allowed hosts.
- Files the agent writes and a person later runs on the host remain a way
  out; `diff` flags them, but review is the defence.
- A rootless escape lands as the user, who owns `~/.ssh`.
- `--gpu` exposes the GPU driver to the sandbox.
- Docker as the fallback runs containers through a root daemon.

## Open questions

1. ~~Name.~~ Decided on 2026-10-02: `hi box`.
2. ~~Whether rootless Podman with ROCm works on gfx1151.~~ Yes, tested on
   aiw11 on 2026-10-02 with Podman 5.7.0 and
   `kyuz0/amd-strix-halo-toolboxes:rocm-10.0`: with
   `--userns=keep-id --device /dev/kfd --device /dev/dri --group-add
   keep-groups`, `rocminfo` in the container finds the gfx1151 Radeon 8060S,
   the process runs as the user, and files it writes belong to the user.
   This needs the `crun` runtime: with `runc`, which Docker's packages
   install and Podman then picks, `keep-groups` drops the `render` group and
   ROCm can't open `/dev/kfd`. `hi install podman` installs `crun`.
3. ~~Whether Codex honours `HTTPS_PROXY`.~~ Yes, tested on 2026-10-02 with
   Codex 0.159.3 signed in with ChatGPT: every connection went through the
   proxy (`chatgpt.com` and `ab.chatgpt.com`), and with an unreachable proxy
   it failed rather than connecting directly. Token refresh, not seen in
   the test, likely also needs `auth.openai.com`.
4. ~~Whether a subscription sign-in can be added at the proxy.~~ Yes for
   Claude Code, tested on 2026-10-02 with Claude Code 2.1.287 on a Claude
   subscription: with an empty home folder, `CLAUDE_CODE_OAUTH_TOKEN` set to
   a placeholder, and `ANTHROPIC_BASE_URL` pointing at a plain-HTTP reverse
   proxy that replaced the placeholder with the real token for
   `api.anthropic.com`, it answered and ran tools. Its other connections
   (straight to `api.anthropic.com`, bypassing the base URL) only ever
   carried the placeholder, and blocking them all changed nothing. So the
   real token never enters the box. The proxy must keep its token fresh:
   access tokens expire, so it either re-reads the host's
   `~/.claude/.credentials.json` per request or holds a long-lived token
   from `claude setup-token`.
5. ~~Docker Sandboxes.~~ Decided on 2026-10-02: not used. Their microVM is
   stronger than a container, but they have no AMD GPU support and tie the
   feature to Docker's product. A box without the GPU can later run under
   gVisor or a microVM behind the same commands.
6. ~~Where the box definition lives.~~ Decided on 2026-10-02:
   `devcontainer.json`, the standard that VS Code, Codespaces, JetBrains,
   and the devcontainer CLI read. hi reads its safe fields (`image`,
   `build`, `containerEnv`, `postCreateCommand`) and ignores, with a note,
   the ones that run on the host or widen the box (`initializeCommand`,
   `runArgs`, `mounts`, `privileged`, `capAdd`). hi's own settings, such as
   extra domains, the network preset, and the GPU, go under
   `customizations.hi` in the same file. No `.hifin/box.json`.

## Findings

### The agents' built-in sandboxes

| | Claude Code | Codex |
|---|---|---|
| Mechanism on Linux | bubblewrap and socat, optional seccomp | bubblewrap and seccomp (Landlock legacy) |
| What is confined | Bash, PowerShell, and Monitor commands; not file tools, MCP servers, or hooks | The agent's commands |
| Writes | Working directory, temp, `--add-dir`; `.git/hooks`, `.git/config`, `.claude`, shell rc files protected | Workspace; `.git`, `.codex`, `.agents` read-only |
| Reads | Whole machine unless denied per path; "there is no built-in credential deny list" | Default reads `~/.codex/auth.json` and lists `~/.ssh` (tested here) |
| Network | Proxy with `allowedDomains`, `strictAllowlist`; no TLS inspection | Off by default; optional domain proxy |
| Unattended | Docs: run `--dangerously-skip-permissions` only in a container, VM, or sandbox runtime | `--yolo` meant for setups sandboxed outside Codex |

Anthropic's reference dev container firewall uses iptables on IPs resolved
once, needs `NET_ADMIN`, and leaves DNS and SSH open to any host; it is not a
model to copy.

### Services

| Service | Isolation | Persistence | Egress | Credentials | GPU |
|---|---|---|---|---|---|
| boxd | KVM microVM | Snapshots, 10 checkpoints, fork in <200 ms, hibernate | Domain allowlist | Host-bound secrets: the VM sees a placeholder | No |
| Fly Sprites | microVM | Filesystem checkpoints; warm and cold sleep | DNS allowlist | Connectors through a gateway | No |
| E2B | Firecracker | Pause with memory, kept until killed | CIDR and SNI rules | Header injection per host | No |
| Daytona | Container or VM | Snapshots, fork a running VM, auto-stop and archive | Domain and CIDR lists | Secrets | NVIDIA and AMD MI355X |
| Modal | gVisor or VM | Filesystem and memory snapshots, fork | CIDR and SNI allowlist | `modal.Secret` | NVIDIA |
| Docker `sbx` | microVM | Kept after exit; clone mode | Deny by default, presets | Host proxy injects headers | NVIDIA VFIO, experimental |
| Vercel | Firecracker | Snapshot on stop, resume on next call | Policies | Brokering | No |
| Coder | Template | Template | Per-process firewall with audit | Templates | Template |

### Local facts

Checked on the workstation: Ubuntu 26.04, kernel 7.0, Docker 29.6, Claude
Code 2.1.287, Codex 0.159.3. bubblewrap works under Ubuntu's user-namespace
restriction. Podman is not installed. `/dev/kfd` and the render node belong
to the `render` group.

## Sources

- Claude Code: https://code.claude.com/docs/en/sandboxing,
  https://code.claude.com/docs/en/sandbox-environments,
  https://code.claude.com/docs/en/devcontainer,
  https://code.claude.com/docs/en/network-config,
  https://github.com/anthropics/sandbox-runtime
- Codex: https://learn.chatgpt.com/docs/sandboxing,
  https://learn.chatgpt.com/docs/agent-approvals-security
- Docker Sandboxes: https://docs.docker.com/ai/sandboxes/,
  https://docs.docker.com/ai/sandboxes/security,
  https://docs.docker.com/ai/sandboxes/configuration/gpu-passthrough/
- Containers and GPUs: https://docs.podman.io/en/latest/markdown/podman-run.1.html,
  https://gvisor.dev/docs/user_guide/gpu,
  https://rocm.docs.amd.com/projects/install-on-linux/en/latest/how-to/docker.html,
  https://github.com/kyuz0/amd-strix-halo-vllm-toolboxes
- Dev containers: https://containers.dev/implementors/json_reference/
- boxd: https://docs.boxd.sh/, https://docs.boxd.sh/guides/env-secrets.md
- Sprites: https://docs.fly.io/sprites, https://docs.fly.io/sprites/concepts/connectors.md
- E2B: https://docs.e2b.dev/, https://docs.e2b.dev/network/internet-access
- Daytona: https://www.daytona.io/docs/en/sandboxes, https://www.daytona.io/docs/en/network-limits
- Modal: https://modal.com/docs/guide/sandbox, https://modal.com/docs/guide/sandbox-networking
- Vercel: https://vercel.com/docs/vercel-sandbox
- Coder: https://coder.com/docs/@v2.36.1/ai-coder/agents
- Wrappers: https://github.com/dagger/container-use, https://github.com/obra/packnplay,
  https://github.com/finbarr/yolobox, https://github.com/trailofbits/claude-code-devcontainer
