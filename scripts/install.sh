#!/usr/bin/env bash
set -Eeuo pipefail

log() {
  printf '\n==> %s\n' "$*"
}

fail() {
  printf 'hi: %s\n' "$*" >&2
  exit 1
}

profile="${1:-standard}"
requested_hostname="${2:-}"
case "$profile" in
  standard | strix) ;;
  *) fail "unknown installation profile: $profile" ;;
esac

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

[[ "$EUID" -ne 0 ]] || fail "run this setup as your regular user, not root"
[[ -r /etc/os-release ]] || fail "cannot identify this operating system"

# shellcheck disable=SC1091
source /etc/os-release
[[ "${ID:-}" == "ubuntu" ]] || fail "the installer supports Ubuntu only"
[[ "${VERSION_ID:-}" == "26.04" ]] || fail "the installer currently requires Ubuntu 26.04"
if [[ "$profile" == "strix" ]]; then
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

log "Requesting administrator access"
sudo -v

if [[ -n "$requested_hostname" ]]; then
  log "Changing hostname from $current_hostname to $requested_hostname"
  sudo hostnamectl set-hostname "$requested_hostname"
  update_hosts
fi

log "Installing base packages"
sudo apt-get update
apt_install ca-certificates curl wget gnupg
sudo install -d -m 0755 /etc/apt/keyrings /etc/apt/sources.list.d

if [[ "$profile" == "strix" ]]; then
  log "Configuring AMD ROCm"
  sudo usermod -a -G render,video "$target_user"
  apt_install libatomic1 libquadmath0
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
  sudo apt-get update
  apt_install amdrocm10.0-gfx1151
fi

log "Installing workstation tools"
apt_install python3-setuptools python3-wheel pipx btop wtmpdb tmux nodejs npm
pipx ensurepath
if [[ "$profile" == "strix" ]]; then
  pipx install --force amd-debug-tools
fi
curl -LsSf https://astral.sh/uv/install.sh | sh
curl -fsSL https://pkgs.netbird.io/install.sh | sh
curl -fsSL https://omp.sh/install | sh
curl -fsSL https://claude.ai/install.sh | bash
curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh
curl -fsSL https://herdr.dev/install.sh | sh

log "Configuring GitHub CLI"
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
  -o "$tmp_dir/githubcli-archive-keyring.gpg"
sudo install -m 0644 "$tmp_dir/githubcli-archive-keyring.gpg" \
  /etc/apt/keyrings/githubcli-archive-keyring.gpg
printf 'deb [arch=%s signed-by=/etc/apt/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main\n' \
  "$(dpkg --print-architecture)" |
  sudo tee /etc/apt/sources.list.d/github-cli.list >/dev/null

log "Configuring Docker"
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

log "Installing GitHub CLI and Docker"
sudo apt-get update
apt_install gh docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

log "Upgrading Ubuntu packages"
sudo env DEBIAN_FRONTEND=noninteractive apt-get upgrade -y

if [[ "$profile" == "strix" ]]; then
  printf '\nStrix setup complete. Log out and back in to apply render/video group membership and PATH changes.\n'
else
  printf '\nWorkstation software installation complete. Open a new shell to apply PATH changes.\n'
fi
