package main

import (
	"fmt"
	"os"
)

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

	// Initial ACL: enable default user with the panel password (required for wphpanel-api).
	// Per-site WordPress ACL users are added later via ACL SETUSER + ACL SAVE.
	// NOTE: `user default off` + requirepass breaks panel auth (WRONGPASS / user disabled).
	os.WriteFile("/etc/valkey/users.acl",
		[]byte(fmt.Sprintf("user default on >%s ~* &* +@all\n", creds.ValkeyPass)), 0640)
	run("chown", "-R", "valkey:valkey", "/etc/valkey")

	// Bind panel loopback + Incus bridge. protected-mode must be off so containers can AUTH
	// as ACL users via 10.100.0.1. Wait for bridge address before starting the service.
	ensureIncusBridgeUp()

	ramMB := shellOutputInt("awk '/^MemTotal:/{print int($2/1024)}' /proc/meminfo")
	maxMemMB := ramMB * 10 / 100 // ~10% RAM for object cache / queues
	if maxMemMB < 128 {
		maxMemMB = 128
	}
	if maxMemMB > 2048 {
		maxMemMB = 2048
	}

	valkeyConf := fmt.Sprintf(`bind 127.0.0.1 10.100.0.1
protected-mode no
port 6379
requirepass "%s"
aclfile /etc/valkey/users.acl
maxmemory %dmb
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
`, creds.ValkeyPass, maxMemMB)
	os.WriteFile("/etc/valkey/valkey.conf", []byte(valkeyConf), 0644)

	// Create systemd service — start after Incus so 10.100.0.1 exists
	valkeyService := `[Unit]
Description=Valkey 9 In-Memory Data Store
After=network-online.target incus.service
Wants=network-online.target
StartLimitBurst=10
StartLimitIntervalSec=120

[Service]
Type=simple
User=valkey
Group=valkey
ExecStartPre=/bin/bash -c 'ip link set wphpanel-net up 2>/dev/null || true; for i in $(seq 1 30); do ip -br addr show wphpanel-net 2>/dev/null | grep -q 10.100.0.1 && exit 0; sleep 1; done; echo "wphpanel-net 10.100.0.1 not ready" >&2; exit 1'
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
