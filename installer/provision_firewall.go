package main

import (
	"fmt"
	"os"
	"time"
)

func setupFirewall() {
	// Production host firewall (UFW → nftables).
	// Critical: apply AFTER Incus creates wphpanel-net, then hard-reload so nftables
	// does not leave HTTPS (443) in a stuck state (seen on fresh Ubuntu 26.04 + Incus).

	run("bash", "-c", `sed -i 's/DEFAULT_FORWARD_POLICY="DROP"/DEFAULT_FORWARD_POLICY="ACCEPT"/' /etc/default/ufw`)
	run("bash", "-c", `sed -i 's/DEFAULT_INPUT_POLICY="ACCEPT"/DEFAULT_INPUT_POLICY="DROP"/' /etc/default/ufw`)
	run("bash", "-c", `grep -q '^IPV6=' /etc/default/ufw && sed -i 's/^IPV6=.*/IPV6=yes/' /etc/default/ufw || echo 'IPV6=yes' >> /etc/default/ufw`)

	// Public panel ports only — never expose MariaDB/PostgreSQL/Valkey/Incus API.
	run("bash", "-c", `ufw allow 22/tcp comment 'SSH' || ufw allow 22/tcp`)
	run("bash", "-c", `ufw allow 80/tcp comment 'HTTP ACME + redirect' || ufw allow 80/tcp`)
	run("bash", "-c", `ufw allow 443/tcp comment 'HTTPS panel + sites' || ufw allow 443/tcp`)
	run("bash", "-c", `ufw allow 8090/tcp comment 'WPHPanel installer wizard' || ufw allow 8090/tcp`)

	// Container bridge — DHCP + east-west + NAT forwarding
	run("bash", "-c", `ufw allow in on wphpanel-net || true`)
	run("bash", "-c", `ufw allow out on wphpanel-net || true`)
	run("bash", "-c", `ufw route allow in on wphpanel-net || true`)
	run("bash", "-c", `ufw route allow out on wphpanel-net || true`)
	run("bash", "-c", `ufw allow 67/udp || true`)
	run("bash", "-c", `ufw allow 68/udp || true`)

	// Hard enable + reload cycle rebuilds nftables cleanly (fixes phantom 443 timeouts).
	run("bash", "-c", "ufw --force enable")
	run("bash", "-c", "ufw --force reload")
	// Extra belt-and-suspenders after Incus bridge changes:
	run("bash", "-c", "ufw disable && sleep 1 && ufw --force enable")

	fmt.Println("UFW active. Public: 22/80/443 (+8090 until installer deleted). DB/Valkey/Incus not exposed.")
}

// finalizeProduction reloads networking, restarts services in dependency order, and verifies
// the panel is reachable on localhost HTTP/HTTPS before marking install complete.
func finalizeProduction(cfg InstallConfig) {
	fmt.Println("=== Production finalize ===")
	ensureIncusBridgeUp()

	// Rebuild firewall one last time after all interfaces exist.
	run("bash", "-c", "ufw --force reload || (ufw disable && sleep 1 && ufw --force enable)")

	phpVer := detectHostPHPVersion()
	phpSvc := "php" + phpVer + "-fpm"

	// Dependency order: data plane → cache → API → edge
	services := []string{
		"incus",
		"mariadb",
		"postgresql",
		"valkey",
		phpSvc,
		"pgadmin4",
		"wphpanel-api",
		"nginx",
		"fail2ban",
	}
	for _, svc := range services {
		if shellOutput("systemctl cat "+svc+" >/dev/null 2>&1 && echo ok") != "ok" {
			continue
		}
		run("systemctl", "enable", svc)
		run("systemctl", "restart", svc)
		time.Sleep(500 * time.Millisecond)
	}

	// Nginx config must be valid after SSL write.
	run("bash", "-c", "nginx -t && systemctl reload nginx")

	// Durable healthcheck operators can re-run anytime.
	health := fmt.Sprintf(`#!/bin/bash
# WPHPanel — host healthcheck (production)
set -uo pipefail
HOST=%q
fail=0
check() { if eval "$1" >/dev/null 2>&1; then echo "OK  $2"; else echo "FAIL $2"; fail=1; fi; }
check 'systemctl is-active --quiet nginx' 'nginx'
check 'systemctl is-active --quiet wphpanel-api' 'wphpanel-api'
check 'systemctl is-active --quiet mariadb' 'mariadb'
check 'systemctl is-active --quiet postgresql' 'postgresql'
check 'systemctl is-active --quiet valkey' 'valkey'
check 'systemctl is-active --quiet fail2ban' 'fail2ban'
check 'ss -tln | grep -q ":80 "' 'listen :80'
check 'ss -tln | grep -q ":443 "' 'listen :443'
check 'ss -tln | grep -q "127.0.0.1:8080"' 'listen API :8080'
check 'ss -tln | grep -q ":3306 "' 'mariadb listen :3306'
check 'mysql -N -e "SELECT 1" | grep -q 1' 'mariadb local query'
check 'mysql -N -e "SHOW VARIABLES LIKE '\''require_secure_transport'\''" | awk "{print \$2}" | grep -qi OFF' 'mariadb require_secure_transport OFF'
check 'ss -tln | grep -q "127.0.0.1:6379"' 'valkey loopback'
check 'ss -tln | grep -q "10.100.0.1:6379"' 'valkey bridge'
check 'incus storage list --format csv | grep -q "^default,"' 'incus storage'
check 'incus network list --format csv | grep -q "^wphpanel-net,"' 'incus network'
check 'curl -sf -o /dev/null -H "Host: $HOST" http://127.0.0.1/' 'HTTP local'
check 'curl -skf -o /dev/null -H "Host: $HOST" https://127.0.0.1/' 'HTTPS local'
check 'ufw status | grep -E "80/tcp.*ALLOW"' 'ufw 80'
check 'ufw status | grep -E "443/tcp.*ALLOW"' 'ufw 443'
if [ "$fail" -ne 0 ]; then echo "Healthcheck FAILED"; exit 1; fi
echo "Healthcheck PASSED"
`, cfg.Hostname)
	os.WriteFile("/opt/wphpanel/bin/healthcheck.sh", []byte(health), 0755)

	// Run healthcheck; log but do not abort UI completion if LE cert path differs momentarily.
	out := shellOutput("bash /opt/wphpanel/bin/healthcheck.sh 2>&1")
	fmt.Println(out)
	os.WriteFile("/opt/wphpanel/logs/install-healthcheck.log", []byte(out+"\n"), 0644)

	// Drop a short operator note about cloud firewalls (DO/AWS/etc.).
	note := `WPHPanel install complete.

Public ports required: 22 (SSH), 80 (HTTP/ACME), 443 (HTTPS).
If the site times out in a browser but curl works on the server, also open
22/80/443 on your CLOUD provider firewall (DigitalOcean / AWS / Hetzner),
which is separate from UFW on the VM.

After finishing the installer UI, click delete installer (closes port 8090).
Re-run health: /opt/wphpanel/bin/healthcheck.sh
`
	os.WriteFile("/opt/wphpanel/INSTALL_OK.txt", []byte(note), 0644)
	fmt.Println("=== Production finalize done ===")
}
