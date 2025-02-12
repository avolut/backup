package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/avolut/backup/internal/backup"
	"github.com/avolut/backup/internal/config"
	"github.com/avolut/backup/internal/repository"
	"github.com/avolut/backup/internal/sshd"
	"github.com/avolut/backup/internal/utils"
	"github.com/robfig/cron/v3"
)

func getHostname() string {
	if hostname := os.Getenv("HOSTNAME"); hostname != "" {
		return hostname
	}
	// Fallback to os.Hostname()
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown-host"
	}
	return hostname
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
	defer utils.Unlock()

	// Create a new context that can be cancelled
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	config, err := config.LoadConfig("backup.yaml")
	if err != nil {
		log.Printf("Error loading config: %v", err)
		return
	}
	log.Printf("Starting backup for %s", config.Name)

	// Initialize file backup repository
	log.Println("Connecting to file repository...")
	fileRepo, err := repository.ConnectToRepository(ctx, config, repository.ConfigFile, "files")
	if err != nil {
		log.Printf("Error connecting to file repository: %v", err)
		return
	}
	defer fileRepo.Close(ctx)
	log.Println("Successfully connected to file repository")

	// Initialize database backup repository
	log.Println("Connecting to database repository...")
	dbRepo, err := repository.ConnectToRepository(ctx, config, repository.ConfigDB, "dbs")
	if err != nil {
		log.Printf("Error connecting to database repository: %v", err)
		return
	}
	defer dbRepo.Close(ctx)
	log.Println("Successfully connected to database repository")

	// Backup directories using file repository
	for _, dir := range config.Directories {
		log.Printf("Starting backup of directory: %s", dir)
		if err := backup.BackupDir(ctx, fileRepo, dir); err != nil {
			log.Printf("Error backing up directory %s: %v", dir, err)
			continue
		}
		log.Printf("Successfully backed up directory: %s", dir)
	}

	// Backup databases using database repository
	for _, db := range config.Databases {
		log.Printf("Starting backup of database: %s", db.Name)
		if err := backup.BackupDatabase(ctx, dbRepo, db); err != nil {
			log.Printf("Error backing up database %s: %v", db.Name, err)
			continue
		}
		log.Printf("Successfully backed up database: %s", db.Name)
	}

	log.Printf("Backup completed for %s", config.Name)
}

func checkPgDumpAvailability() error {
	_, err := exec.LookPath("pg_dump")
	if err != nil {
		return fmt.Errorf("pg_dump command not found in PATH. Please install PostgreSQL client tools")
	}
	return nil
}

func main() {
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
		case "--connect":
			if len(os.Args) != 3 {
				log.Fatal("Usage: --connect [hostname]")
			}
			if err := utils.ConnectToHost(context.Background(), os.Args[2]); err != nil {
				log.Fatal(err)
			}
			return
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

		// Initialize and start SSH server
		sshServer, err := sshd.NewServer(41334, ".avolut/ssh", true, config.Name)
		if err != nil {
			log.Printf("Warning: failed to initialize SSH server: %v", err)
		} else {
			if err := sshServer.Start(); err != nil {
				log.Printf("Warning: failed to start SSH server: %v", err)
			} else {
				log.Println("SSH server started successfully on port 41334")
			}
		}

		// Collect and store IP information
		if err := utils.CollectAndStoreIPs(ctx, config.Name); err != nil {
			log.Printf("Warning: failed to collect and store IP information: %v", err)
		} else {
			log.Println("IP information collected and stored successfully")
		}

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
