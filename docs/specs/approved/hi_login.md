# `hi login` specification

Status: Approved

Dependencies: GitHub CLI for GitHub login; OMP for provider login; the `hf`
CLI for Hugging Face; the Colab CLI for Colab.

## Goal

Offer one discoverable login entry point while leaving authentication,
credential storage, refresh, account selection, and logout with the native
provider tools.

## Commands

```text
hi login
hi login github
hi login omp
hi login omp <provider>
hi login hf
hi login colab
```

`hi login runpod` has no native tool to delegate to, since runpodctl only
stores a key it is given. It reads an API key without echo, checks it with
`GET /v2/catalog/cpus`, and saves it as `apiKey` in runpodctl's
`~/.runpod/config.toml` with mode 0600, keeping the file's other settings. The
guided menu offers the same prompt when RunPod is chosen while signed out.

`hi login hf` runs `hf auth login` and then `hf auth whoami`. `hi login colab`
runs `colab usage`, which starts Colab's browser sign-in and then shows the
compute-unit balance. Both are implemented (v0.7.0); GitHub and OMP login are
planned for v0.9.0.

Bare `hi login` starts GitHub authentication. `hi login github` is the explicit
equivalent. `hi login omp` delegates interactive provider selection, while the
provider form selects one OMP provider directly.

## Delegation

GitHub runs `gh auth login` with the terminal attached and then reports
`gh auth status`. OMP discovers providers through `omp auth-broker list` and
runs `omp auth-broker login [provider]`.

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

1. Bare and explicit GitHub login delegate to `gh auth login`.
2. OMP login uses the auth-broker provider list and login command.
3. Native browser and device-code flows remain usable.
4. Successful GitHub login ends with authenticated status.
5. No credential value is captured in `hi` logs, files, or output.
