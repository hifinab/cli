# `hi model` specification

Status: Draft

Dependencies: `hi doctor` hardware detectors, Docker or Podman, managed-service
lifecycle behavior, and a versioned recipe catalog.

## Goal

Provide one hardware-aware control plane for downloading and serving local
models while preserving the platform-specific requirements of AMD ROCm/Vulkan,
NVIDIA CUDA, and supported CPU engines.

## Commands

```text
hi model detect
hi model list
hi model pull <recipe>
hi model serve <recipe>
hi model serve <recipe> --dry-run
hi model status
hi model logs [recipe]
hi model stop [recipe]
```

## Hardware detection

`hi model detect` reports processor, GPU devices and vendor, usable dedicated or
unified memory, driver stack, container engine, and compatible profiles.

AMD Strix Halo detection requires evidence for `gfx1151`, `/dev/kfd`,
`/dev/dri`, user group access, and a working ROCm or Vulkan probe. NVIDIA
detection uses `nvidia-smi`, driver/CUDA compatibility, and NVIDIA Container
Toolkit or CDI availability. CPU fallback is offered only for recipes with a
validated CPU profile and realistic memory requirements.

No profile is selected solely from a marketing model name. Missing probes are
reported as unsupported or incomplete, not guessed.

## Runtime profiles

Do not build a universal GPU image. A recipe maps one logical model service to
separate validated runtime profiles.

### AMD

Prefer maintained Strix Halo images and recipes from
<https://strix-halo-toolboxes.com/> or compatible AI Toolbox Cockpit catalog
entries. Profiles own device mappings, groups, seccomp, IPC, ROCm/Vulkan
selection, cache mounts, flash attention, mmap policy, and model-specific flags.

### NVIDIA

Use maintained CUDA images with the NVIDIA Container Toolkit. Docker profiles
use configured GPU requests; Podman profiles use the documented CDI devices.
The host driver is checked against the image CUDA requirement before pull or
start. Images are pinned by digest.

### CPU

CPU profiles state thread, memory, model-format, and expected-size constraints.
They are never an invisible fallback after GPU validation fails.

## Recipe catalog

Each versioned recipe identifies:

- Logical name and description
- Model repository, immutable revision, required files, and quantization
- Expected download size and minimum usable memory
- Serving engine and API protocol
- Hardware profiles with image digest and required host capabilities
- Context, concurrency, tensor parallelism, and backend flags
- Persistent model and compilation-cache mounts
- Model name, bind address, port, and health endpoint

Recipes are data, not executable shell fragments. Extra arbitrary arguments are
not accepted until a design can preserve preview, redaction, and validation.

## Storage

Models live under the XDG data directory; download and compilation caches use
the XDG cache directory. Containers are disposable. Image replacement must not
delete model weights or caches. Model mounts are read-only when supported.

`pull` displays repository, revision, files, sizes, license link, destination,
and required disk space before confirmation. Interrupted downloads remain
clearly partial and are never treated as valid models.

## Serving lifecycle

`serve` validates hardware and storage, selects one profile, shows the image
digest, mounts, devices, generated command, endpoint, and expected health check,
then asks for confirmation. `--dry-run` performs every validation but does not
pull, create, start, or remove anything.

Servers use deterministic container names. Start waits for a bounded health
check. Failure reports logs and leaves no running half-configured replacement.
`status`, `logs`, and `stop` operate only on containers carrying `hi` ownership
labels; unrelated containers are never matched by name alone.

## API and exposure

Text-generation recipes prefer an OpenAI-compatible endpoint when the engine
supports it. This does not make llama.cpp, vLLM, ComfyUI, and fine-tuning jobs
interchangeable; each backend owns its request and model semantics.

Bind to `127.0.0.1` by default. External, NetBird, or public binding requires an
explicit option and a displayed exposure warning. API keys come from protected
files or environment references and are redacted from previews and logs. Never
mount the container-engine socket into a serving container.

## Deferred capabilities

Distributed inference, RDMA, automatic model choice, public ingress, arbitrary
images, and user-authored command fragments require separate specifications and
validation.

## Acceptance criteria

1. Detection distinguishes supported, incomplete, and unsupported profiles from
   observable probes.
2. AMD and NVIDIA select separate validated images and launch requirements.
3. Dry-run performs no image, container, model, or filesystem mutation.
4. Images and model revisions are immutable and verified by the recipe.
5. Model data survives image and container replacement.
6. The default endpoint is reachable only from localhost.
7. API keys are absent from command previews, process arguments where avoidable,
   and persisted recipe state.
8. Lifecycle commands affect only containers labeled as owned by `hi`.
9. A successful start ends with a passing backend-specific health check.
