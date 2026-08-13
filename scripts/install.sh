#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# WPHPanel — Server Bootstrap
# ═══════════════════════════════════════════════════════════════════════════════
# Works on any cloud login name (root, ubuntu, debian, admin, …).
# Privilege is UID 0 (real root), not the username.
#
#   curl -fsSL https://raw.githubusercontent.com/SagorWeb/wphp/main/scripts/install.sh | bash
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

REPO_RAW="https://raw.githubusercontent.com/SagorWeb/wphp/main"
SCRIPT_URL="${REPO_RAW}/scripts/install.sh"
INSTALLER_PORT="8090"

# Real root = kernel UID 0. Login name (ubuntu@ on OVH, debian@, etc.) does not matter.
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

  echo "This session is '${login_name}' (UID ${uid})."
  echo "WPHPanel needs real root (UID 0). The account name does not matter."
  echo ""

  if [ "${WPHPANEL_INSTALL_ESCALATED:-}" = "1" ]; then
    echo "Error: sudo ran, but this process is still not root (UID ${uid})."
    echo "This server cannot grant root. Stopped."
    exit 1
  fi

  if ! command -v sudo >/dev/null 2>&1; then
    echo "Error: sudo is not installed, and this account is not root."
    echo "Log in as a root-capable user, then run:"
    echo "  curl -fsSL ${SCRIPT_URL} | sudo bash"
    exit 1
  fi

  # Passwordless sudo (OVH / AWS / GCP / Azure Ubuntu images)
  if sudo -n true 2>/dev/null; then
    echo ">>> Re-running as root via sudo..."
    export WPHPANEL_INSTALL_ESCALATED=1
    exec sudo -E bash -c "curl -fsSL '${SCRIPT_URL}' | bash"
  fi

  # sudo exists but needs a password — curl|bash cannot prompt reliably
  echo "This account can use sudo, but a password is required."
  echo "Run this and enter your password when asked:"
  echo "  curl -fsSL ${SCRIPT_URL} | sudo bash"
  exit 1
}

ensure_supported_server() {
  if [ "$(uname -s)" != "Linux" ]; then
    echo "Error: unsupported system ($(uname -s)). WPHPanel only installs on Linux."
    exit 1
  fi

  local arch
  arch="$(uname -m)"
  case "$arch" in
    x86_64|amd64) ;;
    *)
      echo "Error: unsupported CPU (${arch}). WPHPanel requires x86_64 (amd64)."
      exit 1
      ;;
  esac

  if [ ! -d /run/systemd/system ]; then
    echo "Error: systemd is not running. WPHPanel needs a normal Ubuntu VPS, not a container without systemd."
    exit 1
  fi

  if [ ! -f /etc/os-release ]; then
    echo "Error: /etc/os-release not found. Supported OS: Ubuntu 22.04 / 24.04 / 26.04 LTS."
    exit 1
  fi

  # shellcheck source=/dev/null
  . /etc/os-release

  if [ "${ID:-}" != "ubuntu" ]; then
    echo "Error: unsupported OS (${PRETTY_NAME:-$ID})."
    echo "WPHPanel only supports Ubuntu 22.04, 24.04, and 26.04 LTS."
    exit 1
  fi

  case "${VERSION_ID:-}" in
    22.04|24.04|26.04) ;;
    *)
      echo "Error: unsupported Ubuntu ${VERSION_ID:-unknown} (${PRETTY_NAME:-})."
      echo "Supported: Ubuntu 22.04, 24.04, 26.04 LTS."
      exit 1
      ;;
  esac
}

ensure_root
ensure_supported_server

if [ -f /opt/wphpanel/bin/wphpanel-api ] || [ -f /etc/systemd/system/wphpanel-api.service ]; then
  echo "Error: WPHPanel is already installed on this server."
  echo "WPHPanel requires a clean, fresh Ubuntu server (22.04 / 24.04 / 26.04 LTS)."
  exit 1
fi

echo "════════════════════════════════════════════════════════════════"
echo "        WPHPanel — Server Bootstrap"
echo "════════════════════════════════════════════════════════════════"
echo "  User      : $(id -un) (UID $(id -u), root privileges)"
echo "  Target OS : ${PRETTY_NAME}"
echo "  CPU       : $(uname -m)"
echo "  Date      : $(date -u)"
echo "════════════════════════════════════════════════════════════════"
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
