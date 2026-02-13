package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const defaultKubeconfigRemotePath = "/etc/rancher/rke2/rke2.yaml"

type KubeconfigCmd struct {
	ClusterDir           string        `name:"cluster-dir" help:"Path to cluster directory (reads defaults from cluster.yaml, updates it after success)."`
	Host                 string        `name:"host" help:"Remote host IP or hostname."`
	SSHKeyPath           string        `name:"ssh-key-path" help:"Path to SSH private key."`
	SSHUser              string        `name:"ssh-user" default:"ubuntu" help:"SSH username."`
	SSHPort              int           `name:"ssh-port" default:"22" help:"SSH port."`
	ClusterName          string        `name:"cluster-name" help:"Cluster name for kubeconfig context."`
	KubeconfigOut        string        `name:"kubeconfig-out" help:"Output kubeconfig path (default CLUSTER_NAME.kubeconfig)."`
	KubeconfigRemotePath string        `name:"kubeconfig-remote-path" help:"Remote path to kubeconfig file." default:"/etc/rancher/rke2/rke2.yaml"`
	Timeout              time.Duration `name:"timeout" default:"10m" help:"Timeout for SSH and API readiness."`
}

func (c *KubeconfigCmd) Run() error {
	// Load cluster.yaml defaults when --cluster-dir is provided.
	var cc *ClusterConfig
	var clusterDir string
	if c.ClusterDir != "" {
		clusterDir = c.ClusterDir
		var err error
		cc, err = readClusterConfig(clusterDir)
		fatalIf(err)
		if c.Host == "" {
			c.Host = cc.Controller.PublicIP
		}
		if c.ClusterName == "" {
			c.ClusterName = cc.ClusterName
		}
		if c.KubeconfigRemotePath == defaultKubeconfigRemotePath && cc.KubeconfigRemotePath != "" {
			c.KubeconfigRemotePath = cc.KubeconfigRemotePath
		}
		if c.KubeconfigOut == "" {
			c.KubeconfigOut = filepath.Join(clusterDir, "kubeconfig")
		}
	}

	if c.Host == "" {
		fatalf("--host is required (or use --cluster-dir)")
	}
	if c.ClusterName == "" {
		fatalf("--cluster-name is required (or use --cluster-dir)")
	}
	if c.KubeconfigOut == "" {
		c.KubeconfigOut = c.ClusterName + ".kubeconfig"
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	sshCfg, err := sshConfig(c.SSHUser, c.SSHKeyPath)
	fatalIf(err)

	client, raw, err := fetchRemoteKubeconfig(ctx, c.Host, c.SSHPort, sshCfg, c.KubeconfigRemotePath)
	fatalIf(err)
	defer client.Close()

	cfg, err := parseKubeconfig(raw)
	fatalIf(err)
	rewriteKubeconfig(cfg, c.Host, c.ClusterName)

	data, err := serializeKubeconfig(cfg)
	fatalIf(err)

	fmt.Println("waiting for Kubernetes API...")
	restCfg, err := restConfigFromKubeconfig(cfg)
	fatalIf(err)
	fatalIf(waitAPIReady(ctx, restCfg, 5*time.Second))

	fatalIf(writeKubeconfigFile(c.KubeconfigOut, data))
	fmt.Printf("kubeconfig written to %s\n", c.KubeconfigOut)
	fmt.Printf("export KUBECONFIG=%s\n", c.KubeconfigOut)

	// Update cluster.yaml when operating in a cluster directory.
	if cc != nil {
		if cc.Controller.InternalIP == "" {
			internalIP, err := sshRun(client, "hostname -I | awk '{print $1}'")
			fatalIf(err)
			cc.Controller.InternalIP = strings.TrimSpace(internalIP)
		}
		if cc.Kubeconfig == "" {
			cc.Kubeconfig = relPath(clusterDir, c.KubeconfigOut)
		}
		fatalIf(writeClusterConfig(clusterDir, cc))
		fmt.Printf("cluster config updated: %s/cluster.yaml\n", clusterDir)
	}

	return nil
}

// fetchRemoteKubeconfig connects via SSH and downloads the kubeconfig from
// remotePath. It retries on SSH connection errors (e.g. host reboots during
// cloud-init). Returns the SSH client (caller must close) and raw kubeconfig bytes.
func fetchRemoteKubeconfig(ctx context.Context, host string, port int, sshCfg *ssh.ClientConfig, remotePath string) (*ssh.Client, []byte, error) {
	var client *ssh.Client
	var raw []byte
	var err error

	for reconnects := 0; ; reconnects++ {
		if reconnects > 0 {
			client.Close()
			fmt.Fprintln(os.Stderr, "SSH connection lost, reconnecting...")
		}

		fmt.Printf("waiting for SSH on %s...\n", host)
		client, err = waitSSH(ctx, host, port, sshCfg, 5*time.Second)
		if err != nil {
			return nil, nil, err
		}

		fmt.Printf("waiting for %s...\n", remotePath)
		err = waitRemoteFile(ctx, client, remotePath, 5*time.Second)
		if isSSHConnectionError(err) {
			continue
		}
		if err != nil {
			client.Close()
			return nil, nil, err
		}

		fmt.Println("downloading kubeconfig...")
		raw, err = sshDownload(client, remotePath)
		if isSSHConnectionError(err) {
			continue
		}
		if err != nil {
			client.Close()
			return nil, nil, err
		}
		break
	}
	return client, raw, nil
}

// parseKubeconfig parses raw kubeconfig bytes into a structured Config.
func parseKubeconfig(data []byte) (*clientcmdapi.Config, error) {
	cfg, err := clientcmd.Load(data)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	return cfg, nil
}

// rewriteKubeconfig replaces the server host with publicIP in server URLs and
// renames all entries to clusterName. Works with any kubeconfig layout
// (RKE2 uses "default"/127.0.0.1, kubeadm uses "kubernetes"/internal-IP).
func rewriteKubeconfig(cfg *clientcmdapi.Config, publicIP, clusterName string) {
	for _, cluster := range cfg.Clusters {
		u, err := url.Parse(cluster.Server)
		if err != nil {
			continue
		}
		port := u.Port()
		if port == "" {
			port = "6443"
		}
		u.Host = publicIP + ":" + port
		cluster.Server = u.String()
	}

	renameSoleKey(cfg.Clusters, clusterName)
	renameSoleKey(cfg.AuthInfos, clusterName)

	// Rename the first context and fix its cluster/user references.
	for name, ctx := range cfg.Contexts {
		ctx.Cluster = clusterName
		ctx.AuthInfo = clusterName
		if name != clusterName {
			cfg.Contexts[clusterName] = ctx
			delete(cfg.Contexts, name)
		}
		break
	}

	cfg.CurrentContext = clusterName
}

// renameSoleKey renames the first key in the map to newName.
func renameSoleKey[V any](m map[string]V, newName string) {
	for name, v := range m {
		if name != newName {
			m[newName] = v
			delete(m, name)
		}
		break
	}
}

// serializeKubeconfig serializes a Config to YAML bytes.
func serializeKubeconfig(cfg *clientcmdapi.Config) ([]byte, error) {
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return nil, fmt.Errorf("serializing kubeconfig: %w", err)
	}
	return data, nil
}

// writeKubeconfigFile writes kubeconfig data to disk with 0600 permissions.
func writeKubeconfigFile(path string, data []byte) error {
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing kubeconfig to %s: %w", path, err)
	}
	return nil
}
