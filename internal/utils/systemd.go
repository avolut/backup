package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const serviceTemplate = `[Unit]
Description=Avolut Backup Service
After=network.target

[Service]
Type=notify
ExecStart=%s --daemon
WorkingDirectory=%s
Restart=on-failure
RestartSec=5
WatchdogSec=30
NotifyAccess=all

[Install]
WantedBy=multi-user.target
`

// IsSystemdAvailable checks if systemd is available on the system
func IsSystemdAvailable() bool {
	// Check if systemd is running by looking for systemctl
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}

	// Verify systemd is actually running
	cmd := exec.Command("ps", "-p", "1", "-o", "comm=")
	output, err := cmd.Output()
	if err != nil {
		return false
	}

	return strings.TrimSpace(string(output)) == "systemd"
}

// InstallSystemdService installs the backup service
func InstallSystemdService() error {
	if !IsSystemdAvailable() {
		return fmt.Errorf("systemd is not available on this system")
	}

	// Get the absolute path of the current executable
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}
	exePath, err = filepath.Abs(exePath)
	if err != nil {
		return fmt.Errorf("failed to get absolute executable path: %w", err)
	}

	// Get the current working directory
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get working directory: %w", err)
	}

	// Create service unit file content
	serviceContent := fmt.Sprintf(serviceTemplate, exePath, wd)

	// Write service file
	servicePath := "/etc/systemd/system/avolut-backup.service"
	if err := os.WriteFile(servicePath, []byte(serviceContent), 0644); err != nil {
		return fmt.Errorf("failed to write service file: %w", err)
	}

	// Reload systemd daemon
	cmd := exec.Command("systemctl", "daemon-reload")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	// Enable and start the service
	cmd = exec.Command("systemctl", "enable", "--now", "avolut-backup.service")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to enable and start service: %w", err)
	}

	return nil
}

// RemoveSystemdService removes the backup service
func RemoveSystemdService() error {
	if !IsSystemdAvailable() {
		return fmt.Errorf("systemd is not available on this system")
	}

	// Stop and disable the service
	cmd := exec.Command("systemctl", "disable", "--now", "avolut-backup.service")
	_ = cmd.Run() // Ignore errors as service might not be running

	// Remove service file
	servicePath := "/etc/systemd/system/avolut-backup.service"
	if err := os.Remove(servicePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove service file: %w", err)
	}

	// Reload systemd daemon
	cmd = exec.Command("systemctl", "daemon-reload")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to reload systemd: %w", err)
	}

	return nil
}
