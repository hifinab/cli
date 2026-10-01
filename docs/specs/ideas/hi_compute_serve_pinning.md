# Pinned `hi compute serve` recipes specification

Status: Draft

Dependencies: `hi compute serve`, its recipes in `compute_serve.go`, and
`scripts/compute/serve-llama-cpp.sh`.

## Goal

A recipe that was tested keeps working. Today the tested line records the
model and the hardware but not the llama.cpp that ran them:

- Hugging Face runs `ghcr.io/ggml-org/llama.cpp:server-cuda`, a tag that
  moves every day.
- Colab and RunPod clone llama.cpp's default branch and build it, which
  takes minutes and picks up whatever was merged that day.

## Recipe

Each recipe names one llama.cpp build, and the image digests of that build:

```go
"qwen3.8-flash-next": {
    ...
    llamaCpp: "b11312",
    images: map[string]string{
        "cuda": "ghcr.io/ggml-org/llama.cpp:server-cuda-b11312@sha256:6cdf9529…",
        "rocm": "ghcr.io/ggml-org/llama.cpp:server-rocm-b11312@sha256:…",
    },
    tested: "Colab G4, 2026-09-27: ~84 tokens/s …, llama.cpp b11312",
},
```

Ad hoc models (`hi compute serve owner/Model-GGUF --quant Q`) use one
default build kept in the same file, so they are pinned too. Moving a recipe
to a newer build is a deliberate change that is retested and recorded in
`tested`.

## Per provider

| Provider          | How the pinned build runs                                                                                     |
|-------------------|----------------------------------------------------------------------------------------------------------------|
| Hugging Face Jobs | The `cuda` image by digest instead of `server-cuda`. Whether Jobs accepts `@sha256:` is unverified; the versioned tag is the fallback. |
| Colab             | `git clone --branch b11312` and build, as today but at a fixed commit. Colab cannot run images.               |
| RunPod            | Keep RunPod's own image, which has sshd, and clone the pinned tag. The llama.cpp image has no sshd, so `ssh.direct` and hi's tunnel would not work with it. |
| Shadeform         | `docker run --gpus all --network host <image@digest>` over SSH on the VM, skipping the build. Whether Docker and the NVIDIA container toolkit are on every cloud's image is unverified. |
| Local Strix Halo  | Later: the `rocm` image (built for gfx1151 on ROCm 7.2.1) or `server-vulkan`, with `/dev/kfd` and `/dev/dri`. Untested on aiw11. |

The script already prefers a prebuilt `/app/llama-server`. The images listen
on 8080 by default; hi passes its own port.

## Findings about the images

Checked 2026-10-01 through the GHCR registry API and llama.cpp's workflows.

- Floating tags: `server`, `full`, and `light`, each with `-cuda` (CUDA
  12.8), `-cuda13`, `-rocm`, `-vulkan`, and others. ROCm is amd64 only.
- Versioned tags `server-cuda-b11312` come from a daily build, not every
  release, so most build numbers have no image. Pick a build that has one.
- Versioned tags can be pushed again by a manual rerun, and GHCR does not
  enforce immutable tags. Pin by digest, keeping the tag only for people
  reading it.
- The runtime images contain no SSH server.

## Updating pins

A small script resolves a build number to the digests of its `cuda` and
`rocm` images through the registry API and prints the recipe lines. A person
runs the recipe live and updates `tested`. Nothing bumps pins automatically.

## Related: `--image` on Shadeform

Shadeform starts VMs, so hi rejects `--image` there today. Its create API
has a `launch_configuration` with `type: "docker"` and a
`docker_configuration` (`image`, a single `args` string passed to the
entrypoint, `envs`, `port_mappings`, `volume_mounts`,
`registry_credentials`); containers use host networking. SSH still lands on
the VM, not the container, and the API reports only the VM's status, so `run`
would need `docker wait` and `docker logs` over SSH. Whether this works on
every underlying cloud is unverified; every example uses Massed Compute.
Running `docker run` over hi's own SSH session works the same everywhere and
may be simpler.

## Open questions

1. Whether Hugging Face Jobs accepts image digests. A one-off `cpu-basic`
   job settles it.
2. Whether RunPod should run the pinned image with sshd installed at start
   (RunPod's documented recipe for custom images), trading an install on
   each start for no build.
3. Whether RunPod's HTTPS proxy (`<pod>-8080.proxy.runpod.net`) is usable
   instead of a tunnel, given its 100-second request limit and that it is
   public, needing `--api-key`.

## Sources

- llama.cpp images: https://github.com/ggml-org/llama.cpp/blob/master/docs/docker.md,
  https://github.com/ggml-org/llama.cpp/blob/master/.github/workflows/docker.yml,
  https://github.com/ggml-org/llama.cpp/blob/master/.devops/rocm.Dockerfile
- Shadeform Docker launch: https://docs.shadeform.ai/guides/dockercontainers,
  https://docs.shadeform.ai/api-reference/instances/instances-create
- RunPod SSH and ports: https://docs.runpod.io/pods/configuration/use-ssh,
  https://docs.runpod.io/pods/configuration/expose-ports,
  https://docs.runpod.io/api-reference-v2/pods/create-a-pod
- Hugging Face Jobs images: https://huggingface.co/docs/hub/jobs-images
