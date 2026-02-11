package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

func sshConfig(user, keyPath string) (*ssh.ClientConfig, error) {
	var signers []ssh.Signer

	if keyPath != "" {
		data, err := os.ReadFile(keyPath)
		if err != nil {
			return nil, fmt.Errorf("reading SSH key: %w", err)
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("parsing SSH key: %w", err)
		}
		signers = append(signers, signer)
	}

	// SSH agent fallback.
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		conn, err := net.Dial("unix", sock)
		if err == nil {
			agentSigners, err := agent.NewClient(conn).Signers()
			if err == nil {
				signers = append(signers, agentSigners...)
			}
		}
	}

	if len(signers) == 0 {
		return nil, fmt.Errorf("no SSH keys available: set --ssh-key-path or SSH_AUTH_SOCK")
	}

	return &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signers...)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}, nil
}

func sshDial(ctx context.Context, host string, port int, cfg *ssh.ClientConfig) (*ssh.Client, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return ssh.NewClient(sshConn, chans, reqs), nil
}

func sshRun(client *ssh.Client, cmd string) (string, error) {
	sess, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()
	out, err := sess.CombinedOutput(cmd)
	if err != nil {
		return "", fmt.Errorf("%s: %w", strings.TrimSpace(string(out)), err)
	}
	return string(out), nil
}

func sshDownload(client *ssh.Client, remotePath string) ([]byte, error) {
	out, err := sshRun(client, "cat "+remotePath)
	if err != nil {
		out, err = sshRun(client, "sudo cat "+remotePath)
	}
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", remotePath, err)
	}
	return []byte(out), nil
}

func waitSSH(ctx context.Context, host string, port int, cfg *ssh.ClientConfig, poll time.Duration) (*ssh.Client, error) {
	start := time.Now()
	attempt := 0
	for {
		client, err := sshDial(ctx, host, port, cfg)
		if err == nil {
			return client, nil
		}
		if isSSHAuthError(err) {
			return nil, fmt.Errorf("SSH authentication failed (check --ssh-key-path): %w", err)
		}
		attempt++
		if attempt%6 == 0 {
			fmt.Fprintf(os.Stderr, "  still waiting for SSH (%s)\n", time.Since(start).Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out waiting for SSH on %s:%d after %s: %w",
				host, port, time.Since(start).Round(time.Second), ctx.Err())
		case <-time.After(poll):
		}
	}
}

func waitRemoteFile(ctx context.Context, client *ssh.Client, path string, poll time.Duration) error {
	start := time.Now()
	attempt := 0
	for {
		_, err := sshRun(client, "test -f "+path)
		if err == nil {
			return nil
		}
		if isSSHConnectionError(err) {
			return err
		}
		attempt++
		if attempt%6 == 0 {
			fmt.Fprintf(os.Stderr, "  still waiting for %s (%s)\n", path, time.Since(start).Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for %s after %s: %w",
				path, time.Since(start).Round(time.Second), ctx.Err())
		case <-time.After(poll):
		}
	}
}

// isSSHConnectionError returns true if the error indicates a dead SSH
// connection rather than a remote command failure. An *ssh.ExitError means
// the command ran (connection is alive) but returned non-zero.
func isSSHConnectionError(err error) bool {
	if err == nil {
		return false
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	return true
}

// isSSHAuthError returns true if the error is an SSH authentication failure,
// which should not be retried.
func isSSHAuthError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain")
}
