package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/avolut/backup/internal/repository"
	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/blob/b2"
)

type IPInfo struct {
	Hostname   string              `json:"hostname"`
	Timestamp  time.Time           `json:"timestamp"`
	Interfaces map[string][]string `json:"interfaces"`
}

// blobWrapper implements blob.Bytes interface for B2 storage
type blobWrapper struct {
	data []byte
}

func (b *blobWrapper) Length() int {
	return int(len(b.data))
}

func (b *blobWrapper) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(b.data)
	return int64(n), err
}

type readSeekCloser struct {
	*bytes.Reader
}

func (r *readSeekCloser) Close() error {
	return nil
}

func (b *blobWrapper) Reader() io.ReadSeekCloser {
	return &readSeekCloser{bytes.NewReader(b.data)}
}

func (b *blobWrapper) Close() error {
	return nil
}

func CollectAndStoreIPs(ctx context.Context, name string) error {
	// Get all network interfaces
	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("getting network interfaces: %w", err)
	}

	// Create IP info structure
	ipInfo := IPInfo{
		Hostname:   getHostname(),
		Timestamp:  time.Now(),
		Interfaces: make(map[string][]string),
	}

	// Collect IP addresses for each interface
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		var ips []string
		for _, addr := range addrs {
			// Extract IP address without network mask
			switch v := addr.(type) {
			case *net.IPNet:
				ips = append(ips, v.IP.String())
			case *net.IPAddr:
				ips = append(ips, v.IP.String())
			}
		}

		if len(ips) > 0 {
			ipInfo.Interfaces[iface.Name] = ips
		}
	}

	// Marshal IP info to JSON
	ipJSON, err := json.MarshalIndent(ipInfo, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling IP info: %w", err)
	}

	// Create temporary file
	tmpDir := filepath.Join(".avolut", "tmp")
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return fmt.Errorf("creating temporary directory: %w", err)
	}

	tmpFile := filepath.Join(tmpDir, "ips.json")
	if err := os.WriteFile(tmpFile, ipJSON, 0600); err != nil {
		return fmt.Errorf("writing temporary file: %w", err)
	}

	// Initialize B2 client with properly formatted prefix
	prefix := name
	if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}

	opts := &b2.Options{
		BucketName: repository.B2BucketName,
		KeyID:      repository.B2KeyID,
		Key:        repository.B2Key,
		Prefix:     prefix,
	}

	st, err := b2.New(ctx, opts, true)
	if err != nil {
		return fmt.Errorf("connecting to B2: %w", err)
	}

	// Read the file content
	content, err := os.ReadFile(tmpFile)
	if err != nil {
		return fmt.Errorf("reading temporary file: %w", err)
	}

	// Upload to B2 with fixed filename
	blobID := blob.ID("ips.json")
	blobBytes := &blobWrapper{content}
	if err := st.PutBlob(ctx, blobID, blobBytes, blob.PutOptions{}); err != nil {
		return fmt.Errorf("uploading to B2: %w", err)
	}

	// Clean up temporary file
	os.Remove(tmpFile)

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
