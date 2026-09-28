# `hi update` specification

Status: Released in v0.7.1

Dependencies: GitHub release assets and published SHA-256 checksums.

## Goal

Update the installed `hi` binary safely without rerunning the bootstrap script.

## Commands

```text
hi update
hi update --version v0.4.0
hi update --check
```

Bare update selects the latest non-draft, non-prerelease version from
GitHub's latest-release API. An explicit version selects that immutable
release, including an older one. `--check` reports whether a newer release
exists and changes nothing.

Releases before v0.7.1 have no `hi update`; they update once with the
installer, `curl -fsSL https://hifin.sh/install.sh | sh`.

## Update flow

1. Detect the running OS and architecture.
2. Resolve the current executable and verify it is a regular file.
3. Fetch release metadata, matching asset, and checksum file.
4. Download into the executable's directory.
5. Verify the exact asset checksum.
6. Preserve executable mode and intended ownership.
7. Atomically rename the verified binary over the old executable.
8. Run the new binary's version command and report old and new versions.

If already current, exit successfully without replacing the file.

## Safety

Refuse symlink surprises, unsupported platforms, checksum mismatches, unwritable
destinations, and root/user ownership transitions. Never replace the binary
before verification. Temporary files are removed after every failure. Network
or GitHub errors leave the existing executable untouched.

## Implementation notes

- The binary is downloaded to a temporary file in the executable's own
  directory, so the final rename is atomic, and the temporary file is removed
  on every failure.
- The executable is resolved through symlinks and must be a regular file
  owned by the running user; otherwise `hi update` refuses and names the path.
- After replacing, `hi update` runs the new binary's `version` and reports
  it, and reminds the user to refresh a global agent skill if one exists.
- Verified on 2026-09-28 against the real v0.7.0 release: a v0.6.0 build
  updated itself and its SHA-256 matched the published checksum.

## Acceptance criteria

1. Latest and explicit-version selection resolve the expected release.
2. A checksum mismatch prevents replacement.
3. Replacement is atomic on the target filesystem.
4. Interrupted downloads leave the installed binary executable and unchanged.
5. Successful output reports both versions and the executable path.
6. Updating to the installed version is a no-op.
