package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const totalSteps = 9

func runProvision(cfg InstallConfig, creds Credentials, ch chan StepUpdate) {
	// Set non-interactive environment
	os.Setenv("DEBIAN_FRONTEND", "noninteractive")
	os.Setenv("PAGER", "cat")
	os.Setenv("SYSTEMD_PAGER", "cat")
	os.Setenv("APT_LISTCHANGES_FRONTEND", "none")
	os.Setenv("NEEDRESTART_MODE", "a")

	// Ensure target directory exists immediately
	os.MkdirAll("/opt/wphpanel", 0755)

	var currentState InstallState
	currentState.Total = totalSteps
	currentState.Hostname = cfg.Hostname
	currentState.AdminEmail = cfg.AdminEmail
	currentState.Credentials = creds

	send := func(step int, title, status, msg string) {
		pct := 0
		if status == "done" {
			pct = (step * 100) / totalSteps
		} else {
			pct = ((step - 1) * 100) / totalSteps
		}
		update := StepUpdate{Step: step, Total: totalSteps, Title: title, Status: status, Log: msg, Percent: pct}

		select {
		case ch <- update:
		default:
		}

		// Save state to file
		currentState.Step = step
		currentState.Title = title
		currentState.Status = status
		currentState.Log = msg
		currentState.Percent = pct
		stateData, _ := json.Marshal(currentState)
		os.WriteFile("/opt/wphpanel/.install_state.json", stateData, 0600)
	}

	// ── Step 0: System Update & OS Verification ──
	send(1, "System Update", "running", "Verifying OS and updating packages...")
	osRelease := shellOutput("cat /etc/os-release 2>/dev/null || true")
	if !strings.Contains(osRelease, "ID=ubuntu") {
		send(1, "System Update", "error", "WPHPanel requires Ubuntu Linux (supported: Ubuntu 22.04, 24.04, 26.04 LTS)")
		return
	}

	// Fresh Server Verification: ensure system is clean to prevent package & config collisions
	if _, err := os.Stat("/opt/wphpanel/bin/wphpanel-api"); err == nil {
		send(1, "System Update", "error", "WPHPanel is already installed. WPHPanel requires a clean, fresh Ubuntu server.")
		return
	}
	aptWait()
	run("apt-get", "update", "-y", "-qq")
	run("apt-get", "upgrade", "-y", "-qq")
	run("apt-get", "install", "-y", "-qq", "curl", "wget", "git", "software-properties-common", "ca-certificates", "gnupg", "lsb-release", "ufw", "htop", "jq", "acl", "unattended-upgrades")
	send(1, "System Update", "done", "System updated and verified")

	// ── Step 1: Set Hostname ──
	send(2, "Set Hostname", "running", "Setting hostname to "+cfg.Hostname+"...")
	run("hostnamectl", "set-hostname", cfg.Hostname)
	replaceInFile("/etc/hosts", "127.0.1.1", "127.0.1.1\t"+cfg.Hostname)
	send(2, "Set Hostname", "done", "Hostname set to "+cfg.Hostname)

	// ── Step 2: Add All Official Repositories ──
	send(3, "Official Repositories", "running", "Adding Zabbly + Nginx + PostgreSQL repos...")
	os.MkdirAll("/etc/apt/keyrings", 0755)
	codename := shellOutput("lsb_release -cs 2>/dev/null || grep VERSION_CODENAME= /etc/os-release | cut -d= -f2")

	// Zabbly (Incus) — stable repo gets latest LTS (7.0 LTS)
	run("bash", "-c", "curl -fsSL https://pkgs.zabbly.com/key.asc -o /etc/apt/keyrings/zabbly.asc")
	os.WriteFile("/etc/apt/sources.list.d/zabbly-incus.list",
		[]byte(fmt.Sprintf("deb [signed-by=/etc/apt/keyrings/zabbly.asc] https://pkgs.zabbly.com/incus/lts-7.0 %s main", codename)), 0644)
	// NOTE: ZFS is included natively in Ubuntu 24.04 kernel — no separate repo needed

	// Nginx Official Stable (1.30.x)
	run("bash", "-c", "curl -fsSL https://nginx.org/keys/nginx_signing.key | gpg --batch --yes --dearmor -o /etc/apt/keyrings/nginx.gpg")
	os.WriteFile("/etc/apt/sources.list.d/nginx.list",
		[]byte(fmt.Sprintf("deb [signed-by=/etc/apt/keyrings/nginx.gpg] https://nginx.org/packages/ubuntu %s nginx", codename)), 0644)

	// PostgreSQL Official PGDG (17.x)
	run("bash", "-c", "apt-get install -y -qq postgresql-common 2>/dev/null || true")
	run("bash", "-c", "install -d /usr/share/postgresql-common/pgdg")
	run("bash", "-c", "curl -fsSL -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc https://www.postgresql.org/media/keys/ACCC4CF8.asc")
	os.WriteFile("/etc/apt/sources.list.d/pgdg.list",
		[]byte(fmt.Sprintf("deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt %s-pgdg main", codename)), 0644)

	// PHP (ondrej PPA) — best-effort. On brand-new Ubuntu codenames (e.g. 26.04 resolute)
	// the PPA may not publish a Release file yet; we fall back to distro PHP later.
	run("bash", "-c", "add-apt-repository ppa:ondrej/php -y 2>/dev/null || true")

	// Build tools (for Valkey 9 compilation)
	run("apt-get", "install", "-y", "-qq", "build-essential", "pkg-config", "libssl-dev")

	aptWait()
	// apt update may fail if ondrej/MariaDB lack this codename — continue with usable indexes.
	run("bash", "-c", "apt-get update -y -qq 2>/dev/null || apt-get update -y -qq --allow-releaseinfo-change 2>/dev/null || true")
	send(3, "Official Repositories", "done", "All repos added: Zabbly + Nginx + PGDG + PHP")

	// ── Step 3: Install Nginx 1.30 (official stable) ──
	send(4, "Install Nginx 1.30", "running", "Installing Nginx 1.30 stable from nginx.org...")
	aptWait()
	run("apt-get", "install", "-y", "-qq", "nginx")
	run("systemctl", "enable", "nginx")
	run("systemctl", "start", "nginx")
	run("usermod", "-aG", "www-data", "nginx")
	os.Remove("/etc/nginx/sites-enabled/default")
	os.Remove("/etc/nginx/conf.d/default.conf")
	os.MkdirAll("/etc/nginx/sites-available", 0755)
	os.MkdirAll("/etc/nginx/sites-enabled", 0755)
	// Ensure sites-enabled is included in nginx.conf
	run("bash", "-c", `grep -q 'sites-enabled' /etc/nginx/nginx.conf || sed -i '/http {/a \\    include /etc/nginx/sites-enabled/*;' /etc/nginx/nginx.conf`)
	// Set global client_max_body_size 0 (unlimited) — per-container Nginx + PHP enforce actual limits
	run("bash", "-c", `grep -q 'client_max_body_size' /etc/nginx/nginx.conf || sed -i '/http {/a \\    client_max_body_size 0;' /etc/nginx/nginx.conf`)

	// ── Host Nginx auto-restart override (Issue 1) ──
	// Default nginx.service has Restart=no — if it crashes, ALL sites go down.
	os.MkdirAll("/etc/systemd/system/nginx.service.d", 0755)
	os.WriteFile("/etc/systemd/system/nginx.service.d/restart.conf",
		[]byte("[Unit]\nStartLimitBurst=10\nStartLimitIntervalSec=120\n\n[Service]\nRestart=always\nRestartSec=3\n"), 0644)
	run("systemctl", "daemon-reload")

	writeNginxCatchall()
	run("systemctl", "restart", "nginx")
	nginxVer := shellOutput("nginx -v 2>&1")
	send(4, "Install Nginx 1.30", "done", nginxVer)

	// ── Step 4: Install MariaDB + phpMyAdmin ──
	send(5, "MariaDB + phpMyAdmin", "running", "Installing MariaDB 11.4 LTS...")
	installMariaDB(cfg, creds, codename)
	send(5, "MariaDB + phpMyAdmin", "done", "MariaDB + phpMyAdmin installed with SSO")

	// ── Step 5: Install PostgreSQL & pgAdmin ──
	send(6, "PostgreSQL & pgAdmin", "running", "Installing PostgreSQL & pgAdmin 4...")
	installPostgreSQL(creds)
	installPgAdmin(cfg, creds, codename)
	send(6, "PostgreSQL & pgAdmin", "done", "PostgreSQL & pgAdmin 4 installed")

	// ── Step 6: Setup SSL Manager ──
	send(7, "SSL Manager", "running", "Configuring native SSL manager...")
	setupSSLDirs(cfg)
	send(7, "SSL Manager", "done", "SSL directories prepared")

	// ── Step 7: Install ZFS + Incus ──
	send(8, "ZFS + Incus Engine", "running", "Installing ZFS and Incus container engine...")
	installZFSAndIncus()
	send(8, "ZFS + Incus Engine", "done", "ZFS + Incus installed with preseed")

	// ── Step 8: Valkey & Finalize ──
	send(9, "Valkey 9 & Finalize", "running", "Compiling Valkey 9 and finalizing production stack...")
	// Kernel/sysctl first — ip_forward must be on before container NAT + UFW FORWARD.
	setupKernelTuning()
	installValkey(creds)
	// Firewall AFTER Incus bridge exists — then hard-reload nftables (avoids stuck 443).
	setupFirewall()
	installFail2ban()
	setupSwap()
	setupDirectories(cfg, creds)
	writeEnvFile(cfg, creds)
	writeSystemdService()
	writePanelNginx(cfg)
	writeSSLRenewalTimer()
	pinPackages()
	verifyFail2ban()
	// Final production pass: reload UFW, restart stack in dependency order, verify ports/SSL.
	finalizeProduction(cfg)
	send(9, "Valkey 9 & Finalize", "done", "Production stack verified (ports, SSL, services, UFW)")

	// Final state save
	currentState.Step = totalSteps
	currentState.Title = "Complete"
	currentState.Status = "complete"
	currentState.Log = "Server provisioned!"
	currentState.Percent = 100
	stateData, _ := json.Marshal(currentState)
	os.WriteFile("/opt/wphpanel/.install_state.json", stateData, 0600)

	select {
	case ch <- StepUpdate{Step: totalSteps, Total: totalSteps, Title: "Complete", Status: "complete", Log: "Server provisioned!", Percent: 100}:
	default:
	}
}
