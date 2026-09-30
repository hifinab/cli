# `hi login` specification

Status: Approved

Dependencies: the `hf` CLI for Hugging Face; the Colab CLI for Colab.

## Goal

Offer one discoverable login entry point while leaving authentication,
credential storage, refresh, account selection, and logout with the native
provider tools.

## Commands

```text
hi login hf
hi login colab
hi login runpod
hi login shadeform
```

`hi login runpod` has no native tool to delegate to, since runpodctl only
stores a key it is given. It reads an API key without echo, checks it with
`GET /v2/catalog/cpus`, and saves it as `apiKey` in runpodctl's
`~/.runpod/config.toml` with mode 0600, keeping the file's other settings. The
guided menu offers the same prompt when RunPod is chosen while signed out.

`hi login hf` runs `hf auth login` and then `hf auth whoami`. `hi login colab`
runs `colab usage`, which starts Colab's browser sign-in and then shows the
compute-unit balance. Both are implemented (v0.7.0).

GitHub and OMP login are out of scope (dropped 2026-09-29). Nothing in `hi`
uses those sign-ins, and `gh auth login` and `omp auth-broker login` already
do the job, so a wrapper would only add an interface to keep in step with
tools `hi` doesn't own.

## Delegation

Hugging Face and Colab run their native sign-in with the terminal attached.

`hi` must not copy, print, persist, migrate, or refresh provider tokens. It may
report dependency absence and the native command needed to install or retry the
provider tool.

Exception for providers without a sign-in tool: `hi` may save an API key the
user typed into that provider's standard credentials file, as described for
RunPod above, and nowhere else.

Exception for API-based commands such as `hi compute`: `hi` may read a provider's
token from that provider's documented environment variable or token file, in
memory, to authenticate a single API request. The token is never written,
logged, or passed on the command line.

## Errors and output

Preserve native interactive input and output. Return failure when the delegated
command fails or post-login status is unauthenticated. Error messages identify
the provider and command but never include credentials or environment values.

## Acceptance criteria

1. `hi login hf` and `hi login colab` delegate to the native sign-in.
2. Native browser and device-code flows remain usable.
3. `hi login hf` ends by reporting the signed-in account.
4. No credential value is captured in `hi` logs, files, or output.
