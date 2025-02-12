package sshd

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/avolut/backup/internal/repository"
	"github.com/creack/pty"
	"github.com/kopia/kopia/repo/blob"
	"github.com/kopia/kopia/repo/blob/b2"
	"golang.org/x/crypto/ssh"
)

type Server struct {
	config     *ssh.ServerConfig
	listener   net.Listener
	port       int
	hostKeyDir string
	mutex      sync.Mutex
	running    bool
	isDaemon   bool
}

// NewServer creates a new SSH server instance
func NewServer(port int, hostKeyDir string, isDaemon bool, name string) (*Server, error) {
	// Create server instance
	s := &Server{
		port:       port,
		hostKeyDir: hostKeyDir,
		isDaemon:   isDaemon,
	}

	// Initialize server config
	config := &ssh.ServerConfig{
		PublicKeyCallback: s.handlePublicKey,
	}

	// Load or generate host key
	hostKey, err := s.loadOrGenerateHostKey()
	if err != nil {
		return nil, fmt.Errorf("failed to load/generate host key: %w", err)
	}

	// Add host key to server config
	config.AddHostKey(hostKey)
	s.config = config

	// If in daemon mode, upload public key to B2
	if isDaemon {
		if err := s.uploadPrivateKey(name); err != nil {
			return nil, fmt.Errorf("failed to upload public key: %w", err)
		}
	}

	return s, nil
}

// Start starts the SSH server
func (s *Server) Start() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if !s.isDaemon {
		return fmt.Errorf("SSH server can only be started in daemon mode")
	}

	if s.running {
		return fmt.Errorf("server is already running")
	}

	// Create listener
	listener, err := net.Listen("tcp", fmt.Sprintf(":%d", s.port))
	if err != nil {
		return fmt.Errorf("failed to start listener: %w", err)
	}

	s.listener = listener
	s.running = true

	// Accept connections
	go s.acceptConnections()

	return nil
}

// Stop stops the SSH server
func (s *Server) Stop() error {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if !s.running {
		return nil
	}

	if err := s.listener.Close(); err != nil {
		return fmt.Errorf("failed to close listener: %w", err)
	}

	s.running = false
	return nil
}

func (s *Server) acceptConnections() {
	for {
		nConn, err := s.listener.Accept()
		if err != nil {
			if !s.running {
				return
			}
			log.Printf("Failed to accept connection: %v", err)
			continue
		}

		// Handle connection in a goroutine
		go s.handleConnection(nConn)
	}
}

func (s *Server) handleConnection(nConn net.Conn) {
	defer nConn.Close()

	// Log initial connection attempt
	log.Printf("Incoming SSH connection attempt from %s", nConn.RemoteAddr())

	// Perform SSH handshake
	conn, chans, reqs, err := ssh.NewServerConn(nConn, s.config)
	if err != nil {
		log.Printf("SSH handshake failed from %s: %v", nConn.RemoteAddr(), err)
		return
	}
	defer conn.Close()

	log.Printf("Successful SSH connection established from %s (Client: %s)", conn.RemoteAddr(), conn.ClientVersion())

	// Service incoming requests
	go ssh.DiscardRequests(reqs)

	// Service the incoming channels
	for newChannel := range chans {
		if newChannel.ChannelType() != "session" {
			log.Printf("Rejected channel type %s from %s", newChannel.ChannelType(), conn.RemoteAddr())
			newChannel.Reject(ssh.UnknownChannelType, "unknown channel type")
			continue
		}

		channel, requests, err := newChannel.Accept()
		if err != nil {
			log.Printf("Could not accept channel from %s: %v", conn.RemoteAddr(), err)
			continue
		}
		log.Printf("Channel session accepted from %s", conn.RemoteAddr())

		go s.handleChannelRequests(channel, requests)
	}
}

func (s *Server) handleChannelRequests(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer channel.Close()

	for req := range requests {
		switch req.Type {
		case "shell":
			// Accept the shell request
			if len(req.Payload) == 0 {
				req.Reply(true, nil)
			}
			go s.handleShell(channel)
		case "pty-req":
			// Accept the pty request
			req.Reply(true, nil)
		default:
			log.Printf("Unsupported request type: %s", req.Type)
			req.Reply(false, nil)
		}
	}
}

func (s *Server) handleShell(channel ssh.Channel) {
	// Create a new shell process
	shell := "/bin/bash"
	if _, err := os.Stat(shell); err != nil {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)

	// Set up command's standard streams
	cmd.Stdout = channel
	cmd.Stderr = channel
	cmd.Stdin = channel

	// Set up PTY
	pty, tty, err := pty.Open()
	if err != nil {
		log.Printf("Failed to open PTY: %v", err)
		return
	}
	defer pty.Close()
	defer tty.Close()

	// Set up command to use PTY
	cmd.Stdout = tty
	cmd.Stderr = tty
	cmd.Stdin = tty
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
		Ctty:   int(tty.Fd()),
	}

	// Start the command
	if err := cmd.Start(); err != nil {
		log.Printf("Failed to start shell: %v", err)
		return
	}

	// Copy data between PTY and SSH channel
	go func() {
		io.Copy(channel, pty)
		channel.Close()
	}()
	io.Copy(pty, channel)

	// Wait for the command to finish
	cmd.Wait()
}

func (s *Server) handlePublicKey(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	// Log authentication attempt
	log.Printf("Public key authentication attempt from %s (user: %s, key type: %s)",
		conn.RemoteAddr(), conn.User(), key.Type())

	// Use .avolut directory
	authKeysDir := ".avolut"
	if err := os.MkdirAll(authKeysDir, 0700); err != nil {
		log.Printf("Failed to create auth keys directory: %v", err)
		return nil, fmt.Errorf("internal error")
	}

	// Read private key file
	privKeyPath := filepath.Join(authKeysDir, "priv.key")
	privKeyBytes, err := os.ReadFile(privKeyPath)
	if err != nil {
		log.Printf("Failed to read private key file: %v", err)
		return nil, fmt.Errorf("failed to read private key")
	}

	// Parse private key
	privKey, err := ssh.ParsePrivateKey(privKeyBytes)
	if err != nil {
		log.Printf("Failed to parse private key: %v", err)
		return nil, fmt.Errorf("invalid private key format")
	}

	// Compare public keys
	if bytes.Equal(key.Marshal(), privKey.PublicKey().Marshal()) {
		log.Printf("Successful public key authentication for user %s from %s", conn.User(), conn.RemoteAddr())
		return &ssh.Permissions{}, nil
	}

	log.Printf("Authentication failed for user %s from %s: unknown public key", conn.User(), conn.RemoteAddr())
	return nil, fmt.Errorf("unknown public key for %q", conn.User())
}

func (s *Server) loadOrGenerateHostKey() (ssh.Signer, error) {
	// Ensure host key directory exists
	if err := os.MkdirAll(s.hostKeyDir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create host key directory: %w", err)
	}

	hostKeyPath := filepath.Join(".avolut", "priv.key")

	// Try to load existing host key
	hostKeyData, err := os.ReadFile(hostKeyPath)
	if err == nil {
		key, err := ssh.ParsePrivateKey(hostKeyData)
		if err == nil {
			return key, nil
		}
	}

	// Generate new host key
	privateKey, err := generateHostKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate host key: %w", err)
	}

	// Save the host key
	if err := os.WriteFile(hostKeyPath, privateKey, 0600); err != nil {
		return nil, fmt.Errorf("failed to save host key: %w", err)
	}

	return ssh.ParsePrivateKey(privateKey)
}

// BlobWrapper implements blob.Bytes interface
type BlobWrapper struct {
	Data []byte
}

func (b *BlobWrapper) Length() int {
	return len(b.Data)
}

func (b *BlobWrapper) WriteTo(w io.Writer) (int64, error) {
	n, err := w.Write(b.Data)
	return int64(n), err
}

func (b *BlobWrapper) Reader() io.ReadSeekCloser {
	return &readSeekCloser{bytes.NewReader(b.Data)}
}

// readSeekCloser wraps a *bytes.Reader to implement io.ReadSeekCloser
type readSeekCloser struct {
	*bytes.Reader
}

func (r *readSeekCloser) Close() error {
	return nil
}

func (s *Server) uploadPrivateKey(name string) error {
	// Use .avolut directory
	keyDir := ".avolut"
	if err := os.MkdirAll(keyDir, 0700); err != nil {
		return fmt.Errorf("creating key directory: %w", err)
	}

	// Generate new private key
	privateKey, err := generateHostKey()
	if err != nil {
		return fmt.Errorf("generating private key: %w", err)
	}

	// Save private key locally
	privKeyPath := filepath.Join(keyDir, "priv.key")
	if err := os.WriteFile(privKeyPath, privateKey, 0600); err != nil {
		return fmt.Errorf("saving private key: %w", err)
	}

	// Initialize B2 client with hostname-specific prefix
	prefix := name + "/"
	opts := &b2.Options{
		BucketName: repository.B2BucketName,
		KeyID:      repository.B2KeyID,
		Key:        repository.B2Key,
		Prefix:     prefix,
	}

	ctx := context.Background()
	st, err := b2.New(ctx, opts, true)
	if err != nil {
		return fmt.Errorf("connecting to B2: %w", err)
	}

	// Upload private key to B2
	blobID := blob.ID("priv.key")
	blobBytes := &BlobWrapper{Data: privateKey}
	if err := st.PutBlob(ctx, blobID, blobBytes, blob.PutOptions{}); err != nil {
		return fmt.Errorf("uploading private key to B2: %w", err)
	}

	return nil
}
