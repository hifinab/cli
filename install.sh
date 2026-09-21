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

case ":${PATH}:" in
  *":${install_dir}:"*) ;;
  *)
    if [ "$install_dir" = "$HOME/.local/bin" ]; then
      profile="${HI_PROFILE:-$HOME/.profile}"
      path_line='export PATH="$HOME/.local/bin:$PATH"'
      if ! grep -F "$path_line" "$profile" >/dev/null 2>&1; then
        printf '\n# Added by the hifin CLI installer.\n%s\n' "$path_line" >> "$profile"
      fi
      printf 'Added ~/.local/bin to PATH in %s. Open a new shell to use hi.\n' "$profile"
    else
      printf 'Add %s to PATH to use hi.\n' "$install_dir"
    fi
    ;;
esac
