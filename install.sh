#!/bin/sh
set -eu

repo="hifinab/cli"
version="${HI_VERSION:-latest}"
install_dir="${HI_INSTALL_DIR:-$HOME/.local/bin}"

case "$(uname -s)" in
  Linux) os="linux" ;;
  *) printf 'hi: unsupported operating system: %s\n' "$(uname -s)" >&2; exit 1 ;;
esac

case "$(uname -m)" in
  x86_64|amd64) arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) printf 'hi: unsupported architecture: %s\n' "$(uname -m)" >&2; exit 1 ;;
esac

asset="hi-${os}-${arch}"
if [ "$version" = "latest" ]; then
  download_base="https://github.com/${repo}/releases/latest/download"
else
  download_base="https://github.com/${repo}/releases/download/${version}"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT HUP INT TERM

printf 'Downloading hi for %s/%s...\n' "$os" "$arch"
curl -fsSL "${download_base}/${asset}" -o "${tmp_dir}/${asset}"
curl -fsSL "${download_base}/checksums.txt" -o "${tmp_dir}/checksums.txt"

expected="$(awk -v asset="$asset" '$2 == asset { print $1 }' "${tmp_dir}/checksums.txt")"
if [ -z "$expected" ]; then
  printf 'hi: checksum for %s is missing\n' "$asset" >&2
  exit 1
fi
actual="$(sha256sum "${tmp_dir}/${asset}" | awk '{ print $1 }')"
if [ "$actual" != "$expected" ]; then
  printf 'hi: checksum mismatch for %s\n' "$asset" >&2
  exit 1
fi

install -d -m 0755 "$install_dir"
install -m 0755 "${tmp_dir}/${asset}" "${install_dir}/hi"
printf 'Installed hi to %s/hi\n' "$install_dir"

hi_path="${install_dir}/hi"
case "$hi_path" in
  "$HOME"/*) hi_display="~${hi_path#"$HOME"}" ;;
  *) hi_display="$hi_path" ;;
esac

on_path=1
case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *)
    on_path=0
    if [ "$install_dir" = "$HOME/.local/bin" ]; then
      profile="${HI_PROFILE:-$HOME/.profile}"
      path_line='export PATH="$HOME/.local/bin:$PATH"'
      if ! grep -F "$path_line" "$profile" >/dev/null 2>&1; then
        printf '\n# Added by the hifin CLI installer.\n%s\n' "$path_line" >> "$profile"
      fi
      printf 'Added %s to PATH in %s for new shells.\n' "${install_dir}" "$profile"
    else
      printf 'Add %s to PATH to use hi from new shells.\n' "$install_dir"
    fi
    ;;
esac

# Arguments after `sh -s --` run hi immediately, e.g. `sh -s -- install strix`.
# The script itself arrives on stdin, so hi reads from the terminal instead.
if [ "$#" -gt 0 ]; then
  if ! (: </dev/tty) 2>/dev/null; then
    printf 'hi: no terminal available; run %s %s\n' "$hi_display" "$*" >&2
    exit 1
  fi
  printf 'Running hi %s...\n' "$*"
  exec "$hi_path" "$@" </dev/tty
fi

if [ "$on_path" -eq 1 ]; then
  printf 'Run hi help to get started.\n'
else
  printf 'Run it now without opening a new shell:\n  %s install\n  %s install strix\n' "$hi_display" "$hi_display"
fi
