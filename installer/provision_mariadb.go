package main

import (
	"fmt"
	"os"
)

func installMariaDB(cfg InstallConfig, creds Credentials, codename string) {
	// Prefer MariaDB 11.4 official repo; if this Ubuntu codename is not published yet,
	// disable the broken list and fall back to distro mariadb-server (e.g. 11.8 on 26.04).
	run("bash", "-c", "curl -fsSL https://mariadb.org/mariadb_release_signing_key.pgp | gpg --batch --yes --dearmor -o /usr/share/keyrings/mariadb-keyring.gpg")
	os.WriteFile("/etc/apt/sources.list.d/mariadb.list",
		[]byte(fmt.Sprintf("deb [signed-by=/usr/share/keyrings/mariadb-keyring.gpg] https://dlm.mariadb.com/repo/mariadb-server/11.4/repo/ubuntu %s main", codename)), 0644)
	aptWait()
	if err := run("apt-get", "update", "-y", "-qq"); err != nil {
		fmt.Printf("WARNING: MariaDB official repo unavailable for %s — falling back to Ubuntu packages\n", codename)
		os.Remove("/etc/apt/sources.list.d/mariadb.list")
		run("bash", "-c", "apt-get update -y -qq 2>/dev/null || true")
	}
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
	mariaConf := fmt.Sprintf(`[mysqld]
# Memory / connections (sized from host RAM)
innodb_buffer_pool_size = %dM
max_connections = %d
innodb_log_file_size = 256M
innodb_flush_log_at_trx_commit = 2
innodb_flush_method = O_DIRECT
key_buffer_size = 32M
tmp_table_size = 64M
max_heap_table_size = 64M
thread_cache_size = 16
query_cache_type = 0

# WPHPanel: Incus containers connect to host MariaDB via bridge (10.100.0.1).
# Listen on all interfaces; UFW must NOT expose 3306 publicly (only bridge/local).
bind-address = 0.0.0.0
skip_name_resolve = 1
require_secure_transport = OFF
`, bufferMB, maxConn)
	os.WriteFile("/etc/mysql/mariadb.conf.d/99-wphpanel.cnf", []byte(mariaConf), 0644)
	run("systemctl", "enable", "mariadb")
	run("systemctl", "restart", "mariadb")

	// Host PHP-FPM for phpMyAdmin — prefer 8.4 (ondrej), else newest distro phpX.Y-fpm.
	aptWait()
	phpVer := detectHostPHPVersion()
	fmt.Printf("Installing host PHP-FPM %s for phpMyAdmin\n", phpVer)
	run("apt-get", "install", "-y", "-qq",
		"php"+phpVer+"-fpm", "php"+phpVer+"-mbstring", "php"+phpVer+"-zip", "php"+phpVer+"-gd",
		"php"+phpVer+"-curl", "php"+phpVer+"-mysql", "php"+phpVer+"-xml", "php"+phpVer+"-intl", "net-tools")

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
	os.WriteFile("/etc/php/"+phpVer+"/fpm/conf.d/99-wphpanel.ini",
		[]byte("[Session]\nsession.gc_maxlifetime = 3600\n"), 0644)

	// Host PHP-FPM auto-restart override — default misses OOM kills (SIGKILL → exit 0).
	os.MkdirAll("/etc/systemd/system/php"+phpVer+"-fpm.service.d", 0755)
	os.WriteFile("/etc/systemd/system/php"+phpVer+"-fpm.service.d/restart.conf",
		[]byte("[Unit]\nStartLimitBurst=10\nStartLimitIntervalSec=120\n\n[Service]\nRestart=always\nRestartSec=3\n"), 0644)
	run("systemctl", "daemon-reload")

	run("systemctl", "enable", "php"+phpVer+"-fpm")
	run("systemctl", "start", "php"+phpVer+"-fpm")
}

func calcMariaDBTuning(ramMB int) (bufferMB, maxConn int) {
	bufferMB = ramMB * 30 / 100
	if bufferMB < 512 {
		bufferMB = 512
	}
	maxConn = 100
	switch {
	case ramMB <= 2048:
		if bufferMB > 512 {
			bufferMB = 512
		}
		maxConn = 100
	case ramMB <= 4096:
		if bufferMB > 1536 {
			bufferMB = 1536
		}
		maxConn = 200
	case ramMB <= 8192:
		if bufferMB > 3072 {
			bufferMB = 3072
		}
		maxConn = 300
	case ramMB <= 16384:
		if bufferMB > 6144 {
			bufferMB = 6144
		}
		maxConn = 500
	default:
		if bufferMB > 20480 {
			bufferMB = 20480
		}
		maxConn = 1000
	}
	return
}
