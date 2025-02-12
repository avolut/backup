package backup

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/kopia/kopia/fs"
	"github.com/kopia/kopia/fs/localfs"
	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/snapshot"
	"github.com/kopia/kopia/snapshot/policy"
	"github.com/kopia/kopia/snapshot/snapshotfs"
)

func BackupDir(ctx context.Context, r repo.Repository, dirPath string) error {
	// Verify directory exists
	info, err := os.Stat(dirPath)
	if err != nil {
		return fmt.Errorf("error accessing directory %s: %v", dirPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dirPath)
	}

	// Create source directory entry
	source, err := filepath.Abs(dirPath)
	if err != nil {
		return fmt.Errorf("error getting absolute path: %v", err)
	}

	// Create entry point for the directory
	entry, err := localfs.Directory(source)
	if err != nil {
		return fmt.Errorf("error creating directory entry: %w", err)
	}

	// Create snapshot source
	src := snapshot.SourceInfo{
		Host:     "localhost",
		UserName: os.Getenv("USER"),
		Path:     source,
	}

	// Create writer session
	writeContext, writer, err := r.NewWriter(ctx, repo.WriteSessionOptions{
		Purpose: "Backup directory",
	})
	if err != nil {
		return fmt.Errorf("creating writer session: %w", err)
	}
	defer func() {
		if cerr := writer.Close(writeContext); cerr != nil {
			fmt.Printf("Warning: error closing writer: %v\n", cerr)
		}
	}()

	// Create uploader
	uploader := snapshotfs.NewUploader(writer)

	// Create policy tree
	policyTree := policy.BuildTree(nil, policy.DefaultPolicy)

	// Create manifest
	manifest := &snapshot.Manifest{
		Source:      src,
		Description: fmt.Sprintf("Backup of %s", source),
	}
	manifest.StartTime = fs.UTCTimestampFromTime(time.Now())

	// Upload the snapshot
	uploaded, err := uploader.Upload(writeContext, entry, policyTree, src)
	if err != nil {
		return fmt.Errorf("uploading directory: %w", err)
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
	fmt.Printf("Created snapshot %v of %v\n", manifestID, source)
	return nil
}
