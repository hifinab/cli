#!/usr/bin/env bash
set -Eeuo pipefail

# Usage: install.sh <hostname> <install ids> <remove ids> <upgrade: 0|1>
# Ids are space-separated and match the menu in install.go. An empty hostname
# keeps the current one.

log() {
  printf '\n==> %s\n' "$*"
}

fail() {
  printf 'hi: %s\n' "$*" >&2
  exit 1
}

requested_hostname="${1:-}"
read -r -a install_ids <<<"${2:-}"
read -r -a remove_ids <<<"${3:-}"
upgrade="${4:-0}"

valid_hostname() {
  local hostname="$1"
  local label
  local labels

  ((${#hostname} > 0 && ${#hostname} <= 64)) || return 1
  [[ "$hostname" != .* && "$hostname" != *. && "$hostname" != *..* ]] || return 1
  IFS='.' read -r -a labels <<<"$hostname"
  for label in "${labels[@]}"; do
    ((${#label} <= 63)) || return 1
    [[ "$label" =~ ^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?$ ]] || return 1
  done
}

selected() {
  local wanted="$1"
  shift
  local id
  for id in "$@"; do
    [[ "$id" == "$wanted" ]] && return 0
  done
  return 1
}

installing() { selected "$1" "${install_ids[@]}"; }
removing() { selected "$1" "${remove_ids[@]}"; }

[[ "$EUID" -ne 0 ]] || fail "run this setup as your regular user, not root"
[[ -r /etc/os-release ]] || fail "cannot identify this operating system"

# shellcheck disable=SC1091
source /etc/os-release
[[ "${ID:-}" == "ubuntu" ]] || fail "the installer supports Ubuntu only"
[[ "${VERSION_ID:-}" == "26.04" ]] || fail "the installer currently requires Ubuntu 26.04"
if installing strix; then
  [[ "$(dpkg --print-architecture)" == "amd64" ]] || fail "the Strix setup requires amd64"
fi
command -v sudo >/dev/null || fail "sudo is required"

target_user="$(id -un)"
current_hostname="$(hostname)"
if [[ -n "$requested_hostname" ]]; then
  valid_hostname "$requested_hostname" ||
    fail "invalid hostname: $requested_hostname"
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

apt_install() {
  sudo env DEBIAN_FRONTEND=noninteractive apt-get install -y "$@"
}

# apt_remove removes the named packages that are installed. It answers yes
# only when apt would remove exactly those packages; when others depend on
# them, apt lists what else goes and asks first.
apt_remove() {
  local package
  local present=()
  for package in "$@"; do
    if [[ "$(dpkg-query -W -f='${db:Status-Status}' "$package" 2>/dev/null)" == "installed" ]]; then
      present+=("$package")
    fi
  done
  ((${#present[@]} > 0)) || return 0

  local planned
  local requested
  planned="$(apt-get -s remove "${present[@]}" | awk '/^Remv / { print $2 }' | sort)"
  requested="$(printf '%s\n' "${present[@]}" | sort)"
  if [[ "$planned" == "$requested" ]]; then
    sudo env DEBIAN_FRONTEND=noninteractive apt-get remove -y "${present[@]}"
  else
    printf 'Removing %s also removes packages that depend on it.\n' "${present[*]}"
    sudo apt-get remove "${present[@]}"
  fi
}

update_hosts() {
  awk -v old="$current_hostname" -v new="$requested_hostname" '
    BEGIN { found = 0 }
    {
      for (i = 2; i <= NF; i++) {
        if (substr($i, 1, 1) == "#") {
          break
        }
        if ($i == new) {
          found = 1
        } else if ($i == old) {
          $i = new
          found = 1
        }
      }
      print
    }
    END {
      if (!found) {
        print "127.0.1.1\t" new
      }
    }
  ' /etc/hosts >"$tmp_dir/hosts"
  sudo install -m 0644 "$tmp_dir/hosts" /etc/hosts
}

# Repositories ------------------------------------------------------------

add_github_repo() {
  curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    -o "$tmp_dir/githubcli-archive-keyring.gpg"
  sudo install -m 0644 "$tmp_dir/githubcli-archive-keyring.gpg" \
    /etc/apt/keyrings/githubcli-archive-keyring.gpg
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\n' \
    "$(dpkg --print-architecture)" |
    sudo tee /etc/apt/sources.list.d/github-cli.list >/dev/null
}

add_docker_repo() {
  curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o "$tmp_dir/docker.asc"
  sudo install -m 0644 "$tmp_dir/docker.asc" /etc/apt/keyrings/docker.asc
  sudo tee /etc/apt/sources.list.d/docker.sources >/dev/null <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: ${UBUNTU_CODENAME:-$VERSION_CODENAME}
Components: stable
Architectures: $(dpkg --print-architecture)
Signed-By: /etc/apt/keyrings/docker.asc
EOF
}

add_rocm_repo() {
  curl -fsSL https://stable.repo.amd.com/rocm/gpg/packages.gpg -o "$tmp_dir/amdrocm.gpg"
  gpg --batch --yes --dearmor --output "$tmp_dir/amdrocm-keyring.gpg" "$tmp_dir/amdrocm.gpg"
  sudo install -m 0644 "$tmp_dir/amdrocm-keyring.gpg" /etc/apt/keyrings/amdrocm.gpg
  sudo tee /etc/apt/sources.list.d/amdrocm-stable.sources >/dev/null <<'EOF'
X-Repo-Id: amdrocm-stable
Types: deb
URIs: https://stable.repo.amd.com/rocm/core/packages/ubuntu2604/
Suites: stable
Components: main
Architectures: amd64
Signed-By: /etc/apt/keyrings/amdrocm.gpg
Enabled: yes
EOF
}

# Removal -----------------------------------------------------------------

remove_tool() {
  log "Removing $1"
  case "$1" in
    terminal) apt_remove tmux btop timg ;;
    node) apt_remove nodejs npm ;;
    uv)
      if [[ -x "$HOME/.local/bin/uv" ]]; then
        "$HOME/.local/bin/uv" cache clean || true
      fi
      rm -f "$HOME/.local/bin/uv" "$HOME/.local/bin/uvx"
      ;;
    gh)
      apt_remove gh
      sudo rm -f /etc/apt/sources.list.d/github-cli.list /etc/apt/keyrings/githubcli-archive-keyring.gpg
      ;;
    docker)
      apt_remove docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
      sudo rm -f /etc/apt/sources.list.d/docker.sources /etc/apt/keyrings/docker.asc
      ;;
    podman) apt_remove podman ;;
    claude)
      rm -f "$HOME/.local/bin/claude"
      rm -rf "$HOME/.local/share/claude"
      ;;
    codex)
      rm -f "$HOME/.local/bin/codex" "$HOME/.local/bin/codex-code-mode-host"
      rm -rf "${CODEX_HOME:-$HOME/.codex}/packages/standalone" \
        "${CODEX_HOME:-$HOME/.codex}/packages/app-server-daemon"
      ;;
    omp) rm -f "$HOME/.local/bin/omp" ;;
    herdr) rm -f "$HOME/.local/bin/herdr" ;;
    hermes)
      rm -f "$HOME/.local/bin/hermes"
      rm -rf "${HERMES_HOME:-$HOME/.hermes}/hermes-agent"
      ;;
    netbird)
      if command -v netbird >/dev/null; then
        sudo netbird service stop || true
        sudo netbird service uninstall || true
      fi
      apt_remove netbird
      sudo rm -f /etc/apt/sources.list.d/netbird.list /usr/share/keyrings/netbird-archive-keyring.gpg
      ;;
    colab)
      if [[ -x "$HOME/.local/bin/uv" ]]; then
        "$HOME/.local/bin/uv" tool uninstall google-colab-cli || true
      fi
      rm -rf "$HOME/.local/share/uv/tools/google-colab-cli"
      rm -f "$HOME/.local/bin/colab"
      ;;
    hf)
      local hf_dir="${HF_HOME:+$HF_HOME/cli}"
      hf_dir="${hf_dir:-$HOME/.hf-cli}"
      rm -f "$HOME/.local/bin/hf"
      if [[ -e "$hf_dir/venv/.hf_installer_marker" ]]; then
        rm -rf "$hf_dir"
      fi
      ;;
    strix)
      if command -v pipx >/dev/null; then
        pipx uninstall amd-debug-tools || true
      fi
      apt_remove amdrocm10.0-gfx1151
      sudo rm -f /etc/apt/sources.list.d/amdrocm-stable.sources /etc/apt/keyrings/amdrocm.gpg
      ;;
    *) fail "unknown tool: $1" ;;
  esac
}

# Installation ------------------------------------------------------------

case ":$PATH:" in
  *":$HOME/.local/bin:"*) local_bin_on_path=1 ;;
  *) local_bin_on_path=0 ;;
esac

log "Requesting administrator access"
sudo -v

if [[ -n "$requested_hostname" ]]; then
  log "Changing hostname from $current_hostname to $requested_hostname"
  sudo hostnamectl set-hostname "$requested_hostname"
  update_hosts
fi

# Remove in reverse menu order, so a tool goes before what it was installed
# with (the Colab CLI before uv).
for ((i = ${#remove_ids[@]} - 1; i >= 0; i--)); do
  remove_tool "${remove_ids[i]}"
done

if ((${#install_ids[@]} > 0)) || [[ "$upgrade" == "1" ]]; then
  log "Installing base packages"
  sudo apt-get update
  apt_install ca-certificates curl wget gnupg python3-setuptools python3-wheel pipx wtmpdb
  sudo install -d -m 0755 /etc/apt/keyrings /etc/apt/sources.list.d
  # pipx prints a warning when ~/.local/bin is already on PATH.
  if [[ "$local_bin_on_path" == "0" ]]; then
    pipx ensurepath
  fi
fi

packages=()
installing terminal && packages+=(tmux btop timg)
if installing gh; then
  log "Configuring the GitHub CLI repository"
  add_github_repo
  packages+=(gh)
fi
if installing docker; then
  log "Configuring the Docker repository"
  add_docker_repo
  packages+=(docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin)
fi
# uidmap and passt are what rootless Podman runs on: ID mapping and its
# default network. crun, not Docker's runc, is the runtime that can pass the
# user's render and video groups into a container (--group-add keep-groups),
# which the GPU needs.
installing podman && packages+=(podman crun uidmap passt)
if installing strix; then
  log "Configuring the AMD ROCm repository"
  sudo usermod -a -G render,video "$target_user"
  add_rocm_repo
  packages+=(libatomic1 libquadmath0 amdrocm10.0-gfx1151)
fi
if ((${#packages[@]} > 0)); then
  log "Installing ${packages[*]}"
  if installing gh || installing docker || installing strix; then
    sudo apt-get update
  fi
  apt_install "${packages[@]}"
fi
# Ubuntu's npm recommends eslint, webpack, node-tap, and through them a
# terminal emulator, X11 libraries, and Perl modules, none of which npm
# needs.
if installing node; then
  log "Installing nodejs npm"
  apt_install --no-install-recommends nodejs npm
fi

if installing podman; then
  # Rootless containers map the user's IDs into a range of subordinate IDs.
  # Ubuntu gives users created with adduser one; others get the next free
  # range here.
  for map in subuid subgid; do
    if ! grep -q "^$target_user:" "/etc/$map" 2>/dev/null; then
      start=$(awk -F: '{ end = $2 + $3; if (end > max) max = end } END { print (max > 100000 ? max : 100000) }' "/etc/$map" 2>/dev/null || echo 100000)
      log "Adding $map range $start-$((start + 65535)) for $target_user"
      if [[ "$map" == "subuid" ]]; then
        sudo usermod --add-subuids "$start-$((start + 65535))" "$target_user"
      else
        sudo usermod --add-subgids "$start-$((start + 65535))" "$target_user"
      fi
    fi
  done
  podman system migrate >/dev/null 2>&1 || true
fi
if installing strix; then
  log "Installing amd-debug-tools"
  pipx install --force amd-debug-tools
fi
if installing uv; then
  log "Installing uv"
  curl -LsSf https://astral.sh/uv/install.sh | sh
fi
if installing colab; then
  log "Installing the Colab CLI"
  "$HOME/.local/bin/uv" tool install --upgrade google-colab-cli
fi
if installing netbird; then
  log "Installing NetBird"
  curl -fsSL https://pkgs.netbird.io/install.sh | sh
fi
if installing omp; then
  log "Installing omp"
  curl -fsSL https://omp.sh/install | sh
fi
if installing claude; then
  log "Installing Claude Code"
  curl -fsSL https://claude.ai/install.sh | bash
fi
if installing codex; then
  log "Installing Codex CLI"
  curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh
fi
if installing herdr; then
  log "Installing herdr"
  curl -fsSL https://herdr.dev/install.sh | sh
fi
if installing hermes; then
  # Its setup wizard asks questions; hermes setup runs it later.
  log "Installing Hermes Agent"
  curl -fsSL https://hermes-agent.nousresearch.com/install.sh | bash -s -- --skip-setup
fi
if installing hf; then
  log "Installing the Hugging Face CLI"
  curl -LsSf https://hf.co/cli/install.sh | bash
fi

if [[ "$upgrade" == "1" ]]; then
  log "Upgrading Ubuntu packages"
  sudo env DEBIAN_FRONTEND=noninteractive apt-get upgrade -y
fi

# Installers add ~/.local/bin to PATH in the shell profile, which only
# reaches new shells.
path_changed=0
if ((${#install_ids[@]} > 0)) && [[ "$local_bin_on_path" == "0" ]]; then
  path_changed=1
fi
if installing strix && [[ "$path_changed" == "1" ]]; then
  printf '\nDone. Log out and back in to apply render/video group membership and PATH changes.\n'
elif installing strix; then
  printf '\nDone. Log out and back in to apply render/video group membership.\n'
elif [[ "$path_changed" == "1" ]]; then
  printf '\nDone. Open a new shell to apply PATH changes.\n'
else
  printf '\nDone.\n'
fi
