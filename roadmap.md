# Roadmap

Working ideas for `hi`. These describe intended outcomes; exact stacks and generated
files should be chosen before implementation so templates remain small and
predictable.

## Project initialization

### `hi init`

Create a default repository for agent-driven development.

Initial shape:

- `README.md` for the project contract and entry points
- `AGENTS.md` for repository-specific agent instructions
- `docs/` for durable architecture and operational documentation
- `specs/` for scoped feature specifications and acceptance criteria
- `roadmap.md` for planned work and unresolved product decisions
- A minimal `.gitignore` and initialized Git repository when needed

The command should be safe to rerun, refuse to overwrite non-empty files, and
print every file it creates. Before implementation, decide whether it targets
the current directory only or accepts a separate project-directory argument.

Generated repositories should record their template identity in
`.hifin/template.json`:

```json
{
  "template": "default",
  "version": 1,
  "generatedBy": "hi v0.6.0"
}
```

This enables future lifecycle commands without guessing repository intent:

```text
hi init --check    Report drift from the recorded template
hi init --diff     Show proposed template changes
hi init --upgrade  Apply a reviewed template migration
```

Template upgrades must preserve user-owned files and refuse ambiguous merges.

### `hi init quant`

Create a quantitative-research repository that extends the default template.

Candidate additions:

- A Python project managed and locked with `uv`
- Separate `research/`, `src/`, `tests/`, `data/`, and `backtests/` areas
- Notebook support without making notebooks the source of production logic
- Reproducible experiment configuration, seeds, datasets, and result metadata
- Data and generated-result ignore rules
- Example strategy, backtest, and evaluation entry points
- A documented adapter boundary for market-data providers and brokers

Evaluate the backtesting frameworks we actually use before fixing the recipe.
Candidates include Backtesting.py, Backtrader, vectorbt, and LEAN. Prefer one
primary framework with optional adapters over installing several overlapping
frameworks by default.

### `hi init webapp`

Create a deterministic web-application repository that extends the default
template.

The recipe should pin the runtime and package manager, commit lockfiles, define
the application and test layout, include environment-variable examples, and
provide repeatable development, build, migration, and production commands. It
should also include the chosen database, migration tool, formatter, linter,
test runner, container setup, and CI checks.

Choose and document the Hifin web stack before implementation. The template
should encode one supported path rather than offer an interactive matrix of
framework combinations.

## Authentication

### `hi login`

Start GitHub authentication through the installed GitHub CLI:

1. Verify that `gh` is available.
2. Run `gh auth login` with the user's terminal attached.
3. Preserve the GitHub CLI's native browser, device-code, and host prompts.
4. Finish by reporting `gh auth status` without copying or storing credentials
   in `hi`.

### OMP and additional providers

OMP provider login is possible through its existing credential broker. A future
login surface can delegate directly to:

```text
omp auth-broker list
omp auth-broker login [provider]
```

Potential commands:

```text
hi login                 GitHub login through `gh auth login`
hi login github          Explicit GitHub login
hi login omp             Interactive OMP provider selection
hi login omp <provider>  Login to one OMP-supported provider
```

`hi` should never accept, print, or persist provider tokens itself. It should
only check dependencies and hand control to `gh` or OMP so those tools retain
ownership of credential storage, refresh, logout, and account selection.

## Machine health and maintenance

### `hi doctor`

Inspect machine state and print actionable repairs without changing anything.

```text
hi doctor
hi doctor --json
```

Checks should cover the supported Ubuntu release and architecture, pending
reboots, `PATH`, the installed `hi` version, Docker and NetBird services,
GitHub and OMP authentication, workstation tools, and Strix hardware when
present. Human output should pair every failure with an exact next command.
`--json` must expose the same stable facts for agents and automation without
including credentials.

### Secure NetBird enrollment

Replace setup keys in command-line arguments with an interactive secret prompt:

```text
hi net
```

The command should read the key without echoing it, ask for the NetBird device
hostname with the machine hostname as its default, write the key to a temporary
`0600` file, and delegate through NetBird's `--setup-key-file` option. The file
must be removed on success, failure, or interruption. Automation may supply the
key through a documented environment variable or file, but never through
generated configuration committed to a repository.

Potential follow-up commands:

```text
hi net status
hi net down
hi net reconnect
```

### `hi update`

Update `hi` from a signed or checksummed GitHub release:

```text
hi update
hi update --version v0.4.0
```

Select the correct architecture, verify the published checksum, atomically
replace the current executable, preserve permissions and ownership, and print
the old and new versions. Refuse unsafe root/user ownership transitions.

### `hi services`

Provide one compact view of managed runtime state, including Docker, NetBird,
SSH, GPU readiness, and pending reboot status. This should reuse the same
detectors as `hi doctor` rather than introduce a second definition of healthy.

### `hi hostname`

Read or change the system hostname independently of workstation installation:

```text
hi hostname
hi hostname quant-01
```

Use the existing hostname validation and `/etc/hosts` update behavior.

### `hi adduser` profiles

Consider explicit profiles for repeatable account setup:

```text
hi adduser alice --admin
hi adduser alice --developer
```

Profiles may manage sudo, Docker, GPU groups, SSH keys, and standard shell
configuration. Defaults must remain conservative because user and group changes
are privileged and difficult to undo safely.

### `hi export`

Write a redacted diagnostic snapshot for support or remote review:

```text
hi export
```

The report may include platform, package, service, tool, network, and hardware
status. It must exclude environment variables, tokens, setup keys, SSH
material, cookies, and browser data.

## Agent-driven repository workflows

### `hi task`

Create a feature specification under `specs/`:

```text
hi task "Add portfolio risk limits"
```

The generated document should contain the goal, user-visible behavior,
constraints, acceptance criteria, risks, and verification plan. Branch or
worktree creation can be added later as an explicit option, not as an automatic
side effect.

### `hi repo doctor`

Inspect an initialized repository for template drift and broken project
contracts:

```text
hi repo doctor
hi repo doctor --fix
```

Checks may cover `AGENTS.md`, documentation links, specifications, lockfiles,
declared build commands, generated files, accidentally tracked secrets, and
large datasets. Inspection is the default; repairs require `--fix` and must
show what will change.

### `hi context`

Produce a concise, redacted description of the current machine and repository:

```text
hi context
hi context --json
```

Include the operating system, architecture, repository template, runtime
versions, Git branch and remote, available tools, service state, and hardware
profile. Never include tokens, keys, cookies, or credential paths.

## Local model serving

### `hi model`

Provide one hardware-aware control plane for downloading and serving local
models in containers:

```text
hi model detect
hi model list
hi model pull <recipe>
hi model serve <recipe>
hi model status
hi model logs
hi model stop
```

`hi model detect` should report the processor, GPU vendor and devices, usable
GPU or unified memory, driver stack, container engine, and compatible serving
profiles. Detection must be evidence-based:

- AMD Strix Halo: identify `gfx1151`, `/dev/kfd`, `/dev/dri`, ROCm or Vulkan,
  render/video access, and available unified memory.
- NVIDIA: query `nvidia-smi`, driver and CUDA compatibility, and NVIDIA
  Container Toolkit or CDI availability.
- CPU: expose an explicit fallback only for engines and model sizes that can run
  meaningfully without a GPU.

Do not build one supposedly universal GPU image. AMD ROCm/Vulkan and NVIDIA
CUDA require different runtime libraries, device mappings, launch flags, and
validated model recipes. `hi` should select a platform-specific image while
presenting the same lifecycle and API contract.

#### AMD profiles

Evaluate reusing or delegating to the maintained
[Strix Halo AI Toolboxes](https://strix-halo-toolboxes.com/) for llama.cpp,
vLLM, ComfyUI, and fine-tuning. Their images already encode the `gfx1151`
patches and launch requirements that should not be duplicated casually.

[AI Toolbox Cockpit](https://github.com/kyuz0/ai-toolbox-cockpit) already
manages compatible Toolbx, Distrobox, Podman, and Docker environments across
multiple hardware families. Prefer integration with its catalog or command
builders over maintaining a divergent copy of every model and container
recipe.

AMD containers may require `/dev/kfd`, `/dev/dri`, render/video groups,
relaxed seccomp, host IPC, persistent Hugging Face and compilation caches, and
Strix-specific flags such as flash attention and no-mmap. Each recipe must own
those details rather than applying generic defaults to every backend.

#### NVIDIA profiles

Use separate CUDA images and the
[NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html)
instead of attempting to run ROCm images on NVIDIA devices. Docker profiles
should use the configured NVIDIA runtime and GPU requests; Podman profiles
should prefer NVIDIA's documented CDI path. Validate the host driver against
the image's CUDA requirement before pulling or starting a server.

Initial NVIDIA backends should use maintained upstream vLLM or llama.cpp CUDA
images pinned by digest. Hardware-specific defaults such as tensor parallelism,
quantization support, context size, and attention implementation belong in
versioned recipes.

#### Recipe and API contract

A recipe should identify:

- Model repository, revision, files, quantization, and expected storage
- Serving engine and hardware-specific image digest
- Supported platforms and minimum usable memory
- Context, concurrency, tensor parallelism, and backend-specific launch flags
- Persistent model and compilation-cache mounts
- API protocol, model name, bind address, port, and health check

Prefer an OpenAI-compatible endpoint for text-generation engines where the
backend supports it, without pretending that llama.cpp, vLLM, ComfyUI, and
fine-tuning jobs have interchangeable semantics.

Models and caches should live in XDG data/cache directories on the host and be
mounted into disposable containers. Pulling a new image must not delete model
weights or compiled caches. `hi model serve` should print the selected hardware
profile, image digest, mounts, generated command, endpoint, and health result.

#### Security and lifecycle

- Bind to `127.0.0.1` by default; external or NetBird exposure must be explicit.
- Keep API keys in protected files or environment references, never recipe
  files or command previews.
- Mount model weights read-only when the backend permits it.
- Never mount the Docker socket into a serving container.
- Pin production images by digest and show when a newer validated recipe exists.
- Support `--dry-run` before pulling images or starting containers.
- Use deterministic container names, health checks, logs, stop, and replacement
  behavior so interrupted runs remain recoverable.
- Treat distributed inference, RDMA, automatic model selection, and public
  ingress as later capabilities requiring separate validation.

## Declarative machine state

### `hi apply`

Converge a machine from a versioned `hi.yaml` after the underlying detectors and
individual commands are reliable:

```text
hi apply --dry-run
hi apply
```

Draft configuration:

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

Required safety and behavior:

- Parse strictly, reject unknown fields, and require a supported schema version.
- Keep secrets out of `hi.yaml`; reference environment variables or protected
  files when enrollment requires credentials.
- Detect actual state and apply only drift instead of rerunning every installer.
- Make `--dry-run` side-effect free and show the exact ordered operations.
- Reuse `hi doctor` detectors so planning and verification agree.
- Stop on the first failed operation and report completed and remaining steps.
- Never claim rollback for package, service, account, or firmware operations
  that cannot be reversed reliably.
- Keep interactive authentication delegated to `gh` and OMP rather than
  embedding provider credentials.

The schema should grow from proven commands. Do not implement a generic package
manager or provider plug-in system before repeated use demonstrates the need.

## Proposed order

1. Secure NetBird enrollment so setup keys leave shell history and process
   arguments.
2. Add shared state detectors through `hi doctor` and `hi doctor --json`.
3. Add atomic, checksummed self-updates through `hi update`.
4. Define and implement the minimal default `hi init` file contract.
5. Select the quant stack and add `hi init quant`.
6. Select the web stack and add `hi init webapp`.
7. Add GitHub-backed `hi login`, then OMP provider discovery and delegated
   login.
8. Add repository workflows: `hi task`, `hi repo doctor`, and `hi context`.
9. Add focused machine commands such as `hi services`, `hi hostname`, user
   profiles, and redacted exports.
10. Add hardware detection and a small validated set of AMD and NVIDIA model
    serving recipes before expanding the catalog.
11. Specify and implement `hi apply` only after its detectors and component
    operations are independently reliable.
