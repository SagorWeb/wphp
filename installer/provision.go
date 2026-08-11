package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-acme/lego/v5/acme"
	"github.com/go-acme/lego/v5/certcrypto"
	"github.com/go-acme/lego/v5/certificate"
	"github.com/go-acme/lego/v5/lego"
	"github.com/go-acme/lego/v5/providers/http/webroot"
	"github.com/go-acme/lego/v5/registration"
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

	// PHP 8.4 (ondrej PPA)
	run("bash", "-c", "add-apt-repository ppa:ondrej/php -y 2>/dev/null || true")

	// Build tools (for Valkey 9 compilation)
	run("apt-get", "install", "-y", "-qq", "build-essential", "pkg-config", "libssl-dev")

	aptWait()
	run("apt-get", "update", "-y", "-qq")
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
	send(9, "Valkey 9 & Finalize", "running", "Compiling Valkey 9 and finalizing system configuration...")
	installValkey(creds)
	setupFirewall()
	installFail2ban()
	setupSwap()
	setupKernelTuning()
	setupDirectories(cfg, creds)
	writeEnvFile(cfg, creds)
	writeSystemdService()
	writePanelNginx(cfg)
	writeSSLRenewalTimer()
	pinPackages()
	verifyFail2ban()
	send(9, "Valkey 9 & Finalize", "done", "Valkey compiled, services and configurations finalized")

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

// ── Step Implementations ──

func installMariaDB(cfg InstallConfig, creds Credentials, codename string) {
	// Add MariaDB 11.4 repo
	run("bash", "-c", "curl -fsSL https://mariadb.org/mariadb_release_signing_key.pgp | gpg --batch --yes --dearmor -o /usr/share/keyrings/mariadb-keyring.gpg")
	os.WriteFile("/etc/apt/sources.list.d/mariadb.list",
		[]byte(fmt.Sprintf("deb [signed-by=/usr/share/keyrings/mariadb-keyring.gpg] https://dlm.mariadb.com/repo/mariadb-server/11.4/repo/ubuntu %s main", codename)), 0644)
	aptWait()
	run("apt-get", "update", "-y", "-qq")
	run("apt-get", "install", "-y", "-qq", "mariadb-server")

	// Root password + secure
	os.WriteFile("/root/.my.cnf", []byte(fmt.Sprintf("[client]\nuser=root\npassword=%s\n", creds.MariaDBRootPass)), 0600)
	run("mysql", "-e", "DELETE FROM mysql.global_priv WHERE User='';")
	run("mysql", "-e", "DROP DATABASE IF EXISTS test;")
	run("mysql", "-e", fmt.Sprintf("ALTER USER 'root'@'localhost' IDENTIFIED BY '%s';", creds.MariaDBRootPass))
	run("mysql", "-e", "FLUSH PRIVILEGES;")

	// Bind address for container access
	replaceInFile("/etc/mysql/mariadb.conf.d/50-server.cnf", "bind-address", "bind-address = 0.0.0.0")

	// Dynamic buffer pool optimization
	ramMB := shellOutputInt("awk '/^MemTotal:/{print int($2/1024)}' /proc/meminfo")
	bufferMB, maxConn := calcMariaDBTuning(ramMB)
	mariaConf := fmt.Sprintf("[mysqld]\ninnodb_buffer_pool_size = %dM\nmax_connections = %d\ninnodb_log_file_size = 256M\ninnodb_flush_log_at_trx_commit = 2\ninnodb_flush_method = O_DIRECT\nkey_buffer_size = 32M\ntmp_table_size = 64M\nmax_heap_table_size = 64M\nthread_cache_size = 16\nquery_cache_type = 0\n", bufferMB, maxConn)
	os.WriteFile("/etc/mysql/mariadb.conf.d/99-wphpanel.cnf", []byte(mariaConf), 0644)
	run("systemctl", "enable", "mariadb")
	run("systemctl", "restart", "mariadb")

	// PHP 8.4 FPM (host, for phpMyAdmin) — repo already added in step 2
	aptWait()
	run("apt-get", "install", "-y", "-qq",
		"php8.4-fpm", "php8.4-mbstring", "php8.4-zip", "php8.4-gd",
		"php8.4-curl", "php8.4-mysql", "php8.4-xml", "php8.4-intl", "net-tools")

	// phpMyAdmin
	run("bash", "-c", `echo "phpmyadmin phpmyadmin/reconfigure-webserver multiselect none" | debconf-set-selections`)
	run("bash", "-c", `echo "phpmyadmin phpmyadmin/dbconfig-install boolean false" | debconf-set-selections`)
	run("apt-get", "install", "-y", "-qq", "phpmyadmin")

	// SSO signon script
	signonPHP, _ := embeddedFS.ReadFile("embedded/signon.php")
	os.WriteFile("/usr/share/phpmyadmin/signon.php", signonPHP, 0644)

	// Write complete hardened phpMyAdmin config (replaces default entirely)
	pmaConfig := `<?php
/**
 * WPHPanel — phpMyAdmin Configuration
 * Hardened SSO mode: isolated per-user, no global/root access.
 */

if (!function_exists("check_file_access")) {
    function check_file_access(string $path): bool {
        return is_readable($path);
    }
}

if (check_file_access("/var/lib/phpmyadmin/blowfish_secret.inc.php")) {
    require("/var/lib/phpmyadmin/blowfish_secret.inc.php");
}

$i = 0;
$i++;

$cfg["Servers"][$i]["auth_type"] = "signon";
$cfg["Servers"][$i]["SignonSession"] = "SignonSession";
$cfg["Servers"][$i]["SignonURL"] = "/phpmyadmin/signon.php";
$cfg["Servers"][$i]["LogoutURL"] = "/";
$cfg["Servers"][$i]["host"] = "localhost";
$cfg["Servers"][$i]["port"] = 3306;
$cfg["Servers"][$i]["connect_type"] = "tcp";
$cfg["Servers"][$i]["AllowNoPassword"] = false;
$cfg["Servers"][$i]["AllowRoot"] = false;
$cfg["Servers"][$i]["pmadb"] = "";
$cfg["Servers"][$i]["hide_db"] = "^(mysql|sys|performance_schema|information_schema|phpmyadmin)$";

$cfg["ShowChgPassword"] = false;
$cfg["ShowServerInfo"] = false;
$cfg["ShowPhpInfo"] = false;
$cfg["ShowStats"] = true;
$cfg["ShowCreateDb"] = false;
$cfg["VersionCheck"] = false;
$cfg["UploadDir"] = "";
$cfg["SaveDir"] = "";
$cfg["DefaultLang"] = "en";
$cfg["ThemeDefault"] = "pmahomme";
$cfg["MaxNavigationItems"] = 100;
$cfg["LoginCookieValidity"] = 3600;
$cfg["LoginCookieStore"] = 0;

// Suppress configuration storage warning
$cfg["PmaNoRelation_DisableWarning"] = true;
`
	os.WriteFile("/etc/phpmyadmin/config.inc.php", []byte(pmaConfig), 0644)

	// Set PHP session lifetime to match phpMyAdmin cookie validity (suppresses gc_maxlifetime warning)
	os.WriteFile("/etc/php/8.4/fpm/conf.d/99-wphpanel.ini",
		[]byte("[Session]\nsession.gc_maxlifetime = 3600\n"), 0644)

	// ── Host PHP 8.4-FPM auto-restart override (Issue 3) ──
	// Default has Restart=on-failure which misses OOM kills (SIGKILL → exit 0).
	os.MkdirAll("/etc/systemd/system/php8.4-fpm.service.d", 0755)
	os.WriteFile("/etc/systemd/system/php8.4-fpm.service.d/restart.conf",
		[]byte("[Unit]\nStartLimitBurst=10\nStartLimitIntervalSec=120\n\n[Service]\nRestart=always\nRestartSec=3\n"), 0644)
	run("systemctl", "daemon-reload")

	run("systemctl", "enable", "php8.4-fpm")
	run("systemctl", "start", "php8.4-fpm")
}

func installPostgreSQL(creds Credentials) {
	aptWait()
	// Install PostgreSQL from official PGDG repo (added in step 2)
	run("apt-get", "install", "-y", "-qq", "postgresql")
	run("systemctl", "enable", "postgresql")
	run("systemctl", "start", "postgresql")

	ramMB := shellOutputInt("awk '/^MemTotal:/{print int($2/1024)}' /proc/meminfo")
	sharedBuf, effCache, maintWork, workMem := calcPostgreSQLTuning(ramMB)
	cores := shellOutputInt("nproc")
	parallelGather := cores / 2
	parallelWorkers := cores
	parallelMaint := cores / 2

	if parallelGather < 2 { parallelGather = 2 }
	if parallelGather > 4 { parallelGather = 4 }
	if parallelWorkers < 4 { parallelWorkers = 4 }
	if parallelWorkers > 8 { parallelWorkers = 8 }
	if parallelMaint < 2 { parallelMaint = 2 }
	if parallelMaint > 4 { parallelMaint = 4 }

	// 1. Create or update wphpanel role (DO $$ block ensures clean execution)
	userSQL := fmt.Sprintf(`DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'wphpanel') THEN
        CREATE USER wphpanel WITH PASSWORD '%s' NOSUPERUSER CREATEROLE CREATEDB;
    ELSE
        ALTER USER wphpanel WITH PASSWORD '%s' NOSUPERUSER CREATEROLE CREATEDB;
    END IF;
END $$;`, creds.PostgresPass, creds.PostgresPass)
	run("sudo", "-u", "postgres", "psql", "-c", userSQL)

	// 2. Create database (standalone statement)
	run("bash", "-c", "sudo -u postgres psql -lqt | cut -d \\| -f 1 | grep -qw wphpanel || sudo -u postgres psql -c 'CREATE DATABASE wphpanel OWNER wphpanel;'")
	run("sudo", "-u", "postgres", "psql", "-c", "GRANT ALL PRIVILEGES ON DATABASE wphpanel TO wphpanel;")

	tuningSQL := fmt.Sprintf(`
ALTER SYSTEM SET shared_buffers = '%dMB';
ALTER SYSTEM SET work_mem = '%dMB';
ALTER SYSTEM SET effective_cache_size = '%dMB';
ALTER SYSTEM SET maintenance_work_mem = '%dMB';
ALTER SYSTEM SET wal_buffers = '16MB';
ALTER SYSTEM SET wal_compression = 'lz4';
ALTER SYSTEM SET max_wal_size = '2GB';
ALTER SYSTEM SET min_wal_size = '256MB';
ALTER SYSTEM SET checkpoint_completion_target = 0.9;
ALTER SYSTEM SET random_page_cost = 1.1;
ALTER SYSTEM SET effective_io_concurrency = 200;
ALTER SYSTEM SET max_parallel_workers_per_gather = %d;
ALTER SYSTEM SET max_parallel_workers = %d;
ALTER SYSTEM SET max_parallel_maintenance_workers = %d;
ALTER SYSTEM SET max_connections = 100;
ALTER SYSTEM SET idle_in_transaction_session_timeout = '300s';
ALTER SYSTEM SET statement_timeout = '60s';
ALTER SYSTEM SET log_connections = 'on';
ALTER SYSTEM SET log_disconnections = 'on';
ALTER SYSTEM SET log_statement = 'ddl';
ALTER SYSTEM SET log_min_duration_statement = 1000;
ALTER SYSTEM SET log_line_prefix = '%%m [%%p] %%u@%%d ';
ALTER SYSTEM SET shared_preload_libraries = 'pg_stat_statements';
`, sharedBuf, workMem, effCache, maintWork, parallelGather, parallelWorkers, parallelMaint)

	// Write tuning SQL and apply
	tmpSQL := "/tmp/wphpanel-pg-tune.sql"
	os.WriteFile(tmpSQL, []byte(tuningSQL), 0644)
	run("sudo", "-u", "postgres", "psql", "-f", tmpSQL)
	os.Remove(tmpSQL)

	// ── PostgreSQL auto-restart override (Issue 2) ──
	// Default postgresql@.service has Restart=no — panel DB loss = total outage.
	os.MkdirAll("/etc/systemd/system/postgresql@.service.d", 0755)
	os.WriteFile("/etc/systemd/system/postgresql@.service.d/restart.conf",
		[]byte("[Unit]\nStartLimitBurst=10\nStartLimitIntervalSec=120\n\n[Service]\nRestart=on-failure\nRestartSec=5\n"), 0644)
	run("systemctl", "daemon-reload")

	// Restart to apply shared_preload_libraries
	run("systemctl", "restart", "postgresql")

	// Enable extensions in the panel database
	run("sudo", "-u", "postgres", "psql", "-d", "wphpanel", "-c", "CREATE EXTENSION IF NOT EXISTS pg_stat_statements;")

	// ── Database-level ACL lockdown (critical for multi-tenant isolation) ────
	// Without these, any database user could CONNECT to the panel DB, template1,
	// and postgres. This is defense-in-depth alongside pgAdmin server isolation.
	run("sudo", "-u", "postgres", "psql", "-c", "REVOKE CONNECT ON DATABASE wphpanel FROM PUBLIC;")
	run("sudo", "-u", "postgres", "psql", "-c", "REVOKE CONNECT ON DATABASE postgres FROM PUBLIC;")
	run("sudo", "-u", "postgres", "psql", "-c", "REVOKE CONNECT ON DATABASE template1 FROM PUBLIC;")
	run("sudo", "-u", "postgres", "psql", "-c", "GRANT CONNECT ON DATABASE wphpanel TO wphpanel;")
}

func setupSSLDirs(cfg InstallConfig) {
	os.MkdirAll("/etc/letsencrypt/live", 0755)
	os.MkdirAll("/opt/wphpanel/lego", 0700)
}

func installZFSAndIncus() {
	aptWait()
	run("apt-get", "install", "-y", "-qq", "zfsutils-linux", "incus")
	_ = exec.Command("modprobe", "zfs").Run()

	// ZFS ARC limit: 20% of RAM
	ramKB := shellOutputInt("awk '/^MemTotal:/{print $2}' /proc/meminfo")
	arcMax := ramKB * 1024 * 20 / 100
	os.MkdirAll("/etc/modprobe.d", 0755)
	os.WriteFile("/etc/modprobe.d/zfs.conf",
		[]byte(fmt.Sprintf("options zfs zfs_arc_max=%d\n", arcMax)), 0644)

	// Idempotency: skip preseed if default storage pool already exists
	if strings.TrimSpace(shellOutput("incus storage list --format csv 2>/dev/null | grep -c '^default,' || echo 0")) != "0" {
		run("systemctl", "enable", "--now", "incus")
		run("systemctl", "enable", "--now", "incus.socket")
		return
	}

	// Calculate ZFS pool size
	diskAvailGB := shellOutputInt("df -B1G --output=avail / | tail -1")
	diskTotalGB := shellOutputInt("df -B1G --output=size / | tail -1")
	osReserve := 10
	if diskTotalGB <= 30 {
		osReserve = 8
	} else if diskTotalGB <= 60 {
		osReserve = 10
	} else if diskTotalGB <= 100 {
		osReserve = 12
	} else {
		osReserve = 15
	}
	poolSize := diskAvailGB - osReserve
	if poolSize < 2 {
		poolSize = 2
	}

	// Write preseed with ZFS storage pool ONLY (Strict ZFS — enterprise production performance)
	preseedRaw, _ := embeddedFS.ReadFile("embedded/preseed.yaml")
	preseedStr := string(preseedRaw)
	preseedStr = strings.ReplaceAll(preseedStr, "__ZFS_POOL_SIZE__", fmt.Sprintf("%dGiB", poolSize))

	tmpFile := "/tmp/wphpanel-preseed.yaml"
	os.WriteFile(tmpFile, []byte(preseedStr), 0600)
	run("bash", "-c", "cat "+tmpFile+" | incus admin init --preseed")
	os.Remove(tmpFile)

	run("systemctl", "enable", "--now", "incus")
	run("systemctl", "enable", "--now", "incus.socket")
}

func installValkey(creds Credentials) {
	// Valkey 9.0.3 — compiled from source (no official apt repo for v9 yet)
	run("bash", "-c", "curl -fsSL https://github.com/valkey-io/valkey/archive/refs/tags/9.0.3.tar.gz | tar xz -C /tmp")
	run("bash", "-c", "cd /tmp/valkey-9.0.3 && make -j$(nproc) BUILD_TLS=yes && make install")

	// Create valkey user + directories + log file
	run("bash", "-c", "id -u valkey &>/dev/null || useradd -r -s /sbin/nologin valkey")
	os.MkdirAll("/etc/valkey", 0755)
	os.MkdirAll("/var/lib/valkey", 0755)
	run("chown", "-R", "valkey:valkey", "/var/lib/valkey")
	run("bash", "-c", "touch /var/log/valkey.log && chown valkey:valkey /var/log/valkey.log")
	run("sysctl", "-w", "vm.overcommit_memory=1")
	appendToFile("/etc/sysctl.d/99-wphpanel.conf", "vm.overcommit_memory=1\n")

	// Initial ACL: disable passwordless default; panel creates per-site users in users.acl.
	os.WriteFile("/etc/valkey/users.acl", []byte("user default off\n"), 0640)
	run("chown", "-R", "valkey:valkey", "/etc/valkey")

	// Bind panel + loopback only. protected-mode must be off so Incus containers can AUTH as ACL
	// users via the bridge IP (10.100.0.1). protected-mode yes + default nopass rejects them.
	valkeyConf := fmt.Sprintf(`bind 127.0.0.1 10.100.0.1
protected-mode no
port 6379
requirepass "%s"
aclfile /etc/valkey/users.acl
maxmemory 512mb
maxmemory-policy allkeys-lru
save 900 1
save 300 10
save 60 10000
dir /var/lib/valkey
daemonize no
loglevel notice
logfile /var/log/valkey.log
io-threads 2
io-threads-do-reads yes
`, creds.ValkeyPass)
	os.WriteFile("/etc/valkey/valkey.conf", []byte(valkeyConf), 0644)

	// Create systemd service
	valkeyService := `[Unit]
Description=Valkey 9 In-Memory Data Store
After=network.target
StartLimitBurst=10
StartLimitIntervalSec=120

[Service]
Type=simple
User=valkey
Group=valkey
ExecStart=/usr/local/bin/valkey-server /etc/valkey/valkey.conf
Restart=always
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`
	os.WriteFile("/etc/systemd/system/valkey.service", []byte(valkeyService), 0644)
	run("systemctl", "daemon-reload")
	run("systemctl", "enable", "valkey")
	run("systemctl", "start", "valkey")

	// Cleanup source
	os.RemoveAll("/tmp/valkey-9.0.3")
}

func setupFirewall() {
	run("ufw", "allow", "22/tcp")
	run("ufw", "allow", "80/tcp")
	run("ufw", "allow", "443/tcp")
	run("ufw", "allow", "8090/tcp") // installer port
	run("ufw", "allow", "in", "on", "wphpanel-net")
	run("ufw", "allow", "out", "on", "wphpanel-net")
	run("ufw", "route", "allow", "in", "on", "wphpanel-net")
	run("ufw", "route", "allow", "out", "on", "wphpanel-net")
	run("ufw", "allow", "67/udp")
	run("ufw", "allow", "68/udp")
	// Allow container NAT: set FORWARD policy to ACCEPT
	run("bash", "-c", `sed -i 's/DEFAULT_FORWARD_POLICY="DROP"/DEFAULT_FORWARD_POLICY="ACCEPT"/' /etc/default/ufw`)
	run("bash", "-c", "ufw --force enable")
}

func setupSwap() {
	if shellOutput("swapon --show | wc -l") != "0" {
		return
	}
	ramMB := shellOutputInt("free -m | awk '/^Mem:/{print $2}'")
	swapMB := 2048
	if ramMB > 8192 {
		swapMB = 4096
	}
	if ramMB > 32768 {
		swapMB = 8192
	}
	run("bash", "-c", fmt.Sprintf("fallocate -l %dM /swapfile 2>/dev/null || dd if=/dev/zero of=/swapfile bs=1M count=%d status=none", swapMB, swapMB))
	run("chmod", "600", "/swapfile")
	run("mkswap", "/swapfile")
	run("swapon", "/swapfile")
	// Idempotency: only add fstab entry if not already present (Issue 11)
	if !strings.Contains(shellOutput("grep -c swapfile /etc/fstab 2>/dev/null || echo 0"), "0") {
		// Already has swapfile entry — skip
	} else {
		appendToFile("/etc/fstab", "/swapfile none swap sw 0 0\n")
	}
}

func setupKernelTuning() {
	sysctl := `net.ipv4.ip_forward=1
net.ipv6.conf.all.forwarding=1
vm.swappiness=10
vm.vfs_cache_pressure=50
net.core.somaxconn=65535
fs.file-max=1048576
net.ipv4.tcp_tw_reuse=1
net.ipv4.tcp_fin_timeout=15
net.core.netdev_max_backlog=65535
net.ipv4.tcp_max_syn_backlog=65535
`
	os.WriteFile("/etc/sysctl.d/99-wphpanel.conf", []byte(sysctl), 0644)
	run("sysctl", "--system")

	// System limits
	limits := "* soft nofile 1048576\n* hard nofile 1048576\nroot soft nofile 1048576\nroot hard nofile 1048576\n"
	os.WriteFile("/etc/security/limits.d/99-wphpanel.conf", []byte(limits), 0644)

	os.MkdirAll("/etc/systemd/system.conf.d", 0755)
	os.WriteFile("/etc/systemd/system.conf.d/99-wphpanel.conf",
		[]byte("[Manager]\nDefaultLimitNOFILE=1048576\n"), 0644)
	run("systemctl", "daemon-reload")
}

func setupDirectories(cfg InstallConfig, creds Credentials) {
	for _, d := range []string{"/opt/wphpanel/bin", "/opt/wphpanel/logs", "/opt/wphpanel/data",
		"/opt/wphpanel/data/backups",
		"/opt/wphpanel/error-pages", "/var/www/wphpanel", "/var/www/acme-challenge"} {
		os.MkdirAll(d, 0755)
	}
	// JWT secret
	os.WriteFile("/opt/wphpanel/.jwt_secret", []byte(creds.JWTSecret), 0600)
	// Error pages
	writeErrorPages()

	// ── API log rotation (Issue 5) ──
	// Prevents /opt/wphpanel/logs/api.log from growing indefinitely and filling disk.
	logrotateConf := `/opt/wphpanel/logs/*.log {
    daily
    missingok
    rotate 14
    compress
    delaycompress
    notifempty
    create 0644 root root
    copytruncate
}
`
	os.WriteFile("/etc/logrotate.d/wphpanel", []byte(logrotateConf), 0644)
}

func writeEnvFile(cfg InstallConfig, creds Credentials) {
	env := fmt.Sprintf(`PORT=8080
HOSTNAME=%s
DATABASE_URL=postgres://wphpanel:%s@127.0.0.1:5432/wphpanel?sslmode=disable
VALKEY_ADDR=127.0.0.1:6379
VALKEY_PASSWORD=%s
VALKEY_DB=0
JWT_SECRET=%s
JWT_EXPIRE_HOURS=72
ADMIN_EMAIL=%s
ADMIN_PASSWORD=%s
INCUS_SOCKET=/var/lib/incus/unix.socket
`, cfg.Hostname, creds.PostgresPass, creds.ValkeyPass, creds.JWTSecret, cfg.AdminEmail, cfg.AdminPassword)
	os.WriteFile("/opt/wphpanel/.env", []byte(env), 0600)
}

func installPanelBinary() {
	// If binary already exists (e.g. pre-placed during testing), make executable & return
	if _, err := os.Stat("/opt/wphpanel/bin/wphpanel-api"); err == nil {
		os.Chmod("/opt/wphpanel/bin/wphpanel-api", 0755)
		return
	}

	// Download single unified binary (Backend API + Embedded React SPA + SQL Migrations)
	releaseURL := "https://raw.githubusercontent.com/SagorWeb/wphp/main/build/wphpanel-linux-amd64.tar.gz"
	if customURL := os.Getenv("WPHPANEL_BINARY_URL"); customURL != "" {
		releaseURL = customURL
	}

	tmpArchive := "/tmp/wphpanel-linux-amd64.tar.gz"
	run("curl", "-fsSL", releaseURL, "-o", tmpArchive)
	run("tar", "-xzf", tmpArchive, "-C", "/opt/wphpanel/bin/")
	if _, err := os.Stat("/opt/wphpanel/bin/wphpanel-linux-amd64"); err == nil {
		os.Rename("/opt/wphpanel/bin/wphpanel-linux-amd64", "/opt/wphpanel/bin/wphpanel-api")
	}
	os.Chmod("/opt/wphpanel/bin/wphpanel-api", 0755)
	os.Remove(tmpArchive)
}

func writeSystemdService() {
	installPanelBinary()

	svc := `[Unit]
Description=WPHPanel API Server
After=network.target postgresql.service
Wants=postgresql.service
StartLimitBurst=10
StartLimitIntervalSec=120

[Service]
Type=simple
User=root
WorkingDirectory=/opt/wphpanel
EnvironmentFile=/opt/wphpanel/.env
ExecStart=/opt/wphpanel/bin/wphpanel-api
Restart=always
RestartSec=5
StandardOutput=append:/opt/wphpanel/logs/api.log
StandardError=append:/opt/wphpanel/logs/api.log

[Install]
WantedBy=multi-user.target
`
	os.WriteFile("/etc/systemd/system/wphpanel-api.service", []byte(svc), 0644)
	run("systemctl", "daemon-reload")
	run("systemctl", "enable", "wphpanel-api")
	run("systemctl", "start", "wphpanel-api")
}

func writeNginxCatchall() {
	// Generate self-signed fallback cert for the 443 catchall
	os.MkdirAll("/etc/ssl/wphpanel", 0755)
	run("bash", "-c", `openssl req -x509 -nodes -days 3650 -newkey rsa:2048 \
		-keyout /etc/ssl/wphpanel/default.key \
		-out /etc/ssl/wphpanel/default.crt \
		-subj '/CN=wphpanel-default/O=WPHPanel' 2>/dev/null`)

	conf := `# WPHPanel — Default Catchall (HTTP + HTTPS)
# Handles ACME challenges + error pages for unconfigured domains

server {
    listen 80 default_server;
    listen [::]:80 default_server;
    server_name _;
    location /.well-known/acme-challenge/ { root /var/www/acme-challenge; try_files $uri =404; }
    location / { root /opt/wphpanel/error-pages; try_files /default.html =404; }
}

server {
    listen 443 ssl default_server;
    listen [::]:443 ssl default_server;
    http2 on;
    server_name _;

    ssl_certificate /etc/ssl/wphpanel/default.crt;
    ssl_certificate_key /etc/ssl/wphpanel/default.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    location /.well-known/acme-challenge/ { root /var/www/acme-challenge; try_files $uri =404; }
    location / { root /opt/wphpanel/error-pages; try_files /default.html =404; }
}
`
	os.WriteFile("/etc/nginx/sites-available/000-default-catchall", []byte(conf), 0644)
	os.Symlink("/etc/nginx/sites-available/000-default-catchall", "/etc/nginx/sites-enabled/000-default-catchall")
}

func writePanelNginx(cfg InstallConfig) {
	serverIP := strings.TrimSpace(shellOutput("curl -s4 ifconfig.me --connect-timeout 5 2>/dev/null"))
	if serverIP == "" {
		serverIP = detectServerIP()
	}

	// Step 1: Write initial config with self-signed cert (works immediately)
	initialConf := fmt.Sprintf(`# pgAdmin SSO maps
# pgAdmin SSO — HMAC-signed cookie validation via auth_request
# Login gate: "0" = block (no cookie at all), "1" = has cookie (validated by auth_request)
map $cookie___pgadmin_sso $pgadmin_login_allowed {
    default  "0";
    "~.+"    "1";
}

# pgAdmin SSO: Only inject X-Forwarded-User when __pgadmin_sso cookie exists.
map $cookie___pgadmin_sso $pgadmin_sso_user {
    default  "";
    "~.+"    $cookie___pgadmin_sso;
}

server {
    listen 80;
    listen 443 ssl;
    http2 on;
    server_name %s %s;

    ssl_certificate /etc/ssl/wphpanel/default.crt;
    ssl_certificate_key /etc/ssl/wphpanel/default.key;
    ssl_protocols TLSv1.2 TLSv1.3;

    location /.well-known/acme-challenge/ {
        root /var/www/acme-challenge;
        try_files $uri =404;
    }

    # phpMyAdmin — SSO login + database management
    location ^~ /phpmyadmin {
        root /usr/share/;
        index index.php;
        try_files $uri $uri/ =404;

        location ~ \.php$ {
            include fastcgi_params;
            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
            fastcgi_pass unix:/run/php/php8.4-fpm.sock;
            fastcgi_read_timeout 300;
        }
    }

    # ── pgAdmin 4 — Multi-Tenant Hardened ────────────────────────────────────

    # Internal auth_request endpoint — validates HMAC-signed __pgadmin_sso cookie.
    location = /_pgadmin_auth {
        internal;
        proxy_pass http://127.0.0.1:8080/api/v1/databases/pgadmin-auth-check;
        proxy_pass_request_body off;
        proxy_set_header Content-Length "";
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Login page — SSO only. Uses auth_request to validate HMAC-signed cookie.
    location = /pgadmin4/login {
        if ($pgadmin_login_allowed = "0") {
            return 302 /;
        }
        auth_request /_pgadmin_auth;
        auth_request_set $pgadmin_user $upstream_http_x_pgadmin_user;
        error_page 401 = @pgadmin_denied;

        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_user;
        proxy_redirect off;
        add_header Cache-Control "no-store, no-cache, must-revalidate" always;
    }

    location @pgadmin_denied {
        return 302 /;
    }

    # ── BLOCKED ROUTES — Security-sensitive operations ──────────────────────

    # 1. Password management — managed by WPHPanel only
    location ~ ^/pgadmin4/(reset_password|forgot_password|change_password) {
        return 403;
    }

    # 2. User management — block write operations but allow current_user.js
    #    current_user.js is a read-only JS file that pgAdmin's React app
    #    REQUIRES to initialize. Without it, pgAdmin hangs on "Loading...".
    location = /pgadmin4/user_management/current_user.js {
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }
    location ~ ^/pgadmin4/user_management/ {
        return 403;
    }

    # 3. Server management — prevent users from adding/editing/deleting servers
    location ~ ^/pgadmin4/browser/server/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 4. Server change_password endpoint — block ALTER USER password changes
    location ~ ^/pgadmin4/browser/server/change_password/ {
        return 403;
    }

    # 5. Role/Login management — block creating/editing/deleting PostgreSQL roles
    location ~ ^/pgadmin4/browser/role/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 6. Server group management — prevent adding/editing server groups
    location ~ ^/pgadmin4/browser/server_group/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 7. Cloud deployment — not applicable for hosting
    location ~ ^/pgadmin4/misc/cloud/ {
        return 403;
    }

    # 8. Shared server management — prevent sharing servers between users
    location ~ ^/pgadmin4/browser/shared_server/ {
        return 403;
    }

    # 9. pgAdmin file manager — block filesystem access
    location ~ ^/pgadmin4/file_manager/ {
        return 403;
    }

    # pgAdmin general — session-based access after SSO login.
    location /pgadmin4 {
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
        add_header Cache-Control "no-store, no-cache, must-revalidate" always;
    }

    # Single-Binary Proxy — Backend API + Embedded Frontend SPA + WebSockets
    location / {
        client_max_body_size 512M;
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
        proxy_buffering off;
    }
}
`, cfg.Hostname, serverIP)
	os.WriteFile("/etc/nginx/sites-available/wphpanel.conf", []byte(initialConf), 0644)
	os.Symlink("/etc/nginx/sites-available/wphpanel.conf", "/etc/nginx/sites-enabled/wphpanel.conf")
	run("bash", "-c", "nginx -t 2>/dev/null && systemctl reload nginx || true")

	// Step 2: Try to issue real SSL cert via Lego (best-effort)
	issueErr := issueSSLViaLego(cfg.Hostname, cfg.AdminEmail)
	if issueErr != nil {
		fmt.Printf("SSL generation failed: %v\n", issueErr)
		return // SSL failed — panel stays on self-signed cert
	}
	certDir := "/etc/letsencrypt/live/" + cfg.Hostname

	// Step 4: Upgrade to real cert with HTTP→HTTPS redirect
	sslConf := fmt.Sprintf(`# pgAdmin SSO maps
# pgAdmin SSO — HMAC-signed cookie validation via auth_request
# Login gate: "0" = block (no cookie at all), "1" = has cookie (validated by auth_request)
map $cookie___pgadmin_sso $pgadmin_login_allowed {
    default  "0";
    "~.+"    "1";
}

# pgAdmin SSO: Only inject X-Forwarded-User when __pgadmin_sso cookie exists.
map $cookie___pgadmin_sso $pgadmin_sso_user {
    default  "";
    "~.+"    $cookie___pgadmin_sso;
}

server {
    listen 80;
    server_name %s %s;
    location /.well-known/acme-challenge/ {
        root /var/www/acme-challenge;
        try_files $uri =404;
    }
    location / {
        return 301 https://$host$request_uri;
    }
}

server {
    listen 443 ssl;
    http2 on;
    server_name %s %s;

    ssl_certificate %s/fullchain.pem;
    ssl_certificate_key %s/privkey.pem;
    ssl_protocols TLSv1.2 TLSv1.3;
    ssl_ciphers ECDHE-ECDSA-AES128-GCM-SHA256:ECDHE-RSA-AES128-GCM-SHA256:ECDHE-ECDSA-AES256-GCM-SHA384:ECDHE-RSA-AES256-GCM-SHA384;
    ssl_prefer_server_ciphers off;
    ssl_session_cache shared:SSL:10m;
    ssl_session_timeout 1d;
    add_header Strict-Transport-Security "max-age=63072000; includeSubDomains; preload" always;

    location /.well-known/acme-challenge/ {
        root /var/www/acme-challenge;
        try_files $uri =404;
    }

    # phpMyAdmin — SSO login + database management
    location ^~ /phpmyadmin {
        root /usr/share/;
        index index.php;
        try_files $uri $uri/ =404;

        location ~ \.php$ {
            include fastcgi_params;
            fastcgi_param SCRIPT_FILENAME $document_root$fastcgi_script_name;
            fastcgi_pass unix:/run/php/php8.4-fpm.sock;
            fastcgi_read_timeout 300;
        }
    }

    # ── pgAdmin 4 — Multi-Tenant Hardened ────────────────────────────────────

    # Internal auth_request endpoint — validates HMAC-signed __pgadmin_sso cookie.
    location = /_pgadmin_auth {
        internal;
        proxy_pass http://127.0.0.1:8080/api/v1/databases/pgadmin-auth-check;
        proxy_pass_request_body off;
        proxy_set_header Content-Length "";
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    # Login page — SSO only. Uses auth_request to validate HMAC-signed cookie.
    location = /pgadmin4/login {
        if ($pgadmin_login_allowed = "0") {
            return 302 /;
        }
        auth_request /_pgadmin_auth;
        auth_request_set $pgadmin_user $upstream_http_x_pgadmin_user;
        error_page 401 = @pgadmin_denied;

        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_user;
        proxy_redirect off;
        add_header Cache-Control "no-store, no-cache, must-revalidate" always;
    }

    location @pgadmin_denied {
        return 302 /;
    }

    # ── BLOCKED ROUTES — Security-sensitive operations ──────────────────────

    # 1. Password management — managed by WPHPanel only
    location ~ ^/pgadmin4/(reset_password|forgot_password|change_password) {
        return 403;
    }

    # 2. User management — block write operations but allow current_user.js
    #    current_user.js is a read-only JS file that pgAdmin's React app
    #    REQUIRES to initialize. Without it, pgAdmin hangs on "Loading...".
    location = /pgadmin4/user_management/current_user.js {
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }
    location ~ ^/pgadmin4/user_management/ {
        return 403;
    }

    # 3. Server management — prevent users from adding/editing/deleting servers
    location ~ ^/pgadmin4/browser/server/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 4. Server change_password endpoint — block ALTER USER password changes
    location ~ ^/pgadmin4/browser/server/change_password/ {
        return 403;
    }

    # 5. Role/Login management — block creating/editing/deleting PostgreSQL roles
    location ~ ^/pgadmin4/browser/role/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 6. Server group management — prevent adding/editing server groups
    location ~ ^/pgadmin4/browser/server_group/obj/ {
        limit_except GET {
            deny all;
        }
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
    }

    # 7. Cloud deployment — not applicable for hosting
    location ~ ^/pgadmin4/misc/cloud/ {
        return 403;
    }

    # 8. Shared server management — prevent sharing servers between users
    location ~ ^/pgadmin4/browser/shared_server/ {
        return 403;
    }

    # 9. pgAdmin file manager — block filesystem access
    location ~ ^/pgadmin4/file_manager/ {
        return 403;
    }

    # pgAdmin general — session-based access after SSO login.
    location /pgadmin4 {
        proxy_pass http://127.0.0.1:5050;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header X-Script-Name /pgadmin4;
        proxy_set_header X-Forwarded-User $pgadmin_sso_user;
        proxy_redirect off;
        add_header Cache-Control "no-store, no-cache, must-revalidate" always;
    }

    # Single-Binary Proxy — Backend API + Embedded Frontend SPA + WebSockets
    location / {
        client_max_body_size 512M;
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_read_timeout 86400s;
        proxy_send_timeout 86400s;
        proxy_buffering off;
    }
}
`, cfg.Hostname, serverIP, cfg.Hostname, serverIP, certDir, certDir)
	os.WriteFile("/etc/nginx/sites-available/wphpanel.conf", []byte(sslConf), 0644)
	run("bash", "-c", "nginx -t 2>/dev/null && systemctl reload nginx || true")
}

func pinPackages() {
	// Pin packages to prevent accidental removal during apt dist-upgrade.
	// NOTE: Incus is NOT pinned — Zabbly stable repo manages version transitions safely.
	// pgadmin4-server is pinned to prevent apt upgrade from overwriting our
	// deterministic pass_enc_key patch in webserver.py.
	pgVer := strings.TrimSpace(shellOutput("psql --version 2>/dev/null | grep -oP '\\d+' | head -1"))
	if pgVer == "" {
		pgVer = "17"
	}
	run("apt-mark", "hold", "mariadb-server", "zfsutils-linux", "nginx", "postgresql-"+pgVer, "pgadmin4-web", "pgadmin4-server")
}

// writeSSLRenewalTimer creates a systemd timer that renews Let's Encrypt certs daily.
// Without this, certificates expire after 90 days and the panel HTTPS breaks.
func writeSSLRenewalTimer() {
	renewalScript := `#!/bin/bash
# WPHPanel — SSL Certificate Auto-Renewal
# Checks all certs in /etc/letsencrypt/live/ and renews if < 30 days remaining.
set -e

for CERT_DIR in /etc/letsencrypt/live/*/; do
  DOMAIN=$(basename "$CERT_DIR")
  CERT="${CERT_DIR}fullchain.pem"
  [ -f "$CERT" ] || continue

  # Check expiry (renew if < 30 days remaining)
  if ! openssl x509 -checkend 2592000 -noout -in "$CERT" 2>/dev/null; then
    logger -t ssl-renew "Certificate for ${DOMAIN} expires within 30 days — renewing"
    # Use the panel API binary if available, otherwise skip
    if [ -x /opt/wphpanel/bin/wphpanel-api ]; then
      # The API handles renewal via Lego library
      curl -s --max-time 120 http://127.0.0.1:8080/api/v1/ssl/renew/${DOMAIN} 2>/dev/null || true
    fi
  fi
done

# Reload nginx to pick up any renewed certs
nginx -t 2>/dev/null && systemctl reload nginx 2>/dev/null || true
`
	os.WriteFile("/opt/wphpanel/bin/ssl-renew.sh", []byte(renewalScript), 0755)

	sslService := `[Unit]
Description=WPHPanel SSL Certificate Renewal
[Service]
Type=oneshot
ExecStart=/opt/wphpanel/bin/ssl-renew.sh
`
	os.WriteFile("/etc/systemd/system/wphpanel-ssl-renew.service", []byte(sslService), 0644)

	sslTimer := `[Unit]
Description=WPHPanel SSL Certificate Renewal Timer
[Timer]
OnCalendar=*-*-* 02:30:00
RandomizedDelaySec=3600
Persistent=true
[Install]
WantedBy=timers.target
`
	os.WriteFile("/etc/systemd/system/wphpanel-ssl-renew.timer", []byte(sslTimer), 0644)
	run("systemctl", "daemon-reload")
	run("systemctl", "enable", "wphpanel-ssl-renew.timer")
	run("systemctl", "start", "wphpanel-ssl-renew.timer")
}

// ── Helpers ──

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Env = append(os.Environ(),
		"DEBIAN_FRONTEND=noninteractive",
		"PAGER=cat",
		"NEEDRESTART_MODE=a",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func aptWait() {
	for i := 0; i < 60; i++ {
		out, _ := exec.Command("bash", "-c", "fuser /var/lib/dpkg/lock-frontend 2>/dev/null").Output()
		if len(strings.TrimSpace(string(out))) == 0 {
			return
		}
		time.Sleep(5 * time.Second)
	}
}

func replaceInFile(path, search, replace string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		if strings.Contains(line, search) {
			lines[i] = replace
		}
	}
	os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644)
}

func appendToFile(path, content string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(content)
}

func shellOutputInt(cmd string) int {
	s := shellOutput(cmd)
	var v int
	fmt.Sscanf(s, "%d", &v)
	return v
}

func calcMariaDBTuning(ramMB int) (bufferMB, maxConn int) {
	bufferMB = ramMB * 30 / 100
	if bufferMB < 512 {
		bufferMB = 512
	}
	maxConn = 100
	switch {
	case ramMB <= 2048:
		if bufferMB > 512 { bufferMB = 512 }
		maxConn = 100
	case ramMB <= 4096:
		if bufferMB > 1536 { bufferMB = 1536 }
		maxConn = 200
	case ramMB <= 8192:
		if bufferMB > 3072 { bufferMB = 3072 }
		maxConn = 300
	case ramMB <= 16384:
		if bufferMB > 6144 { bufferMB = 6144 }
		maxConn = 500
	default:
		if bufferMB > 20480 { bufferMB = 20480 }
		maxConn = 1000
	}
	return
}

func writeErrorPages() {
	pages := map[string]string{
		"default.html": errorPageHTML("No Website Configured", "This domain is not currently associated with any website on this server.", "#8b95b0", `<rect x="2" y="2" width="20" height="8" rx="2"/><circle cx="7" cy="6" r="1"/><rect x="2" y="14" width="20" height="8" rx="2"/><circle cx="7" cy="18" r="1"/>`),
		"wphpanel_502.html": errorPageHTML("Bad Gateway", "The upstream application returned an invalid response. Please try again.", "#6366f1", `<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/><path d="M12 9v4M12 17h.01"/>`),
		"wphpanel_503.html": errorPageHTML("Service Unavailable", "The server is temporarily unable to handle your request.", "#f59e0b", `<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>`),
		"suspended.html": errorPageHTML("Account Suspended", "This website has been suspended. Contact your hosting provider.", "#ef4444", `<circle cx="12" cy="12" r="10"/><path d="M15 9l-6 6M9 9l6 6"/>`),
		"offline.html": errorPageHTML("Site Temporarily Offline", "This website is currently offline for maintenance.", "#f59e0b", `<circle cx="12" cy="12" r="10"/><path d="M12 8v4M12 16h.01"/>`),
	}
	for name, html := range pages {
		os.WriteFile("/opt/wphpanel/error-pages/"+name, []byte(html), 0644)
	}
}

func errorPageHTML(title, msg, color, svgPath string) string {
	return fmt.Sprintf(`<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><style>*{margin:0;padding:0;box-sizing:border-box}body{min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f1629;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:#fff}.c{text-align:center;max-width:520px;padding:40px 24px}.icon{width:80px;height:80px;margin:0 auto 32px;opacity:.3}.icon svg{width:100%%;height:100%%;stroke:%s;stroke-width:1.5;fill:none}h1{font-size:32px;font-weight:700;margin-bottom:12px;color:#e2e8f0}p{font-size:15px;color:#64748b;line-height:1.6}.footer{position:fixed;bottom:24px;left:0;right:0;text-align:center;font-size:11px;color:#334155}.footer a{color:#475569;text-decoration:none}</style></head><body><div class="c"><div class="icon"><svg viewBox="0 0 24 24">%s</svg></div><h1>%s</h1><p>%s</p></div><div class="footer">Powered by <a href="#">WPHPanel</a></div></body></html>`, title, color, svgPath, title, msg)
}

// ──────────────────────────────────────────────────────────────────────
// Native SSL generation via Lego for panel hostname
// ──────────────────────────────────────────────────────────────────────

type legoUser struct {
	email        string
	registration *acme.ExtendedAccount
	key          *ecdsa.PrivateKey
}

func (u *legoUser) GetEmail() string                       { return u.email }
func (u *legoUser) GetRegistration() *acme.ExtendedAccount { return u.registration }
func (u *legoUser) GetPrivateKey() crypto.Signer           { return u.key }

func issueSSLViaLego(hostname, email string) error {
	stateDir := "/opt/wphpanel/lego"
	os.MkdirAll(stateDir, 0700)

	keyPath := filepath.Join(stateDir, "account.key")
	uriPath := filepath.Join(stateDir, "account.uri")

	var privateKey *ecdsa.PrivateKey
	var err error

	// Try loading existing key
	if keyBytes, err := os.ReadFile(keyPath); err == nil {
		block, _ := pem.Decode(keyBytes)
		if block != nil {
			privateKey, _ = x509.ParseECPrivateKey(block.Bytes)
		}
	}

	if privateKey == nil {
		privateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return fmt.Errorf("generate key: %w", err)
		}
		keyDER, _ := x509.MarshalECPrivateKey(privateKey)
		keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
		os.WriteFile(keyPath, keyPEM, 0600)
	}

	user := &legoUser{
		email: email,
		key:   privateKey,
	}

	if uriBytes, err := os.ReadFile(uriPath); err == nil {
		uriStr := strings.TrimSpace(string(uriBytes))
		if strings.HasPrefix(uriStr, "https://") {
			user.registration = &acme.ExtendedAccount{
				Location: uriStr,
			}
		}
	}

	config := lego.NewConfig(user)
	config.CADirURL = lego.DirectoryURLLetsEncrypt

	client, err := lego.NewClient(config)
	if err != nil {
		return fmt.Errorf("create ACME client: %w", err)
	}

	// Webroot provider (local filesystem since installer runs on host)
	webrootProvider, err := webroot.NewHTTPProvider("/var/www/acme-challenge")
	if err != nil {
		return fmt.Errorf("init webroot provider: %w", err)
	}
	if err := client.Challenge.SetHTTP01Provider(webrootProvider); err != nil {
		return fmt.Errorf("set HTTP-01 provider: %w", err)
	}

	// Register account if needed
	if user.registration == nil {
		reg, err := client.Registration.Register(context.Background(), registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return fmt.Errorf("ACME registration: %w", err)
		}
		user.registration = reg
		os.WriteFile(uriPath, []byte(reg.Location), 0644)
	}

	request := certificate.ObtainRequest{
		Domains: []string{hostname},
		Bundle:  true,
		KeyType: certcrypto.EC256,
	}

	certificates, err := client.Certificate.Obtain(context.Background(), request)
	if err != nil {
		return fmt.Errorf("obtain cert: %w", err)
	}

	certDir := "/etc/letsencrypt/live/" + hostname
	os.MkdirAll(certDir, 0755)

	if len(certificates.Certificate) > 0 {
		os.WriteFile(filepath.Join(certDir, "fullchain.pem"), certificates.Certificate, 0644)
		certPEM := string(certificates.Certificate)
		block, rest := pem.Decode([]byte(certPEM))
		if block != nil {
			leafPEM := pem.EncodeToMemory(block)
			os.WriteFile(filepath.Join(certDir, "cert.pem"), leafPEM, 0644)
			if len(rest) > 0 {
				os.WriteFile(filepath.Join(certDir, "chain.pem"), []byte(rest), 0644)
			}
		}
	}

	if len(certificates.PrivateKey) > 0 {
		os.WriteFile(filepath.Join(certDir, "privkey.pem"), certificates.PrivateKey, 0600)
	}

	return nil
}

// ── Hardening Helpers ───────────────────────────────────────────────────────────

func calcPostgreSQLTuning(ramMB int) (sharedBuf, effCache, maintWork, workMem int) {
	sharedBuf = ramMB / 4
	effCache = ramMB * 3 / 4
	maintWork = ramMB / 32
	workMem = ramMB / 512

	if sharedBuf < 256 {
		sharedBuf = 256
	}
	if sharedBuf > 8192 {
		sharedBuf = 8192
	}
	if effCache < 512 {
		effCache = 512
	}
	if maintWork < 128 {
		maintWork = 128
	}
	if maintWork > 2048 {
		maintWork = 2048
	}
	if workMem < 4 {
		workMem = 4
	}
	if workMem > 64 {
		workMem = 64
	}
	return
}

func installPgAdmin(cfg InstallConfig, creds Credentials, codename string) {
	// Add pgAdmin repository
	run("bash", "-c", "curl -fsS https://www.pgadmin.org/static/packages_pgadmin_org.pub | gpg --batch --yes --dearmor -o /usr/share/keyrings/pgadmin.gpg")
	os.WriteFile("/etc/apt/sources.list.d/pgadmin4.list",
		[]byte(fmt.Sprintf("deb [signed-by=/usr/share/keyrings/pgadmin.gpg] https://ftp.postgresql.org/pub/pgadmin/pgadmin4/apt/%s pgadmin4 main", codename)), 0644)

	aptWait()
	run("apt-get", "update", "-y", "-qq")

	// Try installing pgadmin4-web
	err := run("apt-get", "install", "-y", "-qq", "pgadmin4-web")
	if err != nil {
		fmt.Printf("WARNING: pgadmin4-web apt install failed, trying pip fallback: %v\n", err)
		run("apt-get", "install", "-y", "-qq", "python3-pip", "python3-venv", "libpq-dev")
		run("python3", "-m", "venv", "/opt/pgadmin4-venv")
		run("/opt/pgadmin4-venv/bin/pip", "install", "pgadmin4")
	}

	pgadminConfigDir := "/var/lib/pgadmin"
	os.MkdirAll(pgadminConfigDir, 0755)

	// Write config_local.py
	// NOTE: The find pattern must match the actual path structure.
	// apt pgadmin4-web installs to /usr/pgadmin4/web/config.py (not /usr/pgadmin4/config.py).
	pythonPathBytes, _ := exec.Command("bash", "-c", "find /usr -path '*/pgadmin4/web/config.py' -type f 2>/dev/null | head -1").Output()
	pythonPath := strings.TrimSpace(string(pythonPathBytes))
	if pythonPath == "" {
		// Fallback: pip/venv installs may have a different structure
		pythonPathBytes, _ = exec.Command("bash", "-c", "find /opt -name 'config.py' -path '*/pgadmin4/*' -not -path '*/venv/lib/*' -type f 2>/dev/null | head -1").Output()
		pythonPath = strings.TrimSpace(string(pythonPathBytes))
	}
	if pythonPath == "" {
		// Last resort: any config.py under pgadmin4, excluding virtualenv internals
		pythonPathBytes, _ = exec.Command("bash", "-c", "find /usr /opt -name 'config.py' -path '*/pgadmin4/*' -not -path '*/venv/lib/*' -not -path '*/site-packages/*/config.py' -type f 2>/dev/null | head -1").Output()
		pythonPath = strings.TrimSpace(string(pythonPathBytes))
	}

	if pythonPath != "" {
		configLocalPath := filepath.Join(filepath.Dir(pythonPath), "config_local.py")
		configLocal := fmt.Sprintf(`# ═══════════════════════════════════════════════════════════════════════════════
# pgAdmin 4 — WPHPanel Multi-Tenant Hardened Configuration
# ═══════════════════════════════════════════════════════════════════════════════
# Shared hosting panel mode:
#   - SSO via WPHPanel (webserver auth + __pgadmin_sso cookie)
#   - Password changes ONLY through WPHPanel
#   - Full SQL/data management for users own applications
#   - Admin/infrastructure features locked down
#   - Database visibility restricted per-user via db_res
# ═══════════════════════════════════════════════════════════════════════════════

import os

# ── Server Binding ────────────────────────────────────────────────────────────
DEFAULT_SERVER = "127.0.0.1"
DEFAULT_SERVER_PORT = 5050
SERVER_MODE = True

# ── Reverse Proxy Path ────────────────────────────────────────────────────────
APPLICATION_ROOT = "/pgadmin4"

# ── Data Storage ──────────────────────────────────────────────────────────────
DATA_DIR = "%s"
SESSION_DB_PATH = os.path.join(DATA_DIR, "sessions")
STORAGE_DIR = os.path.join(DATA_DIR, "storage")
SQLITE_PATH = os.path.join(DATA_DIR, "pgadmin4.db")

# ══════════════════════════════════════════════════════════════════════════════
# AUTHENTICATION — Webserver SSO (primary) + Internal (fallback)
# ══════════════════════════════════════════════════════════════════════════════
AUTHENTICATION_SOURCES = ["webserver", "internal"]
WEBSERVER_REMOTE_USER = "X-Forwarded-User"
WEBSERVER_AUTO_CREATE_USER = True
ENHANCED_COOKIE_PROTECTION = False
MASTER_PASSWORD_REQUIRED = False

# ══════════════════════════════════════════════════════════════════════════════
# SECURITY — Multi-Tenant Hosting Hardening
# ══════════════════════════════════════════════════════════════════════════════

# --- Credentials ---
ALLOW_SAVE_PASSWORD = True        # Users can save DB password in their session

# --- Block Dangerous Operations ---
ENABLE_SERVER_PASS_EXEC_CMD = False   # No arbitrary command execution
ENABLE_BINARY_PATH_BROWSING = False   # No filesystem browsing for binaries
SUPPORT_SSH_TUNNEL = False            # No SSH tunnels from hosting server
ALLOW_SAVE_TUNNEL_PASSWORD = False    # No tunnel passwords
ENABLE_PSQL = False                   # No raw PSQL terminal (shell-level risk)

# --- Shared Storage ---
SHARED_STORAGE = []                   # No shared storage between users

# --- Login Protection ---
MAX_LOGIN_ATTEMPTS = 3
LOGIN_ATTEMPT_FIELDS = ["password"]

# --- MFA (handled by WPHPanel SSO, not pgAdmin) ---
MFA_ENABLED = False

# ══════════════════════════════════════════════════════════════════════════════
# FEATURES USERS NEED — Keep Enabled
# ══════════════════════════════════════════════════════════════════════════════
#   Query Tool, Table viewer/editor, Import/Export, Backup/Restore,
#   ERD Tool, Schema/Table/View/Function/Trigger management

# ── PostgreSQL Binary Paths (required for Backup/Restore) ────────────────────
DEFAULT_BINARY_PATHS = {
    "pg": "/usr/bin",
    "pg-18": "/usr/bin",
    "pg-17": "/usr/bin",
    "pg-16": "/usr/bin",
    "pg-15": "/usr/bin",
    "pg-14": "/usr/bin",
    "pg-13": "/usr/bin",
}

# ══════════════════════════════════════════════════════════════════════════════
# FEATURES DISABLED — Not Needed in Hosting Panel
# ══════════════════════════════════════════════════════════════════════════════
AUTO_DISCOVER_SERVERS = False      # Servers registered via SSO only
SHOW_GRAVATAR_IMAGE = False        # Privacy: no external requests
UPGRADE_CHECK_ENABLED = False      # Admin manages upgrades
CHECK_SUPPORTED_BROWSER = False    # No browser warnings
LLM_ENABLED = False                # No AI features in hosting

# ══════════════════════════════════════════════════════════════════════════════
# SESSION & PERFORMANCE
# ══════════════════════════════════════════════════════════════════════════════
MAX_SESSION_IDLE_TIME = 60         # 60-minute idle timeout
SESSION_EXPIRATION_TIME = 1        # Sessions expire after 1 day
CHECK_SESSION_FILES_INTERVAL = 12  # Clean stale sessions every 12 hours
THREADED_MODE = True               # Handle multiple requests concurrently

# ══════════════════════════════════════════════════════════════════════════════
# BRANDING & LOGGING
# ══════════════════════════════════════════════════════════════════════════════
LOGIN_BANNER = "WPHPanel — Database Management"
LOG_FILE = "/var/log/pgadmin4/pgadmin4.log"
LOG_ROTATION_SIZE = 10             # 10MB per log file
LOG_ROTATION_AGE = 1440            # Rotate daily
LOG_ROTATION_MAX_LOG_FILES = 30    # Keep 30 days
`, pgadminConfigDir)
		os.WriteFile(configLocalPath, []byte(configLocal), 0644)
	}

	os.MkdirAll("/var/log/pgadmin4", 0755)
	run("chown", "-R", "www-data:www-data", "/var/log/pgadmin4")
	run("chown", "-R", "www-data:www-data", pgadminConfigDir)

	// Setup pgAdmin admin user using the main admin password for SSO compatibility
	setupScriptBytes, _ := exec.Command("bash", "-c", "find /usr -name 'setup-web.sh' -path '*/pgadmin4/*' 2>/dev/null | head -1").Output()
	setupScript := strings.TrimSpace(string(setupScriptBytes))
	if setupScript != "" {
		cmd := exec.Command("bash", setupScript, "--yes")
		cmd.Env = append(os.Environ(),
			"PGADMIN_SETUP_EMAIL="+cfg.AdminEmail,
			"PGADMIN_SETUP_PASSWORD="+cfg.AdminPassword,
		)
		cmd.Run()
	} else {
		setupPyBytes, _ := exec.Command("bash", "-c", "find /usr -name 'setup.py' -path '*/pgadmin4/*' 2>/dev/null | head -1").Output()
		setupPy := strings.TrimSpace(string(setupPyBytes))
		if setupPy != "" {
			cmd := exec.Command("python3", setupPy)
			cmd.Env = append(os.Environ(),
				"PGADMIN_SETUP_EMAIL="+cfg.AdminEmail,
				"PGADMIN_SETUP_PASSWORD="+cfg.AdminPassword,
			)
			cmd.Run()
		}
	}

	// Write Systemd Service
	systemdService := fmt.Sprintf(`[Unit]
Description=pgAdmin 4 Web Interface
After=network.target postgresql.service

[Service]
Type=simple
User=www-data
Group=www-data
Environment=PGADMIN_CONFIG_DIR=%s
ExecStart=/usr/pgadmin4/venv/bin/python3 /usr/pgadmin4/web/pgAdmin4.py
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, pgadminConfigDir)

	// Check if virtualenv path exists, otherwise adjust ExecStart for pip fallback
	if _, errStat := os.Stat("/usr/pgadmin4/venv/bin/python3"); os.IsNotExist(errStat) {
		if _, errVal := os.Stat("/opt/pgadmin4-venv/bin/python3"); errVal == nil {
			var pgadminApp string
			pgadminAppBytes, err := exec.Command("/opt/pgadmin4-venv/bin/python3", "-c", "import os, pgadmin4; print(os.path.join(os.path.dirname(pgadmin4.__file__), 'pgAdmin4.py'))").Output()
			if err == nil && len(pgadminAppBytes) > 0 {
				pgadminApp = strings.TrimSpace(string(pgadminAppBytes))
			}
			if pgadminApp == "" {
				pgadminApp = "/opt/pgadmin4-venv/lib/python3.12/site-packages/pgadmin4/pgAdmin4.py" // fallback
			}

			systemdService = fmt.Sprintf(`[Unit]
Description=pgAdmin 4 Web Interface
After=network.target postgresql.service

[Service]
Type=simple
User=www-data
Group=www-data
Environment=PGADMIN_CONFIG_DIR=%s
ExecStart=/opt/pgadmin4-venv/bin/python3 %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, pgadminConfigDir, pgadminApp)
		}
	}

	os.WriteFile("/etc/systemd/system/pgadmin4.service", []byte(systemdService), 0644)
	run("systemctl", "daemon-reload")
	run("systemctl", "enable", "pgadmin4")
	run("systemctl", "start", "pgadmin4")

	// ── Patch webserver.py for deterministic session encryption key ──────────
	// pgAdmin's webserver auth generates a random pass_enc_key on each login,
	// which means saved database passwords become undecryptable when the user
	// logs in again (different random key). We patch it to use a deterministic
	// SHA-256 hash of the username so the key is consistent across sessions.
	// This is CRITICAL for SSO to work — without it, users see
	// "Failed to decrypt password" errors every login.
	webserverPy := filepath.Join(filepath.Dir(pythonPath), "pgadmin", "authenticate", "webserver.py")
	patchPgAdminWebserverPy(webserverPy)
}

// patchPgAdminWebserverPy replaces pgAdmin's random session encryption key
// with a deterministic SHA-256-based key derived from the SSO username.
// This ensures saved database passwords remain decryptable across logins.
//
// The original code (pgAdmin 9.x) looks like:
//
//	session['pass_enc_key'] = ''.join(
//	    (secrets.choice(string.ascii_lowercase) for _ in range(10)))
//
// We replace these 2 lines with a single deterministic line:
//
//	import hashlib
//	session['pass_enc_key'] = hashlib.sha256(username.encode('utf-8')).hexdigest()[:32]
func patchPgAdminWebserverPy(webserverPy string) {
	content, err := os.ReadFile(webserverPy)
	if err != nil {
		fmt.Printf("WARNING: could not read webserver.py for patching: %v\n", err)
		return
	}

	original := string(content)

	// Already patched?
	if strings.Contains(original, "hashlib.sha256") {
		fmt.Println("pgAdmin webserver.py already patched (deterministic key)")
		return
	}

	// Must contain pass_enc_key to be a valid target
	if !strings.Contains(original, "pass_enc_key") {
		fmt.Println("WARNING: webserver.py does not contain pass_enc_key — cannot patch")
		return
	}

	// Strategy: Find and replace the multi-line block that sets pass_enc_key.
	// The original code spans 2 lines:
	//   session['pass_enc_key'] = ''.join(
	//       (secrets.choice(string.ascii_lowercase) for _ in range(10)))
	// We need to remove both lines and insert our replacement.
	lines := strings.Split(original, "\n")
	var result []string
	skipNext := false
	patched := false

	for i, line := range lines {
		if skipNext {
			skipNext = false
			continue
		}

		// Look for the line containing pass_enc_key assignment
		if strings.Contains(line, "pass_enc_key") && strings.Contains(line, "=") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			// Get indentation from this line
			indent := ""
			for _, ch := range line {
				if ch == ' ' || ch == '\t' {
					indent += string(ch)
				} else {
					break
				}
			}

			// Check if the statement continues on the next line
			// (multi-line: ends with opening paren, or next line starts with continuation)
			if i+1 < len(lines) {
				nextTrimmed := strings.TrimSpace(lines[i+1])
				if strings.HasPrefix(nextTrimmed, "(") || strings.Contains(nextTrimmed, "secrets") || strings.Contains(nextTrimmed, "choice") {
					skipNext = true // Skip the continuation line
				}
			}

			// Insert our deterministic replacement
			result = append(result, indent+"import hashlib")
			result = append(result, indent+"session['pass_enc_key'] = hashlib.sha256(username.encode('utf-8')).hexdigest()[:32]")
			patched = true
			continue
		}

		result = append(result, line)
	}

	if !patched {
		fmt.Println("WARNING: could not find pass_enc_key assignment pattern in webserver.py")
		return
	}

	newContent := strings.Join(result, "\n")
	if err := os.WriteFile(webserverPy, []byte(newContent), 0644); err != nil {
		fmt.Printf("WARNING: failed to write patched webserver.py: %v\n", err)
		return
	}
	fmt.Println("pgAdmin webserver.py patched successfully (deterministic pass_enc_key)")
}

func installFail2ban() {
	aptWait()
	run("apt-get", "install", "-y", "-qq", "fail2ban")

	jailLocal := `[DEFAULT]
bantime = 3600
findtime = 600
maxretry = 5
ignoreip = 127.0.0.1/8 ::1
backend = systemd

[sshd]
enabled = true
port = ssh
filter = sshd
logpath = /var/log/auth.log
maxretry = 5
bantime = 3600
backend = auto

[nginx-http-auth]
enabled = true
port = http,https
filter = nginx-http-auth
logpath = /var/log/nginx/error.log
maxretry = 5
bantime = 3600
backend = auto

[nginx-limit-req]
enabled = true
port = http,https
filter = nginx-limit-req
logpath = /var/log/nginx/error.log
maxretry = 10
bantime = 600
backend = auto
`
	os.WriteFile("/etc/fail2ban/jail.local", []byte(jailLocal), 0644)
	run("systemctl", "enable", "fail2ban")
	run("systemctl", "restart", "fail2ban")
}

// verifyFail2ban ensures fail2ban is actually running after install.
// On some Ubuntu 24.04 systems, fail2ban fails to start due to missing
// log files or backend issues. This re-attempts with a workaround.
func verifyFail2ban() {
	out := shellOutput("systemctl is-active fail2ban 2>/dev/null")
	if strings.Contains(out, "active") {
		return
	}
	// Ensure required log files exist (fail2ban refuses to start without them)
	run("bash", "-c", "touch /var/log/auth.log /var/log/nginx/error.log 2>/dev/null || true")
	run("systemctl", "restart", "fail2ban")
}

