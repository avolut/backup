package backup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/avolut/backup/internal/config"
	"github.com/kopia/kopia/fs"
	"github.com/kopia/kopia/fs/localfs"
	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/snapshot"
	"github.com/kopia/kopia/snapshot/policy"
	"github.com/kopia/kopia/snapshot/snapshotfs"
)

func BackupDatabase(ctx context.Context, r repo.Repository, db config.Database) error {
	// Check pg_dump version
	pgDumpVersion, err := exec.Command("pg_dump", "--version").Output()
	if err != nil {
		return fmt.Errorf("getting pg_dump version: %w", err)
	}

	// Get database version
	dbVersionCmd := exec.Command("psql",
		"--host", db.Host,
		"--port", fmt.Sprintf("%d", db.Port),
		"--username", db.User,
		"--dbname", db.DBName,
		"--tuples-only",
		"--command", "SELECT version();",
	)
	dbVersionCmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", db.Password))
	dbVersion, err := dbVersionCmd.Output()
	if err != nil {
		return fmt.Errorf("getting database version: %w", err)
	}

	// Extract major version numbers for comparison
	pgDumpMajorVersion := extractMajorVersion(string(pgDumpVersion))
	dbMajorVersion := extractMajorVersion(string(dbVersion))

	// Compare versions
	if pgDumpMajorVersion < dbMajorVersion {
		return fmt.Errorf("version mismatch: pg_dump version %s is not compatible with database version %s", pgDumpMajorVersion, dbMajorVersion)
	}

	// Create a unique temporary directory for this backup
	timestamp := time.Now().Format("20060102_150405")
	tmpDir := filepath.Join(".avolut", "tmp", fmt.Sprintf("%s_%s", db.Name, timestamp))
	tmpFile := filepath.Join(tmpDir, "dump.sql")

	// Ensure the temporary directory exists
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return fmt.Errorf("creating temporary directory: %w", err)
	}

	// Prepare pg_dump command
	cmd := exec.CommandContext(ctx, "pg_dump",
		"--host", db.Host,
		"--port", fmt.Sprintf("%d", db.Port),
		"--username", db.User,
		"--dbname", db.DBName,
		"--schema", db.Schema,
		"--file", tmpFile,
	)

	// Set environment variables for authentication
	cmd.Env = append(os.Environ(), fmt.Sprintf("PGPASSWORD=%s", db.Password))

	// Execute pg_dump
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("executing pg_dump: %w\nOutput: %s", err, string(output))
	}

	// Create source info for the snapshot
	src := snapshot.SourceInfo{
		Host:     "localhost",
		UserName: os.Getenv("USER"),
		Path:     tmpDir,
	}

	// Create writer session
	writeContext, writer, err := r.NewWriter(ctx, repo.WriteSessionOptions{
		Purpose: "Backup database",
	})
	if err != nil {
		return fmt.Errorf("creating writer session: %w", err)
	}
	defer func() {
		if cerr := writer.Close(writeContext); cerr != nil {
			fmt.Printf("Warning: error closing writer: %v\n", cerr)
		}
		// Clean up temporary directory
		if err := os.RemoveAll(tmpDir); err != nil {
			fmt.Printf("Warning: error removing temporary directory: %v\n", err)
		}
	}()

	// Create manifest
	manifest := &snapshot.Manifest{
		Source:      src,
		Description: fmt.Sprintf("Backup of database %s", db.Name),
		StartTime:   fs.UTCTimestampFromTime(time.Now()),
	}

	// Create uploader
	uploader := snapshotfs.NewUploader(writer)

	// Create policy tree
	policyTree := policy.BuildTree(nil, policy.DefaultPolicy)

	// Upload the snapshot
	entry, err := localfs.Directory(tmpDir)
	if err != nil {
		return fmt.Errorf("creating directory entry: %w", err)
	}
	uploaded, err := uploader.Upload(writeContext, entry, policyTree, src)
	if err != nil {
		return fmt.Errorf("uploading database dump: %w", err)
	}

	// Update manifest
	manifest.EndTime = fs.UTCTimestampFromTime(time.Now())
	manifest.RootEntry = uploaded.RootEntry
	manifest.Stats = uploaded.Stats

	// Save manifest
	manifestID, err := snapshot.SaveSnapshot(writeContext, writer, manifest)
	if err != nil {
		return fmt.Errorf("saving snapshot: %w", err)
	}

	// Flush changes
	if err := writer.Flush(writeContext); err != nil {
		return fmt.Errorf("flushing changes: %w", err)
	}

	// Log success
	fmt.Printf("Created snapshot %v of database %s\n", manifestID, db.Name)
	return nil
}
