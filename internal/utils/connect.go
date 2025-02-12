package utils

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/avolut/backup/internal/repository"
	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/blob/b2"
	"golang.org/x/crypto/ssh"
)

// customBuffer implements blob.OutputBuffer interface
type customBuffer struct {
	*bytes.Buffer
}

func (b *customBuffer) Length() int {
	return b.Buffer.Len()
}

// ConnectToHost attempts to establish an SSH connection to a host
func ConnectToHost(ctx context.Context, name string) error {
	// Initialize B2 client
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

	// Read IP information from B2
	blobID := blob.ID("ips.json")
	buffer := &customBuffer{Buffer: &bytes.Buffer{}}
	if err := st.GetBlob(ctx, blobID, 0, -1, buffer); err != nil {
		return fmt.Errorf("reading IP information from B2: %w", err)
	}

	// Read and parse JSON data
	data := buffer.Bytes()
	var ipInfo IPInfo
	if err := json.Unmarshal(data, &ipInfo); err != nil {
		return fmt.Errorf("parsing IP information: %w", err)
	}

	// Check if IP information is too old (e.g., more than 1 hour)
	if time.Since(ipInfo.Timestamp) > time.Hour {
		return fmt.Errorf("IP information is too old (last updated: %s)", ipInfo.Timestamp)
	}

	// Get public key from B2
	blobID = blob.ID("priv.key")
	buffer = &customBuffer{Buffer: &bytes.Buffer{}}
	if err := st.GetBlob(ctx, blobID, 0, -1, buffer); err != nil {
		return fmt.Errorf("reading public key from B2: %w", err)
	}

	// Parse private key
	privKeyBytes := buffer.Bytes()
	privKey, err := ssh.ParsePrivateKey(privKeyBytes)
	if err != nil {
		return fmt.Errorf("parsing private key: %w", err)
	}

	// Try to connect to each IP
	var lastErr error
	for _, ips := range ipInfo.Interfaces {
		for _, ip := range ips {
			// Check if port is open
			if !isPortOpen(ip, 41334) {
				lastErr = fmt.Errorf("port 41334 is not open on %s", ip)
				continue
			}

			// Try SSH connection
			if err := trySSHConnection(ip, 41334, privKey); err != nil {
				lastErr = fmt.Errorf("SSH connection failed to %s: %w", ip, err)
				continue
			}

			return nil // Successfully connected
		}
	}

	if lastErr != nil {
		return fmt.Errorf("failed to connect to %s: %w", name, lastErr)
	}
	return fmt.Errorf("no available hosts found for %s", name)
}

func isPortOpen(ip string, port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", ip, port), 5*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func trySSHConnection(ip string, port int, privKey ssh.Signer) error {
	// Configure SSH client
	config := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{
			ssh.PublicKeys(privKey),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	// Try to connect
	client, err := ssh.Dial("tcp", fmt.Sprintf("%s:%d", ip, port), config)
	if err != nil {
		return fmt.Errorf("SSH connection failed: %w", err)
	}
	defer client.Close()

	// Create a session
	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("failed to create session: %w", err)
	}
	defer session.Close()

	// Set up stdio forwarding
	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	// Set up terminal modes with proper control sequence handling and mouse support
	modes := ssh.TerminalModes{}

	// Request pseudo terminal with mouse support
	if err := session.RequestPty("xterm-256color", 40, 80, modes); err != nil {
		return fmt.Errorf("request for pseudo terminal failed: %w", err)
	}

	// Start shell
	if err := session.Shell(); err != nil {
		return fmt.Errorf("failed to start shell: %w", err)
	}

	// Wait for session to finish
	return session.Wait()
}
