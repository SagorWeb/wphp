#!/bin/bash
# ═══════════════════════════════════════════════════════════════════════════════
# WPHPanel — Public Server Bootstrap Installer Script
# ═══════════════════════════════════════════════════════════════════════════════
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/wphpanel/wphpanel/main/github/scripts/install.sh | bash
# ═══════════════════════════════════════════════════════════════════════════════

set -euo pipefail

# 1. Root privilege verification
if [ "$EUID" -ne 0 ]; then
  echo "❌ Error: WPHPanel installer must be run as root."
  echo "Please run: sudo bash $0"
  exit 1
fi

# 2. OS Verification (Ubuntu 22.04, 24.04, or 26.04 LTS)
if [ -f /etc/os-release ]; then
  . /etc/os-release
  if [ "$ID" != "ubuntu" ]; then
    echo "❌ Error: WPHPanel requires Ubuntu Linux (supported: Ubuntu 22.04, 24.04, 26.04 LTS)."
    echo "Current OS: ${PRETTY_NAME:-$ID}"
    exit 1
  fi
else
  echo "❌ Error: /etc/os-release not found. Supported OS: Ubuntu 22.04/24.04/26.04 LTS."
  exit 1
fi

# Fresh Server Verification
if [ -f /opt/wphpanel/bin/wphpanel-api ] || [ -f /etc/systemd/system/wphpanel-api.service ]; then
  echo "❌ Error: WPHPanel is already installed on this server."
  echo "WPHPanel requires a clean, fresh Ubuntu server (supported: 22.04, 24.04, 26.04 LTS)."
  echo "Running on a pre-configured server causes port and service collisions."
  exit 1
fi

echo "════════════════════════════════════════════════════════════════"
echo "        🚀 WPHPanel — Server Bootstrap Installer"
echo "════════════════════════════════════════════════════════════════"
echo "  Target OS : ${PRETTY_NAME}"
echo "  Date      : $(date -u)"
echo "════════════════════════════════════════════════════════════════"
echo ""

# 3. Install core dependencies
export DEBIAN_FRONTEND=noninteractive
echo ">>> Installing prerequisite packages..."
apt-get update -qq >/dev/null 2>&1 || true
apt-get install -y -qq curl tar ca-certificates ufw >/dev/null 2>&1 || true

# 4. Open Installer Firewall Port (8090) + web ports early (ACME/HTTPS)
ufw allow 22/tcp >/dev/null 2>&1 || true
ufw allow 80/tcp >/dev/null 2>&1 || true
ufw allow 443/tcp >/dev/null 2>&1 || true
ufw allow 8090/tcp >/dev/null 2>&1 || true

# 5. Detect Server Public IP Address
SERVER_IP=$(curl -s4 https://api.ipify.org 2>/dev/null || curl -s4 https://ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')
if [ -z "$SERVER_IP" ]; then
  SERVER_IP="YOUR_SERVER_IP"
fi

INSTALLER_PORT="8090"
INSTALLER_URL="${WPHPANEL_INSTALLER_URL:-https://raw.githubusercontent.com/SagorWeb/wphp/main/build/wphpanel-installer-linux-amd64.tar.gz}"

# 6. Download & Extract Standalone Installer Binary
echo ">>> Fetching WPHPanel Web Installer binary..."
mkdir -p /tmp/wphpanel-install-tmp
curl -fsSL "$INSTALLER_URL" -o /tmp/wphpanel-install-tmp/installer.tar.gz
tar -xzf /tmp/wphpanel-install-tmp/installer.tar.gz -C /tmp/wphpanel-install-tmp/
cp /tmp/wphpanel-install-tmp/wphpanel-installer-linux-amd64 /tmp/wphpanel-installer
chmod +x /tmp/wphpanel-installer
rm -rf /tmp/wphpanel-install-tmp

# 7. Terminate any previous installer instance and launch new wizard
pkill -f wphpanel-installer 2>/dev/null || true
nohup /tmp/wphpanel-installer > /tmp/wphpanel-installer.log 2>&1 &
sleep 2

# 8. Visual IP Display & Terminal Banner
echo ""
echo "════════════════════════════════════════════════════════════════"
echo "  ✅ WPHPanel Web Installer is RUNNING!"
echo "════════════════════════════════════════════════════════════════"
echo ""
echo "  Open your web browser and navigate to:"
echo ""
echo "      👉 http://${SERVER_IP}:${INSTALLER_PORT}"
echo ""
echo "  Complete the interactive setup wizard in your browser."
echo "════════════════════════════════════════════════════════════════"
