package utils

import (
	"fmt"
	"net"
	"os"
	"time"
)

var (
	systemdSocket    net.Conn
	watchdogInterval time.Duration
)

// InitSystemdNotify initializes systemd notification support
func InitSystemdNotify() error {
	// Check if running under systemd
	socketPath := os.Getenv("NOTIFY_SOCKET")
	if socketPath == "" {
		return nil // Not running under systemd
	}

	// Parse watchdog interval
	if usecStr := os.Getenv("WATCHDOG_USEC"); usecStr != "" {
		usec := 0
		if _, err := fmt.Sscanf(usecStr, "%d", &usec); err == nil {
			watchdogInterval = time.Duration(usec) * time.Microsecond
		}
	}

	// Connect to systemd notification socket
	conn, err := net.Dial("unixgram", socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to systemd socket: %w", err)
	}
	systemdSocket = conn

	return nil
}

// NotifySystemd sends a notification to systemd
func NotifySystemd(state string) error {
	if systemdSocket == nil {
		return nil // Not running under systemd
	}

	_, err := systemdSocket.Write([]byte(state))
	return err
}

// StartWatchdogUpdates starts a goroutine to send watchdog updates
func StartWatchdogUpdates() {
	if watchdogInterval == 0 {
		return // Watchdog not enabled
	}

	go func() {
		ticker := time.NewTicker(watchdogInterval / 2)
		defer ticker.Stop()

		for range ticker.C {
			if err := NotifySystemd("WATCHDOG=1"); err != nil {
				fmt.Printf("Warning: failed to send watchdog update: %v\n", err)
			}
		}
	}()
}
