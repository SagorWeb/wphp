package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	run("bash", "-c", "touch /var/log/pgadmin4/pgadmin4.log && chown -R www-data:www-data /var/log/pgadmin4 && chmod 755 /var/log/pgadmin4 && chmod 640 /var/log/pgadmin4/pgadmin4.log")
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
	// setup scripts often recreate the log as root — restore www-data ownership
	run("bash", "-c", "chown -R www-data:www-data /var/log/pgadmin4 /var/lib/pgadmin 2>/dev/null || true")

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
