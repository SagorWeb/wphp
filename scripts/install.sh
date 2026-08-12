#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# WPHPanel — Server Bootstrap
# ═══════════════════════════════════════════════════════════════════════════════
# Usage (root, fresh Ubuntu 22.04 / 24.04 / 26.04 LTS):
#   curl -fsSL https://raw.githubusercontent.com/SagorWeb/wphp/main/scripts/install.sh | bash
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

REPO_RAW="https://raw.githubusercontent.com/SagorWeb/wphp/main"
INSTALLER_PORT="8090"

if [ "$EUID" -ne 0 ]; then
  echo "Error: WPHPanel installer must be run as root."
  echo "Please run: sudo bash $0"
  exit 1
fi

if [ -f /etc/os-release ]; then
  . /etc/os-release
  if [ "$ID" != "ubuntu" ]; then
    echo "Error: WPHPanel requires Ubuntu Linux (supported: Ubuntu 22.04, 24.04, 26.04 LTS)."
    echo "Current OS: ${PRETTY_NAME:-$ID}"
    exit 1
  fi
else
  echo "Error: /etc/os-release not found. Supported OS: Ubuntu 22.04/24.04/26.04 LTS."
  exit 1
fi

if [ -f /opt/wphpanel/bin/wphpanel-api ] || [ -f /etc/systemd/system/wphpanel-api.service ]; then
  echo "Error: WPHPanel is already installed on this server."
  echo "WPHPanel requires a clean, fresh Ubuntu server (supported: 22.04, 24.04, 26.04 LTS)."
  exit 1
fi

echo "════════════════════════════════════════════════════════════════"
echo "        WPHPanel — Server Bootstrap"
echo "════════════════════════════════════════════════════════════════"
echo "  Target OS : ${PRETTY_NAME}"
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
