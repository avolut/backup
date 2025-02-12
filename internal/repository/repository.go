package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/avolut/backup/internal/config"
	"github.com/kopia/kopia/repo"
	"github.com/kopia/kopia/repo/blob/b2"
	"github.com/kopia/kopia/repo/content"
)

type ConfigType int

const (
	ConfigFile ConfigType = iota
	ConfigDB
)

const (
	backupPassword = "kopia-backup-2025"
	b2BucketName   = "avolut-backup"
	b2KeyID        = "004a2c1d76ae1cf0000000003"
	b2Key          = "K00451kcIteAJimwP0eNKABY9F9SGqE"
)

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

	// Ensure it ends with /suffix/
	if !strings.HasSuffix(prefix, "/"+suffix+"/") {
		prefix = prefix + "/" + suffix + "/"
	}

	return prefix
}

func ConnectToRepository(ctx context.Context, cfg *config.Config, configType ConfigType, suffix string) (repo.Repository, error) {
	// Create config file path
	configPath := filepath.Join(".avolut", suffix, "repository.config")

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
				"prefix": formatPrefix(cfg.Name, suffix),
				"keyID":  b2KeyID,
				"key":    b2Key,
			},
		},
		"caching": map[string]interface{}{
			"cacheDirectory": "cache",
		},
		"hostname":                "avolut-backup",
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

	// Open repository
	r, err := repo.Open(ctx, configPath, backupPassword, &repo.Options{})
	if err != nil {
		return nil, fmt.Errorf("opening repository: %w", err)
	}

	// Use B2 configuration with TLS settings
	opts := &b2.Options{
		BucketName: b2BucketName,
		KeyID:      b2KeyID,
		Key:        b2Key,
		Prefix:     formatPrefix(cfg.Name, suffix),
	}

	st, err := b2.New(ctx, opts, true)
	if err != nil {
		return nil, fmt.Errorf("connecting to B2: %w", err)
	}

	// Initialize repository if needed
	if err := repo.Initialize(ctx, st, &repo.NewRepositoryOptions{}, backupPassword); err != nil {
		if err != repo.ErrAlreadyInitialized {
			return nil, fmt.Errorf("initializing repository: %w", err)
		}
	}

	// Connect to the repository
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
