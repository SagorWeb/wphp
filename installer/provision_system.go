package main

import (
	"fmt"
	"os"
	"strings"
)

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

func writeErrorPages() {
	pages := map[string]string{
		"default.html":      errorPageHTML("No Website Configured", "This domain is not currently associated with any website on this server.", "#8b95b0", `<rect x="2" y="2" width="20" height="8" rx="2"/><circle cx="7" cy="6" r="1"/><rect x="2" y="14" width="20" height="8" rx="2"/><circle cx="7" cy="18" r="1"/>`),
		"wphpanel_502.html": errorPageHTML("Bad Gateway", "The upstream application returned an invalid response. Please try again.", "#6366f1", `<path d="M10.29 3.86L1.82 18a2 2 0 001.71 3h16.94a2 2 0 001.71-3L13.71 3.86a2 2 0 00-3.42 0z"/><path d="M12 9v4M12 17h.01"/>`),
		"wphpanel_503.html": errorPageHTML("Service Unavailable", "The server is temporarily unable to handle your request.", "#f59e0b", `<circle cx="12" cy="12" r="10"/><path d="M12 6v6l4 2"/>`),
		"suspended.html":    errorPageHTML("Account Suspended", "This website has been suspended. Contact your hosting provider.", "#ef4444", `<circle cx="12" cy="12" r="10"/><path d="M15 9l-6 6M9 9l6 6"/>`),
		"offline.html":      errorPageHTML("Site Temporarily Offline", "This website is currently offline for maintenance.", "#f59e0b", `<circle cx="12" cy="12" r="10"/><path d="M12 8v4M12 16h.01"/>`),
	}
	for name, html := range pages {
		os.WriteFile("/opt/wphpanel/error-pages/"+name, []byte(html), 0644)
	}
}

func errorPageHTML(title, msg, color, svgPath string) string {
	return fmt.Sprintf(`<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>%s</title><style>*{margin:0;padding:0;box-sizing:border-box}body{min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f1629;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:#fff}.c{text-align:center;max-width:520px;padding:40px 24px}.icon{width:80px;height:80px;margin:0 auto 32px;opacity:.3}.icon svg{width:100%%;height:100%%;stroke:%s;stroke-width:1.5;fill:none}h1{font-size:32px;font-weight:700;margin-bottom:12px;color:#e2e8f0}p{font-size:15px;color:#64748b;line-height:1.6}.footer{position:fixed;bottom:24px;left:0;right:0;text-align:center;font-size:11px;color:#334155}.footer a{color:#475569;text-decoration:none}</style></head><body><div class="c"><div class="icon"><svg viewBox="0 0 24 24">%s</svg></div><h1>%s</h1><p>%s</p></div><div class="footer">Powered by <a href="#">WPHPanel</a></div></body></html>`, title, color, svgPath, title, msg)
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
