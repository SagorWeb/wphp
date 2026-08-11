package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

//go:embed embedded/*
var embeddedFS embed.FS

// InstallConfig holds user-provided setup info.
type InstallConfig struct {
	Hostname      string `json:"hostname"`
	AdminEmail    string `json:"admin_email"`
	AdminPassword string `json:"admin_password"`
}

// StepUpdate is sent via SSE to the browser.
type StepUpdate struct {
	Step    int    `json:"step"`
	Total   int    `json:"total"`
	Title   string `json:"title"`
	Status  string `json:"status"` // running, done, error
	Log     string `json:"log"`
	Percent int    `json:"percent"`
}

// Generated credentials after install.
type Credentials struct {
	MariaDBRootPass string `json:"mariadb_root_pass"`
	PostgresPass    string `json:"postgres_pass"`
	ValkeyPass      string `json:"valkey_pass"`
	JWTSecret       string `json:"jwt_secret"`
}

// InstallState holds the persistence status of installation
type InstallState struct {
	Step        int         `json:"step"`
	Total       int         `json:"total"`
	Title       string      `json:"title"`
	Status      string      `json:"status"` // running, done, error, complete
	Log         string      `json:"log"`
	Percent     int         `json:"percent"`
	Hostname    string      `json:"hostname"`
	AdminEmail  string      `json:"admin_email"`
	Credentials Credentials `json:"credentials"`
}

var (
	installing   bool
	installMu    sync.Mutex
	progressChan chan StepUpdate
	selfBinary   string
)

func main() {
	selfBinary, _ = os.Executable()
	port := "8090"
	if p := os.Getenv("PORT"); p != "" {
		port = p
	}

	http.HandleFunc("/", handleUI)
	http.HandleFunc("/api/sysinfo", handleSysInfo)
	http.HandleFunc("/api/install", handleInstall)
	http.HandleFunc("/api/status", handleStatus)
	http.HandleFunc("/api/delete", handleDelete)

	ip := detectServerIP()
	fmt.Println("╔══════════════════════════════════════════════════╗")
	fmt.Println("║       WPHPanel — Server Installer          ║")
	fmt.Println("╠══════════════════════════════════════════════════╣")
	fmt.Printf("║  Open: http://%s:%s\n", ip, port)
	fmt.Println("╚══════════════════════════════════════════════════╝")

	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// handleUI serves the single-page installer wizard.
func handleUI(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, installerHTML)
}

// handleSysInfo returns server info for the wizard.
func handleSysInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]interface{}{
		"ip":       detectServerIP(),
		"os":       shellOutput("lsb_release -ds 2>/dev/null || cat /etc/os-release | grep PRETTY_NAME | cut -d'\"' -f2"),
		"cpu":      shellOutput("nproc"),
		"ram_mb":   shellOutput("awk '/^MemTotal:/{print int($2/1024)}' /proc/meminfo"),
		"disk_gb":  shellOutput("df -B1G --output=size / | tail -1 | tr -d ' '"),
		"hostname": shellOutput("hostname"),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

// handleInstall starts the provisioning process in the background.
func handleInstall(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}

	installMu.Lock()
	defer installMu.Unlock()

	// Check if state file already exists
	if _, err := os.Stat("/opt/wphpanel/.install_state.json"); err == nil {
		http.Error(w, "Installation already in progress or completed", 409)
		return
	}

	if installing {
		http.Error(w, "Installation already in progress", 409)
		return
	}
	installing = true

	var cfg InstallConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "Invalid input", 400)
		return
	}
	cfg.Hostname = strings.TrimSpace(cfg.Hostname)
	cfg.AdminEmail = strings.TrimSpace(cfg.AdminEmail)

	// Generate all credentials
	creds := Credentials{
		MariaDBRootPass: randHex(16),
		PostgresPass:    randHex(16),
		ValkeyPass:      randHex(16),
		JWTSecret:       randHex(32),
	}

	progressChan = make(chan StepUpdate, 100)

	// Run provisioning in background
	go func() {
		// Initialize temporary state
		initialState := InstallState{
			Step:        0,
			Total:       totalSteps,
			Title:       "Starting Up",
			Status:      "running",
			Log:         "Starting installation...",
			Percent:     0,
			Hostname:    cfg.Hostname,
			AdminEmail:  cfg.AdminEmail,
			Credentials: creds,
		}
		os.MkdirAll("/opt/wphpanel", 0755)
		stateData, _ := json.Marshal(initialState)
		os.WriteFile("/opt/wphpanel/.install_state.json", stateData, 0600)

		runProvision(cfg, creds, progressChan)
		
		// Consume any remaining channel updates safely
		for range progressChan {}
		
		installMu.Lock()
		installing = false
		installMu.Unlock()
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"started": true})
}

// handleDelete removes the installer binary from the server.
func handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if selfBinary != "" {
		// Securely clean up any installation logs, SQL templates, and close firewall port 8090
		os.Remove(selfBinary)
		os.Remove("/root/installer.log")
		os.Remove("/tmp/wphpanel-preseed.yaml")
		os.Remove("/tmp/wphpanel-pg-tune.sql")
		os.Remove("/opt/wphpanel/.install_state.json")
		exec.Command("ufw", "delete", "allow", "8090/tcp").Run()

		json.NewEncoder(w).Encode(map[string]interface{}{"deleted": true, "path": selfBinary})
		// Schedule exit after response is sent
		go func() {
			time.Sleep(500 * time.Millisecond)
			os.Exit(0)
		}()
	} else {
		json.NewEncoder(w).Encode(map[string]interface{}{"deleted": false, "error": "cannot determine binary path"})
	}
}

// handleStatus reads the persistent installer state.
func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	stateBytes, err := os.ReadFile("/opt/wphpanel/.install_state.json")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"status": "idle"})
		return
	}
	w.Write(stateBytes)
}

// ──────────────────────────────────────────────
//  Helpers
// ──────────────────────────────────────────────

func detectServerIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "0.0.0.0"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}

func shellOutput(cmd string) string {
	out, _ := exec.Command("bash", "-c", cmd).Output()
	return strings.TrimSpace(string(out))
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}
