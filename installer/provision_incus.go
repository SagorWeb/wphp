package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

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

	// Ensure daemon is up before querying/applying preseed.
	run("systemctl", "enable", "--now", "incus.socket")
	run("systemctl", "enable", "--now", "incus")
	// Wait briefly for the unix socket / API to become ready.
	for i := 0; i < 30; i++ {
		if shellOutput("incus info >/dev/null 2>&1 && echo ok") == "ok" {
			break
		}
		time.Sleep(1 * time.Second)
	}

	// Idempotency: skip preseed if default storage pool already exists.
	// IMPORTANT: do NOT use `grep -c ... || echo 0` — when count is 0, grep exits 1
	// and `|| echo 0` appends a second line ("0\n0"), which incorrectly skips preseed.
	storageCount := strings.TrimSpace(shellOutput(`incus storage list --format csv 2>/dev/null | grep -c '^default,' || true`))
	if storageCount != "" && storageCount != "0" {
		ensureIncusBridgeUp()
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
	if err := run("bash", "-c", "cat "+tmpFile+" | incus admin init --preseed"); err != nil {
		fmt.Printf("ERROR: incus admin init --preseed failed: %v\n", err)
	}
	os.Remove(tmpFile)

	ensureIncusBridgeUp()

	// Verify pool exists; fail loudly in installer log if not.
	if strings.TrimSpace(shellOutput(`incus storage list --format csv 2>/dev/null | grep -c '^default,' || true`)) == "0" {
		fmt.Println("ERROR: Incus default storage pool was not created — instance provisioning will fail")
	}
}

// ensureIncusBridgeUp brings wphpanel-net UP and waits for 10.100.0.1 (Valkey/bind + UFW).
func ensureIncusBridgeUp() {
	run("bash", "-c", "ip link set wphpanel-net up 2>/dev/null || true")
	for i := 0; i < 20; i++ {
		if strings.Contains(shellOutput("ip -br addr show wphpanel-net 2>/dev/null"), "10.100.0.1") {
			return
		}
		time.Sleep(1 * time.Second)
	}
	fmt.Println("WARNING: wphpanel-net / 10.100.0.1 not ready — Valkey bind may fail until bridge is up")
}
