package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kopia/kopia/fs"
	"github.com/kopia/kopia/fs/localfs"
	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/blob/b2"
	"github.com/kopia/kopia/repo/content"
	"github.com/kopia/kopia/snapshot"
	"github.com/kopia/kopia/snapshot/policy"
	"github.com/kopia/kopia/snapshot/snapshotfs"
	"gopkg.in/yaml.v3"
)

const (
	backupPassword = "kopia-backup-2025"
	b2BucketName   = "avolut-backup"
	b2KeyID        = "004a2c1d76ae1cf0000000003"
	b2Key          = "K00451kcIteAJimwP0eNKABY9F9SGqE"
	configFile     = ".avolut/files/repository.config"
	configDB       = ".avolut/dbs/repository.config"
)

type Config struct {
	Name        string   `yaml:"name"`
	Directories []string `yaml:"directories"`
	Schedule    string   `yaml:"schedule"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	var config Config
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	return &config, nil
}

func initializeB2(ctx context.Context, storage blob.Storage, configPath string) error {
	// Initialize repository only if it doesn't exist
	formatOpts := repo.NewRepositoryOptions{}
	if err := repo.Initialize(ctx, storage, &formatOpts, backupPassword); err != nil {
		if err != repo.ErrAlreadyInitialized {
			return fmt.Errorf("initializing repository: %w", err)
		}
		log.Printf("Repository already initialized for %s", configPath)
	}

	return nil
}

func formatPrefix(name string, suffix string) string {
	// Convert to lowercase and replace non-alphanumeric with underscore
	var result strings.Builder
	prevUnderscore := false

	for _, char := range strings.ToLower(name) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') {
			result.WriteRune(char)
			prevUnderscore = false
		} else if !prevUnderscore {
			result.WriteRune('_')
			prevUnderscore = true
		}
	}

	// Trim underscores from the end
	prefix := strings.TrimRight(result.String(), "_")

	// Ensure it ends with /files/
	if !strings.HasSuffix(prefix, "/"+suffix+"/") {
		prefix = prefix + "/" + suffix + "/"
	}

	return prefix
}

func connectToRepository(ctx context.Context, config *Config, configPath string, suffix string) (repo.Repository, error) {

	// Create all parent directories for the config file
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return nil, fmt.Errorf("creating config directories: %w", err)
	}

	// Create config file with proper JSON structure
	configData := map[string]interface{}{
		"storage": map[string]interface{}{
			"type": "b2",
			"config": map[string]interface{}{
				"bucket": b2BucketName,
				"prefix": formatPrefix(config.Name, suffix),
				"keyID":  b2KeyID,
				"key":    b2Key,
			},
		},
		"caching": map[string]interface{}{
			"cacheDirectory": "cache",
		},
		"hostname":                getHostname(),
		"username":                os.Getenv("USER"),
		"description":             fmt.Sprintf("Repository in B2: %s", b2BucketName),
		"enableActions":           false,
		"formatBlobCacheDuration": 900000000000,
	}

	configJSON, err := json.MarshalIndent(configData, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling config data: %w", err)
	}

	if err := os.WriteFile(configPath, configJSON, 0600); err != nil {
		return nil, fmt.Errorf("writing config file: %w", err)
	}

	// Open repository and return it
	r, err := repo.Open(ctx, configPath, backupPassword, &repo.Options{})
	if err != nil {
		return nil, fmt.Errorf("opening repository: %w", err)
	}

	// Use B2 configuration from constants
	opts := &b2.Options{
		BucketName: b2BucketName,
		KeyID:      b2KeyID,
		Key:        b2Key,
		Prefix:     formatPrefix(config.Name, suffix),
	}

	st, err := b2.New(ctx, opts, false)
	if err != nil {
		return nil, fmt.Errorf("connecting to B2: %w", err)
	}

	initializeB2(ctx, st, configPath)

	// Connect to the repository using local config with password
	if err := repo.Connect(ctx, configPath, st, backupPassword, &repo.ConnectOptions{
		CachingOptions: content.CachingOptions{
			CacheDirectory:        ".avolut/" + suffix + "/cache",
			ContentCacheSizeBytes: 1024 * 1024 * 1024, // 1GB
		},
	}); err != nil {
		return nil, fmt.Errorf("connecting to repository: %w", err)
	}

	return r, nil
}

func backupDirectory(ctx context.Context, rep repo.Repository, path string) error {
	source := snapshot.SourceInfo{
		Path:     path,
		UserName: os.Getenv("USER"),
		Host:     getHostname(),
	}

	dir, err := localfs.Directory(path)
	if err != nil {
		return fmt.Errorf("getting directory entry: %w", err)
	}

	writeContext, writer, err := rep.NewWriter(ctx, repo.WriteSessionOptions{
		Purpose: "Backup directory",
	})
	if err != nil {
		return fmt.Errorf("creating writer session: %w", err)
	}
	defer writer.Close(writeContext)

	uploader := snapshotfs.NewUploader(writer)
	policyTree := policy.BuildTree(nil, policy.DefaultPolicy)

	manifest := &snapshot.Manifest{
		Source:      source,
		Description: fmt.Sprintf("Backup of %s", path),
	}
	manifest.StartTime = fs.UTCTimestampFromTime(time.Now())
	uploaded, err := uploader.Upload(writeContext, dir, policyTree, source)
	if err != nil {
		return fmt.Errorf("uploading directory: %w", err)
	}

	manifest.EndTime = fs.UTCTimestampFromTime(time.Now())
	manifest.RootEntry = uploaded.RootEntry
	manifest.Stats = uploaded.Stats

	manifestID, err := snapshot.SaveSnapshot(writeContext, writer, manifest)
	if err != nil {
		return fmt.Errorf("saving snapshot: %w", err)
	}

	if err := writer.Flush(writeContext); err != nil {
		return fmt.Errorf("flushing changes: %w", err)
	}

	log.Printf("Created snapshot %v of %v\n", manifestID, path)
	return nil
}

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

func main() {
	ctx := context.Background()
	config, err := loadConfig("backup.yaml")
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	// Initialize file backup repository
	fileRepo, err := connectToRepository(ctx, config, configFile, "files")
	if err != nil {
		log.Fatalf("Error connecting to file repository: %v", err)
	}
	defer fileRepo.Close(ctx)

	// Initialize database backup repository
	dbRepo, err := connectToRepository(ctx, config, configDB, "dbs")
	if err != nil {
		log.Fatalf("Error connecting to database repository: %v", err)
	}
	defer dbRepo.Close(ctx)

	// Backup directories using file repository
	for _, dir := range config.Directories {
		if err := backupDirectory(ctx, fileRepo, dir); err != nil {
			log.Printf("Error backing up %s: %v", dir, err)
			continue
		}
	}
}
