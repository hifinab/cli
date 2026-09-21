# Roadmap

Implementation order for `hi`. Completed releases use `[x]`; planned work uses
`[ ]`. Detailed behavior and acceptance criteria live in `docs/specs/`.

## Released

### v0.1.0 — Initial workstation support

- [x] Install the CLI from checksummed amd64 and arm64 release assets.
- [x] Configure and verify an Ubuntu Strix Halo workstation.
- [x] Create users with render and video access.

### v0.2.0 — General workstation installation

- [x] Install the non-hardware workstation software through `hi install`.

### v0.3.0 — NetBird enrollment

- [x] Connect a machine with `hi net <setup-key>`.

### v0.4.0 — NetBird device naming

- [x] Prompt for the NetBird device hostname and show the machine hostname as
  the default.

## Planned

### v0.5.0 — Security and diagnostics

- [ ] Move NetBird setup keys out of shell history and process arguments.
  See [hi_net.md](specs/hi_net.md).
- [ ] Add read-only machine diagnostics with human and JSON output.
  See [hi_doctor.md](specs/hi_doctor.md).

Dependency: shared machine-state checks established here are reused by later
service, export, model-serving, and declarative commands.

### v0.6.0 — Updates and authentication

- [ ] Add atomic, checksummed self-updates.
  See [hi_update.md](specs/hi_update.md).
- [ ] Delegate GitHub and OMP authentication to their native tools.
  See [hi_login.md](specs/hi_login.md).

Dependencies: `hi update` relies on the existing release assets and checksum
pipeline. Login requires the corresponding installed CLI.

### v0.7.0 — Project bootstrap

- [ ] Add the interactive `hi init` helper and deterministic Python template.
  See [hi_init.md](specs/hi_init.md).

Dependency: the Python template requires `uv` and establishes the metadata and
safe-generation contract used by later templates.

### v0.8.0 — Specialized project templates

- [ ] Add the quantitative-research template.
  See [hi_init_quant.md](specs/hi_init_quant.md).
- [ ] Add the deterministic web-application template.
  See [hi_init_webapp.md](specs/hi_init_webapp.md).

Dependency: both templates extend the v0.7.0 planner, conflict detection,
metadata, agent instructions, and documentation structure.

### v0.9.0 — Focused machine operations

- [ ] Add a compact managed-service status view.
  See [hi_services.md](specs/hi_services.md).
- [ ] Read and change the machine hostname independently of installation.
  See [hi_hostname.md](specs/hi_hostname.md).
- [ ] Add explicit account-creation profiles.
  See [hi_adduser_profiles.md](specs/hi_adduser_profiles.md).
- [ ] Export a redacted machine diagnostic bundle.
  See [hi_export.md](specs/hi_export.md).

Dependency: service status and export reuse the v0.5.0 doctor detectors rather
than defining health twice.

### v0.10.0 — Repository intelligence

- [ ] Detect template drift and broken repository contracts.
  See [hi_repo_doctor.md](specs/hi_repo_doctor.md).
- [ ] Produce concise, redacted machine and repository context.
  See [hi_context.md](specs/hi_context.md).

Dependencies: these commands consume v0.7.0 template metadata and v0.5.0
machine detectors.

### v0.11.0 — Hardware-aware model serving

- [ ] Detect AMD, NVIDIA, and supported CPU serving profiles.
- [ ] Pull, serve, inspect, log, and stop validated model containers.
  See [hi_model.md](specs/hi_model.md).

Dependencies: model serving requires doctor-grade hardware detection, Docker or
Podman, service lifecycle behavior, and a small validated recipe catalog.

### v1.0.0 — Declarative machine state

- [ ] Preview and converge a versioned `hi.yaml` with `hi apply`.
  See [hi_apply.md](specs/hi_apply.md).

Dependencies: `hi apply` is last because it composes proven detectors and
independently usable mutation commands. It must not hide imperative scripts
behind a declarative label.
