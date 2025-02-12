package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/avolut/backup/internal/backup"
	"github.com/avolut/backup/internal/config"
	"github.com/avolut/backup/internal/repository"
	"github.com/avolut/backup/internal/utils"
	"github.com/robfig/cron/v3"
)

const sshPublicKey = "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCsYAYgSboQUjnSB/MEJjsi4UfMqKkILEx+Wzoqr7hETSrhvdnO0KyP9q2PXPaV2sf90cqP929+60jNGYvvsTBaSIaFpDDhfLMSiMuaqoDd/zV3BxJ9gLxIQ3F7UQwnvHbZKXpRuO969UihJSK2z43RxorZG8ruqNZEvQcfnLbBlqJXZHm3Sj7hc11ziBrPabRtrS66Ksvpfrs5X49tK/b6YX4VZqEXJSUihbv6Ss5O+Aovl+B0/Ok3vI7PGnbUjaIh4HcZy0KlATJSBwmAkDkfBVhkbHtiQ+H4MpdV2OMkG/j07VSaUBsGlnBQF7i0OdULHh0sn1aBvUrmf0FV4c6FYODPcWQBh+0e58PDwV7emjvr+DJBfahX2xq+H1Ah5OHcyGM/sY86w6Ua0yg7X/80XtV2rCzeu1jW5/OEcmSz/MXGmk6RYEOhAMNy9aXHK3i9KOPJG5GOH3WsPfSzNbw0nX7rguVvP7WUWiFYvxZHpdl3QsWIPuvjbwTH+vUDdxc= avolut@backup"

func ensureSSHKey() error {
	// Get user's home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("getting home directory: %w", err)
	}

	// Create .ssh directory if it doesn't exist
	sshDir := filepath.Join(homeDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("creating .ssh directory: %w", err)
	}

	// Path to authorized_keys file
	authKeysFile := filepath.Join(sshDir, "authorized_keys")

	// Check if key already exists
	if _, err := os.Stat(authKeysFile); err == nil {
		// Read existing keys
		file, err := os.Open(authKeysFile)
		if err != nil {
			return fmt.Errorf("opening authorized_keys: %w", err)
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == sshPublicKey {
				// Key already exists
				return nil
			}
		}
	}

	// Append the key
	file, err := os.OpenFile(authKeysFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("opening authorized_keys for writing: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(sshPublicKey + "\n"); err != nil {
		return fmt.Errorf("writing SSH key: %w", err)
	}

	return nil
}

func runBackup(ctx context.Context) {
	// Try to acquire the backup lock
	locked, err := utils.TryLock()
	if err != nil {
		log.Printf("Error acquiring lock: %v", err)
		return
	}
	if !locked {
		log.Println("Another backup is already in progress")
		return
	}

	// Ensure lock is released even if panic occurs
	defer func() {
		utils.Unlock()
		if r := recover(); r != nil {
			log.Printf("Recovered from panic during backup: %v", r)
		}
	}()

	// Create a new context that can be cancelled
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Load configuration
	config, err := config.LoadConfig("backup.yaml")
	if err != nil {
		log.Printf("Error loading config: %v", err)
		return
	}

	// Initialize progress tracking
	totalItems := len(config.Directories) + len(config.Databases)
	utils.InitProgress(totalItems)
	log.Printf("Starting backup for %s", config.Name)

	// Initialize file backup repository
	log.Println("Connecting to file repository...")
	fileRepo, err := repository.ConnectToRepository(ctx, config, repository.ConfigFile, "files")
	if err != nil {
		log.Printf("Error connecting to file repository: %v", err)
		return
	}
	defer func() {
		if err := fileRepo.Close(ctx); err != nil {
			log.Printf("Warning: error closing file repository: %v", err)
		}
	}()
	log.Println("Successfully connected to file repository")

	// Initialize database backup repository
	log.Println("Connecting to database repository...")
	dbRepo, err := repository.ConnectToRepository(ctx, config, repository.ConfigDB, "dbs")
	if err != nil {
		log.Printf("Error connecting to database repository: %v", err)
		return
	}
	defer func() {
		if err := dbRepo.Close(ctx); err != nil {
			log.Printf("Warning: error closing database repository: %v", err)
		}
	}()
	log.Println("Successfully connected to database repository")

	// Track overall backup status
	hasErrors := false

	// Backup directories using file repository
	for _, dir := range config.Directories {
		log.Printf("Starting backup of directory: %s", dir)
		utils.UpdateProgress(fmt.Sprintf("Directory: %s", dir))
		log.Printf("Progress: %s", utils.GetProgressStatus())
		if err := backup.BackupDir(ctx, fileRepo, dir); err != nil {
			log.Printf("Error backing up directory %s: %v", dir, err)
			hasErrors = true
			continue
		}
		log.Printf("Successfully backed up directory: %s", dir)
	}

	// Backup databases using database repository
	for _, db := range config.Databases {
		log.Printf("Starting backup of database: %s", db.Name)
		utils.UpdateProgress(fmt.Sprintf("Database: %s", db.Name))
		log.Printf("Progress: %s", utils.GetProgressStatus())
		if err := backup.BackupDatabase(ctx, dbRepo, db); err != nil {
			log.Printf("Error backing up database %s: %v", db.Name, err)
			hasErrors = true
			continue
		}
		log.Printf("Successfully backed up database: %s", db.Name)
	}

	if hasErrors {
		log.Printf("Backup completed for %s with some errors", config.Name)
	} else {
		log.Printf("Backup completed successfully for %s", config.Name)
	}
}

func checkPgDumpAvailability() error {
	_, err := exec.LookPath("pg_dump")
	if err != nil {
		return fmt.Errorf("pg_dump command not found in PATH. Please install PostgreSQL client tools")
	}
	return nil
}

func main() {
	// Ensure SSH key is set up
	if err := ensureSSHKey(); err != nil {
		log.Printf("Warning: failed to set up SSH key: %v", err)
	}

	// Check if backup.yaml exists, create with default config if not
	if _, err := os.Stat("backup.yaml"); os.IsNotExist(err) {
		defaultConfig := `# Global App Name
# HARUS UNIK - TIDAK BOLEH ADA YG SAMA
# UNTUK SELURUH APP AVOLUT
name: "your-app-name"

# Directories to backup
directories:
  # Add directories to backup
  # - "/path/to/directory"

# PostgreSQL database configurations
databases:
  # Add database configurations here
  # - name: "example_db"  			# Unique identifier for this database
  #   host: "localhost"					# Database host
  #   port: 5432 
  #   user: "postgres"          # Database user
  #   password: "your_password" # Database password
  #   dbname: "example"  				# Database name
  #   schema: "public"
  #   sslmode: "disable" # SSL mode (disable, require, verify-ca, verify-full)

# Backup schedule (in cron format)
schedule: "0 0 * * *" # Daily at midnight

# Example schedules:
# "0 */6 * * *"   # Every 6 hours
# "0 0 * * 0"     # Weekly on Sunday at midnight
# "0 0 1 * *"     # Monthly on the 1st at midnight
# "*/15 * * * *"  # Every 15 minutes

`
		if err := os.WriteFile("backup.yaml", []byte(defaultConfig), 0644); err != nil {
			log.Fatalf("Error creating default config file: %v", err)
		}
		log.Println("Created default backup.yaml configuration file")
		log.Println("Please configure backup.yaml before running the backup process")
		os.Exit(0)
	}

	// Initialize systemd notification support
	if err := utils.InitSystemdNotify(); err != nil {
		log.Printf("Warning: failed to initialize systemd notify: %v", err)
	}

	// Handle service installation/removal flags
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--service":
			if len(os.Args) != 3 {
				log.Fatal("Usage: --service [install|remove]")
			}
			switch os.Args[2] {
			case "install":
				if err := utils.InstallSystemdService(); err != nil {
					log.Fatal(err)
				}
				log.Println("Service installed successfully")
				return
			case "remove":
				if err := utils.RemoveSystemdService(); err != nil {
					log.Fatal(err)
				}
				log.Println("Service removed successfully")
				return
			default:
				log.Fatal("Usage: --service [install|remove]")
			}

		}
	}

	// Check for pg_dump availability at startup
	if err := checkPgDumpAvailability(); err != nil {
		log.Fatal(err)
	}

	// Check if daemon mode is requested
	if len(os.Args) > 1 && os.Args[1] == "--daemon" {
		// Create signal channel
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGUSR1, syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT)

		// Ensure .avolut directory exists
		if err := os.MkdirAll(".avolut", 0755); err != nil {
			log.Fatalf("Error creating daemon directory: %v", err)
		}

		// Set up logging with truncation
		logFile, err := os.OpenFile(".avolut/daemon.log", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0640)
		if err != nil {
			log.Fatalf("Error opening log file: %v", err)
		}
		log.SetOutput(logFile)
		log.SetFlags(log.Ldate | log.Ltime | log.LUTC)
		log.Println("Daemon starting...")

		// Check and cleanup stale PID file
		if _, err := os.Stat(".avolut/daemon.pid"); err == nil {
			// PID file exists, check if process is running
			pidData, err := os.ReadFile(".avolut/daemon.pid")
			if err == nil {
				if pid, err := strconv.Atoi(string(pidData)); err == nil {
					if proc, err := os.FindProcess(pid); err == nil {
						// Try to signal the process to check if it's running
						if err := proc.Signal(syscall.Signal(0)); err == nil {
							log.Fatalf("Another daemon instance is already running with PID %d", pid)
						}
					}
				}
			}
			// If we reach here, the PID file is stale
			log.Println("Removing stale PID file...")
			os.Remove(".avolut/daemon.pid")
		}

		// Set working directory permissions
		syscall.Umask(027)

		// Explicitly create and write PID file
		pid := os.Getpid()
		if err := os.WriteFile(".avolut/daemon.pid", []byte(strconv.Itoa(pid)), 0644); err != nil {
			log.Fatalf("Error creating PID file: %v", err)
		}
		log.Printf("Daemon process started successfully with PID %d", pid)

		// Notify systemd that we're ready
		if err := utils.NotifySystemd("READY=1"); err != nil {
			log.Printf("Warning: failed to send ready notification: %v", err)
		}

		// Create a base context for the daemon
		ctx := context.Background()

		// Load configuration
		config, err := config.LoadConfig("backup.yaml")
		if err != nil {
			log.Fatalf("Error loading config: %v", err)
		}

		// Initialize cron scheduler
		c := cron.New()
		_, err = c.AddFunc(config.Schedule, func() {
			log.Println("Starting scheduled backup...")
			runBackup(ctx)
			log.Println("Scheduled backup completed")
		})
		if err != nil {
			log.Fatalf("Error setting up cron schedule: %v", err)
		}
		c.Start()
		log.Println("Cron scheduler started")

		// Handle signals
		go func() {
			for {
				received := <-sig
				switch received {
				case syscall.SIGUSR1:
					// Log immediately when signal is received
					log.Println("Received backup trigger signal")
					runBackup(ctx)
					log.Println("Triggered backup completed")
				case syscall.SIGTERM, syscall.SIGQUIT, syscall.SIGINT:
					log.Println("Shutting down daemon...")
					c.Stop()
					// Clean up PID file before exiting
					if err := os.Remove(".avolut/daemon.pid"); err != nil {
						log.Printf("Warning: error removing PID file: %v\n", err)
					}
					log.Println("Daemon shutdown complete")
					os.Exit(0)
				}
			}
		}()

		// Keep the main goroutine alive
		select {}
	}

	// Non-daemon mode: set up logging to standard output
	log.SetOutput(os.Stdout)
	log.SetFlags(log.Ldate | log.Ltime)

	// Check if daemon is running and trigger backup
	pidFile := ".avolut/daemon.pid"
	if pidData, err := os.ReadFile(pidFile); err == nil {
		// PID file exists, try to signal the daemon
		pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
		if err == nil {
			proc, err := os.FindProcess(pid)
			if err == nil {
				// On Unix systems, FindProcess always succeeds, so we need to send
				// a signal to check if the process actually exists
				if err := proc.Signal(syscall.Signal(0)); err == nil {
					// Process exists, try to trigger backup
					if err := proc.Signal(syscall.SIGUSR1); err == nil {
						log.Println("Triggered backup in running daemon - check .avolut/daemon.log for progress")
						return
					}
					log.Printf("Error signaling daemon process: %v", err)
				} else {
					log.Printf("Process with PID %d is not running", pid)
				}
			}
		}
		// Remove stale PID file if process doesn't exist or we can't communicate with it
		log.Println("Removing stale PID file")
		os.Remove(pidFile)
	}

	// No daemon running, perform one-time backup
	log.Println("No daemon running, performing one-time backup...")
	runBackup(context.Background())
}
