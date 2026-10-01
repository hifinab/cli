# `hi compute up --idle` specification

Status: Draft

Dependencies: the `hi compute` providers (RunPod, Shadeform, Hugging Face
Jobs, Colab), the local lifetime watcher, and the RunPod on-pod watchdog.

## Goal

Stop machines whose GPUs sit idle, so a forgotten machine costs minutes
instead of running until `--max`. This is the idle GPU flag deferred from
v0.13.0.

## Command

```text
hi compute up --gpu <hardware> --idle 30m
```

`--idle <duration>` stops the machine once every GPU has been at or below
about 5% utilization, with unchanged memory use, for that long. Stopping
deletes the machine on every provider, exactly as `hi compute stop` does.
Without `--idle`, nothing changes. A policy default per group in
`policy.json` and an idle column in `hi server live` are later additions.

## Reading GPU use

One provider method, `gpuUtil(name) ([]gpuSample, error)`, with a
per-provider source:

| Provider          | Source                                                                                               |
|-------------------|------------------------------------------------------------------------------------------------------|
| RunPod            | `runtime.gpus[].util` and `memoryUtil` from `GET /pods/{id}`, which hi already calls; `runtime` is null unless the pod is running |
| Hugging Face Jobs | A few seconds of the `/api/jobs/{ns}/{id}/metrics` server-sent-event stream: `gpus[id].utilization` and `memory_used_bytes`, one event per second |
| Shadeform         | `nvidia-smi --query-gpu=utilization.gpu,memory.used --format=csv,noheader,nounits` over SSH; `rocm-smi --showuse --showmemuse --csv` on AMD |
| Colab             | `nvidia-smi` over SSH                                                                                |

SSH with `nvidia-smi` is also the fallback when an API read fails. Shadeform
and Colab have no metrics in their APIs.

## Enforcement

As with `--max`, in two places where possible:

- The local watcher polls about once a minute on every provider. It stops
  when the laptop sleeps, as the lifetime watcher already does.
- An on-machine loop keeps working with the laptop off. On RunPod the
  existing `~/.hi/watchdog.sh` samples `nvidia-smi` and deletes the pod with
  its pod-scoped key. On Hugging Face the same loop could end the container
  from inside; whether that ends billing as promptly as `/cancel` is
  unverified. Shadeform stays watcher-only, because its API key must never
  be put on the machine; `auto_delete.date_threshold` at `--max` gives it a
  server-side backstop. Colab is stopped by hi with `colab stop`, because the
  CLI's keep-alive defeats Colab's own idle timeout.
- With `hi server`, the server's reconciler checks managed instances instead
  of the laptop.

## Provider findings

Checked against provider docs on 2026-10-01; not yet tried live.

| Provider          | GPU use in API | Provider idle shutdown                                                                     |
|-------------------|----------------|--------------------------------------------------------------------------------------------|
| RunPod            | Yes            | None; pods run until deleted                                                               |
| Hugging Face Jobs | Yes (SSE)      | None; the job `timeout` (default 30 min) is a hard lifetime                                |
| Shadeform         | No             | None; `auto_delete` stops at a date or spend threshold, not on idleness                    |
| Colab             | No             | In the browser (12 h maximum, 24 h on Pro+), but the CLI's 60-second keep-alive prevents it |

## Suspend and resume

Considered and not proposed. Only RunPod can pause a machine and keep its
disk, and even there the GPU is not guaranteed on resume:

| Provider          | Pause and keep disk                                                                                                       |
|-------------------|---------------------------------------------------------------------------------------------------------------------------|
| RunPod            | Yes: `stop` and `start` actions. Compute costs nothing while stopped; the volume disk costs $0.20/GB-month (against $0.10 running). The container disk is wiped. The pod is tied to its host, so if the GPU is rented meanwhile it resumes with zero GPUs. Resume time is undocumented. |
| Shadeform         | No: only restart (still billed) and delete. Volumes attach only in the same cloud and region.                            |
| Hugging Face Jobs | No: a job runs once. Only scheduled jobs suspend, and that pauses the schedule. Data persists in HF buckets or repos.     |
| Colab             | No: stop deletes the VM. Only Google Drive persists.                                                                      |

So `--idle` deletes everywhere. A RunPod-only `--idle-action pause`, with a
warning about losing the GPU, is possible later if someone needs it. A
portable resume would mean keeping data on a network volume (RunPod, $0.07/GB-
month, survives deletion) and starting a new machine, which belongs with the
containers and project-image work.

## Safety

A machine stopped for idleness reads "stopped after 30m idle" in
`hi compute ls`, the audit log, and Slack, so nobody mistakes it for a crash.
The idle check never replaces `--max`; both apply.

## Open questions

1. The threshold: 5% utilization may miss CPU-bound data loading between
   GPU steps. Memory change may be the better signal.
2. Whether open SSH sessions or tunnels keep a machine alive.
3. Whether Hugging Face self-termination from inside the container stops
   billing at once.

## Sources

- RunPod get a pod (v2 `runtime.gpus`): https://docs.runpod.io/api-reference-v2/pods/get-a-pod.md
- RunPod pod state transitions: https://docs.runpod.io/api-reference-v2/pods/trigger-a-pod-state-transition.md
- RunPod stopped pods and zero GPUs: https://docs.runpod.io/pods/manage-pods,
  https://docs.runpod.io/pods/troubleshooting/zero-gpus
- RunPod pricing: https://www.runpod.io/pricing
- Shadeform API index and create (`auto_delete`): https://docs.shadeform.ai/llms.txt,
  https://docs.shadeform.ai/api-reference/instances/instances-create.md
- Shadeform volumes: https://docs.shadeform.ai/guides/attachvolume
- Hugging Face Jobs guide and metrics: https://huggingface.co/docs/huggingface_hub/guides/jobs,
  https://github.com/huggingface/huggingface_hub/blob/main/src/huggingface_hub/hf_api.py
- Hugging Face Jobs pricing: https://huggingface.co/docs/hub/jobs-pricing
- Colab FAQ: https://research.google.com/colaboratory/faq.html
- Colab CLI: https://github.com/googlecolab/google-colab-cli
