package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"sigs.k8s.io/yaml"
)

// ClusterConfig is the cluster.yaml schema written by bootstrap and read by gather/kubeconfig.
type ClusterConfig struct {
	APIVersion  string `json:"apiVersion" yaml:"apiVersion"`
	ClusterName string `json:"clusterName" yaml:"clusterName"`
	Region      string `json:"region" yaml:"region"`
	ImageFamily string `json:"imageFamily" yaml:"imageFamily"`
	SSHKeyName  string `json:"sshKeyName" yaml:"sshKeyName"`
	JoinToken   string `json:"joinToken" yaml:"joinToken"`

	Controller ClusterController `json:"controller" yaml:"controller"`

	Kubeconfig           string `json:"kubeconfig" yaml:"kubeconfig"`                                         // relative to cluster dir
	KubeconfigRemotePath string `json:"kubeconfigRemotePath,omitempty" yaml:"kubeconfigRemotePath,omitempty"` // remote path on controller
}

type ClusterController struct {
	InstanceID   string `json:"instanceID" yaml:"instanceID"`
	InstanceType string `json:"instanceType" yaml:"instanceType"`
	InternalIP   string `json:"internalIP" yaml:"internalIP"`
	PublicIP     string `json:"publicIP" yaml:"publicIP"`
}

// KubeconfigPath resolves the kubeconfig path relative to clusterDir.
func (c *ClusterConfig) KubeconfigPath(clusterDir string) string {
	return resolvePath(clusterDir, c.Kubeconfig)
}

// TemplateData returns a map of template variables for .tmpl rendering.
func (c *ClusterConfig) TemplateData() map[string]string {
	return map[string]string{
		"ClusterName":          c.ClusterName,
		"Region":               c.Region,
		"ImageFamily":          c.ImageFamily,
		"SSHKeyName":           c.SSHKeyName,
		"JoinToken":            c.JoinToken,
		"ControllerIP":         c.Controller.InternalIP,
		"ControllerInstanceID": c.Controller.InstanceID,
		"ControllerPublicIP":   c.Controller.PublicIP,
	}
}

// writeClusterConfig marshals cfg as YAML and writes it to dir/cluster.yaml.
func writeClusterConfig(dir string, cfg *ClusterConfig) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("creating cluster dir %s: %w", dir, err)
	}
	cfg.APIVersion = "lambdactl/v1alpha1"
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling cluster config: %w", err)
	}
	path := filepath.Join(dir, "cluster.yaml")
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// readClusterConfig reads and validates cluster.yaml from dir.
func readClusterConfig(dir string) (*ClusterConfig, error) {
	path := filepath.Join(dir, "cluster.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var cfg ClusterConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if cfg.ClusterName == "" {
		return nil, fmt.Errorf("%s: clusterName is required", path)
	}
	return &cfg, nil
}

// resolvePath resolves a relative path against a base directory.
// Returns "" for empty paths, absolute paths are returned as-is.
func resolvePath(base, path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(base, path)
}

// relPath computes the path of target relative to base directory.
// Falls back to the absolute path of target on error.
func relPath(base, target string) string {
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return target
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return absTarget
	}
	rel, err := filepath.Rel(absBase, absTarget)
	if err != nil {
		return absTarget
	}
	return rel
}

// findClusterDir returns the cluster directory path. If explicit is set, it is
// used directly. Otherwise, if ./cluster.yaml exists, "." is returned. Returns
// "" if no cluster directory can be found.
func findClusterDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if _, err := os.Stat("cluster.yaml"); err == nil {
		return "."
	}
	return ""
}

// gatherClusterInfo SSHes into the controller and populates missing fields in
// the ClusterConfig: kubeconfig and internalIP. It is idempotent — fields that
// are already set are not overwritten. The updated config is written back to
// cluster.yaml in clusterDir.
func gatherClusterInfo(ctx context.Context, cc *ClusterConfig, clusterDir, sshUser, sshKeyPath string) error {
	needKubeconfig := cc.Kubeconfig == ""
	needInternalIP := cc.Controller.InternalIP == ""
	if !needKubeconfig && !needInternalIP {
		return nil
	}

	if cc.Controller.PublicIP == "" {
		return fmt.Errorf("controller.publicIP is required for gather")
	}

	sshCfg, err := sshConfig(sshUser, sshKeyPath)
	if err != nil {
		return err
	}

	remotePath := cc.KubeconfigRemotePath
	if remotePath == "" {
		remotePath = defaultKubeconfigRemotePath
	}

	var sshClient *ssh.Client

	if needKubeconfig {
		var raw []byte
		sshClient, raw, err = fetchRemoteKubeconfig(ctx, cc.Controller.PublicIP, 22, sshCfg, remotePath)
		if err != nil {
			return err
		}
		defer sshClient.Close()

		kubeCfg, err := parseKubeconfig(raw)
		if err != nil {
			return err
		}
		rewriteKubeconfig(kubeCfg, cc.Controller.PublicIP, cc.ClusterName)

		data, err := serializeKubeconfig(kubeCfg)
		if err != nil {
			return err
		}

		fmt.Println("waiting for Kubernetes API...")
		restCfg, err := restConfigFromKubeconfig(kubeCfg)
		if err != nil {
			return err
		}
		if err := waitAPIReady(ctx, restCfg, 5*time.Second); err != nil {
			return err
		}

		kubeconfigPath := filepath.Join(clusterDir, "kubeconfig")
		if err := writeKubeconfigFile(kubeconfigPath, data); err != nil {
			return err
		}
		fmt.Printf("kubeconfig written to %s\n", kubeconfigPath)
		fmt.Printf("export KUBECONFIG=%s\n", kubeconfigPath)

		cc.Kubeconfig = "kubeconfig"
	}

	if needInternalIP {
		if sshClient == nil {
			fmt.Printf("waiting for SSH on %s...\n", cc.Controller.PublicIP)
			sshClient, err = waitSSH(ctx, cc.Controller.PublicIP, 22, sshCfg, 5*time.Second)
			if err != nil {
				return err
			}
			defer sshClient.Close()
		}
		internalIP, err := sshRun(sshClient, "hostname -I | awk '{print $1}'")
		if err != nil {
			return err
		}
		cc.Controller.InternalIP = strings.TrimSpace(internalIP)
	}

	if err := writeClusterConfig(clusterDir, cc); err != nil {
		return err
	}
	fmt.Printf("cluster config updated: %s/cluster.yaml\n", clusterDir)
	return nil
}
