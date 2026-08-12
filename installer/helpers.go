package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

func detectHostPHPVersion() string {
	if shellOutput("apt-cache show php8.4-fpm 2>/dev/null | head -1") != "" {
		return "8.4"
	}
	ver := strings.TrimSpace(shellOutput(`apt-cache search --names-only '^php[0-9]+\.[0-9]+-fpm$' 2>/dev/null | sed -E 's/^php([0-9]+\.[0-9]+)-fpm.*/\1/' | sort -V | tail -1`))
	if ver != "" {
		return ver
	}
	return "8.4"
}

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
