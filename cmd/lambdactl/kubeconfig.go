package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const rke2KubeconfigPath = "/etc/rancher/rke2/rke2.yaml"

type KubeconfigCmd struct {
	Host          string        `name:"host" required:"" help:"Remote host IP or hostname."`
	SSHKeyPath    string        `name:"ssh-key-path" help:"Path to SSH private key."`
	SSHUser       string        `name:"ssh-user" default:"ubuntu" help:"SSH username."`
	SSHPort       int           `name:"ssh-port" default:"22" help:"SSH port."`
	ClusterName   string        `name:"cluster-name" required:"" help:"Cluster name for kubeconfig context."`
	KubeconfigOut string        `name:"kubeconfig-out" help:"Output kubeconfig path (default CLUSTER_NAME.kubeconfig)."`
	Timeout       time.Duration `name:"timeout" default:"10m" help:"Timeout for SSH and API readiness."`
}

func (c *KubeconfigCmd) Run() error {
	if c.KubeconfigOut == "" {
		c.KubeconfigOut = c.ClusterName + ".kubeconfig"
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	sshCfg, err := sshConfig(c.SSHUser, c.SSHKeyPath)
	fatalIf(err)

	var client *ssh.Client
	var raw []byte
	for reconnects := 0; ; reconnects++ {
		if reconnects > 0 {
			client.Close()
			fmt.Fprintln(os.Stderr, "SSH connection lost, reconnecting...")
		}

		fmt.Printf("waiting for SSH on %s...\n", c.Host)
		client, err = waitSSH(ctx, c.Host, c.SSHPort, sshCfg, 5*time.Second)
		fatalIf(err)

		fmt.Printf("waiting for %s...\n", rke2KubeconfigPath)
		err = waitRemoteFile(ctx, client, rke2KubeconfigPath, 5*time.Second)
		if isSSHConnectionError(err) {
			continue
		}
		fatalIf(err)

		fmt.Println("downloading kubeconfig...")
		raw, err = sshDownload(client, rke2KubeconfigPath)
		if isSSHConnectionError(err) {
			continue
		}
		fatalIf(err)
		break
	}
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
	return nil
}

// parseKubeconfig parses raw kubeconfig bytes into a structured Config.
func parseKubeconfig(data []byte) (*clientcmdapi.Config, error) {
	cfg, err := clientcmd.Load(data)
	if err != nil {
		return nil, fmt.Errorf("parsing kubeconfig: %w", err)
	}
	return cfg, nil
}

// rewriteKubeconfig replaces 127.0.0.1 with publicIP in server URLs and
// renames all entries from "default" to clusterName.
func rewriteKubeconfig(cfg *clientcmdapi.Config, publicIP, clusterName string) {
	for _, cluster := range cfg.Clusters {
		if strings.Contains(cluster.Server, "127.0.0.1") {
			cluster.Server = strings.Replace(cluster.Server, "127.0.0.1", publicIP, 1)
		}
	}

	renameMapKey(cfg.Clusters, "default", clusterName)
	renameMapKey(cfg.AuthInfos, "default", clusterName)

	if ctx, ok := cfg.Contexts["default"]; ok {
		ctx.Cluster = clusterName
		ctx.AuthInfo = clusterName
		delete(cfg.Contexts, "default")
		cfg.Contexts[clusterName] = ctx
	}

	cfg.CurrentContext = clusterName
}

func renameMapKey[V any](m map[string]V, old, new string) {
	if old == new {
		return
	}
	if v, ok := m[old]; ok {
		m[new] = v
		delete(m, old)
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
