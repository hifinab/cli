# `hi compute run` over SSH specification

Status: Draft

Dependencies: `hi compute up`, `ssh`, and the `--max` watchdog; the
`computeProvider` interface.

## Goal

Run a script to completion on RunPod, Shadeform, and managed providers, as
`hi compute run` already does on Colab and Hugging Face Jobs. This closes the
last open item of v0.8.0. Today these providers' `validateRun` tells the user
to `up` and then `ssh` (`compute_runpod.go`, `compute_shadeform.go`,
`compute_managed.go`).

## Flow

None of these providers has batch jobs, so `run` is built on SSH, once for
all three:

1. Start a machine as `up` does, with the same `--max` watchdog.
2. Copy the script over with `scp`.
3. Run it over SSH, streaming its output.
4. Return the script's exit status.
5. Always delete the machine afterwards: on success, failure, and Ctrl-C.

`--detach` and `wait` need the run to survive the SSH session: start it under
`nohup` or `tmux` on the machine, write its exit status to a file, and have
`wait` and `logs` read that file and the log over SSH.

## Safety

The `--max` watchdog bounds a run that hangs or loses its laptop. A machine
left behind after a failed delete is reported, never silently ignored.
Community Cloud keeps its warnings, and secrets passed with `--env` are
refused there.

## Open questions

1. Whether to copy a whole directory, or only the script, as Hugging Face
   Jobs does.
2. Whether managed runs need their own approval wording in Slack.
