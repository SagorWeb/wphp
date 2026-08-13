#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# WPHPanel — Server Bootstrap
# ═══════════════════════════════════════════════════════════════════════════════
# One command on any cloud (OVH, AWS, GCP, Azure, Hetzner, DigitalOcean, …).
# Login name can be root, ubuntu, debian, admin, etc. Privilege is UID 0.
#
#   sudo bash -c 'curl -fsSL https://raw.githubusercontent.com/SagorWeb/wphp/main/scripts/install.sh | bash'
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

REPO_RAW="https://raw.githubusercontent.com/SagorWeb/wphp/main"
SCRIPT_URL="${REPO_RAW}/scripts/install.sh"
INSTALLER_PORT="8090"
UNIVERSAL_CMD="sudo bash -c 'curl -fsSL ${SCRIPT_URL} | bash'"

fail() {
  echo ""
  echo "WPHPanel cannot install on this server."
  echo "  $1"
  echo ""
  echo "Required: fresh Ubuntu 22.04 / 24.04 / 26.04 LTS, x86_64, a real KVM/dedicated"
  echo "host (not OpenVZ, not WSL, not already inside a container), systemd,"
  echo "at least 2 GB RAM and 20 GB disk, and real root (UID 0)."
  echo ""
  echo "Install command:"
  echo "  ${UNIVERSAL_CMD}"
  exit 1
}

is_root() {
  [ "$(id -u)" -eq 0 ]
}

ensure_root() {
  if is_root; then
    return 0
  fi

  local login_name uid
  login_name="$(id -un 2>/dev/null || echo unknown)"
  uid="$(id -u)"

  echo "Logged in as '${login_name}' (UID ${uid})."
  echo "Need real root (UID 0). Account name does not matter."
  echo ""

  if [ "${WPHPANEL_INSTALL_ESCALATED:-}" = "1" ]; then
    fail "sudo ran, but this process is still UID ${uid}. This account cannot become root."
  fi

  if ! command -v sudo >/dev/null 2>&1; then
    fail "sudo is not installed. Log in as root, or install sudo, then run: ${UNIVERSAL_CMD}"
  fi

  if sudo -n true 2>/dev/null; then
    echo ">>> Re-running as root via sudo..."
    export WPHPANEL_INSTALL_ESCALATED=1
    exec sudo -E bash -c "curl -fsSL '${SCRIPT_URL}' | bash"
  fi

  echo "This account can use sudo. Run this one command (enter your password if asked):"
  echo ""
  echo "  ${UNIVERSAL_CMD}"
  echo ""
  exit 1
}

# Isolated system containers need a real host kernel (namespaces, cgroup, ZFS).
ensure_supported_server() {
  [ "$(uname -s)" = "Linux" ] || fail "Not Linux ($(uname -s))."

  local arch
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) ;;
    *) fail "Unsupported CPU (${arch}). Need x86_64 (amd64)." ;;
  esac

  if [ ! -d /run/systemd/system ] || [ "$(ps -p 1 -o comm= 2>/dev/null || true)" != "systemd" ]; then
    fail "systemd is not PID 1. Need a normal Ubuntu VPS, not a container without systemd."
  fi

  if [ -f /proc/sys/fs/binfmt_misc/WSLInterop ] || grep -qi microsoft /proc/version 2>/dev/null; then
    fail "WSL is not supported. Use a cloud VPS or dedicated server."
  fi

  # OpenVZ / Virtuozzo cannot run isolated system containers.
  if [ -d /proc/vz ] || [ -f /proc/user_beancounters ]; then
    fail "This VPS type (OpenVZ/Virtuozzo) cannot run isolated system containers. Use KVM or dedicated hardware."
  fi

  local virt virt_container
  virt=""
  virt_container=""
  if command -v systemd-detect-virt >/dev/null 2>&1; then
    virt="$(systemd-detect-virt 2>/dev/null || true)"
    virt_container="$(systemd-detect-virt --container 2>/dev/null || true)"
  fi
  case "${virt_container}" in
    none|"") ;;
    wsl)
      fail "WSL is not supported. Use a cloud VPS or dedicated server."
      ;;
    openvz|lxc|lxc-libvirt|docker|podman|container-other|systemd-nspawn|proxmox)
      fail "This machine is already a container (${virt_container}). WPHPanel must be installed on the host VM or dedicated server."
      ;;
  esac
  case "${virt}" in
    openvz)
      fail "This VPS type cannot run isolated system containers. Use KVM or dedicated hardware."
      ;;
  esac

	if [ ! -f /etc/os-release ]; then
    fail "/etc/os-release not found. WPHPanel only installs on Ubuntu 22.04, 24.04, or 26.04 LTS."
  fi
  # shellcheck source=/dev/null
  . /etc/os-release
  if [ "${ID:-}" != "ubuntu" ]; then
    fail "This OS is ${PRETTY_NAME:-${ID:-unknown}}. WPHPanel only installs on Ubuntu 22.04, 24.04, or 26.04 LTS — not Debian, Mint, or other distros."
  fi
  case "${VERSION_ID:-}" in
    22.04|24.04|26.04) ;;
    *)
      fail "This is Ubuntu ${VERSION_ID:-unknown} (${PRETTY_NAME:-}). WPHPanel only installs on Ubuntu 22.04, 24.04, or 26.04 LTS."
      ;;
  esac

  local ns
  for ns in user mnt pid net ipc uts; do
    [ -e "/proc/1/ns/${ns}" ] || fail "Kernel is missing the ${ns} namespace. Isolated containers cannot run here."
  done

  if [ ! -f /sys/fs/cgroup/cgroup.controllers ] && [ ! -d /sys/fs/cgroup/memory ]; then
    fail "cgroup is not available. Isolated containers cannot run here."
  fi

  # Storage driver (ZFS) must be loadable — Ubuntu kernels include it; custom/OpenVZ kernels often do not.
  local krel zfs_ko
  krel="$(uname -r)"
  zfs_ko="$(find "/lib/modules/${krel}" -name 'zfs.ko*' 2>/dev/null | head -1 || true)"
  if [ -d /sys/module/zfs ]; then
    :
  elif modprobe zfs >/dev/null 2>&1; then
    :
  elif [ -n "${zfs_ko}" ]; then
    :
  else
    fail "This kernel cannot load the required storage module (ZFS). Use a standard Ubuntu cloud/dedicated image, not a custom or OpenVZ kernel."
  fi

  local ram_mb disk_gb
  ram_mb="$(awk '/^MemTotal:/{print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)"
  disk_gb="$(df -B1G --output=size / 2>/dev/null | tail -1 | tr -d ' ' || echo 0)"
  [ "${ram_mb}" -ge 2048 ] || fail "Not enough RAM (${ram_mb} MB). Need at least 2 GB (4 GB recommended)."
  [ "${disk_gb}" -ge 20 ] || fail "Not enough disk (${disk_gb} GB). Need at least 20 GB on /."

  # Running foreign container platforms will fight the panel. Idle Ubuntu lxd snap is OK.
  if systemctl is-active --quiet docker 2>/dev/null || systemctl is-active --quiet docker.socket 2>/dev/null; then
    if command -v docker >/dev/null 2>&1 && docker info >/dev/null 2>&1; then
      fail "Docker is running. WPHPanel needs a clean host. Use a fresh Ubuntu server."
    fi
  fi
  if systemctl is-active --quiet incus 2>/dev/null || systemctl is-active --quiet lxd 2>/dev/null; then
    fail "Another container platform is already running. WPHPanel needs a clean host."
  fi
}

ensure_root
ensure_supported_server

if [ -f /opt/wphpanel/bin/wphpanel-api ] || [ -f /etc/systemd/system/wphpanel-api.service ]; then
  fail "WPHPanel is already installed. Use a clean Ubuntu server."
fi

echo "════════════════════════════════════════════════════════════════"
echo "        WPHPanel — Server Bootstrap"
echo "════════════════════════════════════════════════════════════════"
echo "  User      : $(id -un) (UID $(id -u), root)"
echo "  Target OS : ${PRETTY_NAME}"
echo "  CPU       : $(uname -m)"
echo "  RAM       : $(awk '/^MemTotal:/{print int($2/1024)}' /proc/meminfo) MB"
echo "  Date      : $(date -u)"
echo "════════════════════════════════════════════════════════════════"
echo ""
echo "Server checks passed. Isolated containers can run on this host."
echo ""

export DEBIAN_FRONTEND=noninteractive
echo ">>> Installing prerequisite packages..."
apt-get update -qq >/dev/null 2>&1 || true
apt-get install -y -qq curl tar ca-certificates ufw >/dev/null 2>&1 || true

ufw allow 22/tcp >/dev/null 2>&1 || true
ufw allow 80/tcp >/dev/null 2>&1 || true
ufw allow 443/tcp >/dev/null 2>&1 || true
ufw allow 8090/tcp >/dev/null 2>&1 || true

SERVER_IP=$(curl -s4 https://api.ipify.org 2>/dev/null || curl -s4 https://ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')
if [ -z "$SERVER_IP" ]; then
  SERVER_IP="YOUR_SERVER_IP"
fi

INSTALLER_URL="${WPHPANEL_INSTALLER_URL:-${REPO_RAW}/build/wphpanel-installer-linux-amd64.tar.gz}"
MANIFEST_URL="${WPHPANEL_MANIFEST_URL:-${REPO_RAW}/build/release.json}"

WORKDIR=/tmp/wphpanel-install-tmp
rm -rf "$WORKDIR"
mkdir -p "$WORKDIR"

echo ">>> Fetching WPHPanel web installer..."
curl -fsSL "$INSTALLER_URL" -o "$WORKDIR/installer.tar.gz"

if curl -fsSL "$MANIFEST_URL" -o "$WORKDIR/release.json" 2>/dev/null; then
  EXPECTED=$(sed -n '/installer_linux_amd64/,/sha256/s/.*"sha256": *"\([^"]*\)".*/\1/p' "$WORKDIR/release.json" | head -1)
  GOT=$(sha256sum "$WORKDIR/installer.tar.gz" | awk '{print $1}')
  if [ -n "$EXPECTED" ] && [ "$EXPECTED" != "$GOT" ]; then
    echo "Error: installer checksum mismatch."
    echo "  expected: $EXPECTED"
    echo "  got:      $GOT"
    exit 1
  fi
fi

tar -xzf "$WORKDIR/installer.tar.gz" -C "$WORKDIR"
cp "$WORKDIR/wphpanel-installer-linux-amd64" /tmp/wphpanel-installer
chmod +x /tmp/wphpanel-installer
rm -rf "$WORKDIR"

pkill -f wphpanel-installer 2>/dev/null || true
nohup /tmp/wphpanel-installer > /tmp/wphpanel-installer.log 2>&1 &
sleep 2

echo ""
echo "════════════════════════════════════════════════════════════════"
echo "  WPHPanel web installer is running"
echo "════════════════════════════════════════════════════════════════"
echo ""
echo "  Open your browser:"
echo ""
echo "      http://${SERVER_IP}:${INSTALLER_PORT}"
echo ""
echo "════════════════════════════════════════════════════════════════"
