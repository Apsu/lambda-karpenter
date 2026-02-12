package main

import (
	"fmt"
	"os"
	"path/filepath"

	"sigs.k8s.io/yaml"
)

// ClusterConfig is the cluster.yaml schema written by bootstrap and read by deploy.
type ClusterConfig struct {
	APIVersion  string `json:"apiVersion" yaml:"apiVersion"`
	ClusterName string `json:"clusterName" yaml:"clusterName"`
	Region      string `json:"region" yaml:"region"`
	ImageFamily string `json:"imageFamily" yaml:"imageFamily"`
	SSHKeyName  string `json:"sshKeyName" yaml:"sshKeyName"`
	JoinToken   string `json:"joinToken" yaml:"joinToken"`

	Controller ClusterController `json:"controller" yaml:"controller"`
	Versions   ClusterVersions   `json:"versions" yaml:"versions"`

	Kubeconfig     string   `json:"kubeconfig" yaml:"kubeconfig"`                                   // relative to cluster dir
	NodeClassFiles []string `json:"nodeClassFiles,omitempty" yaml:"nodeClassFiles,omitempty"`        // relative to cluster dir
	NodePoolFiles  []string `json:"nodePoolFiles,omitempty" yaml:"nodePoolFiles,omitempty"`          // relative to cluster dir
	GPUValues      string   `json:"gpuValues,omitempty" yaml:"gpuValues,omitempty"`                  // relative to cluster dir
}

type ClusterController struct {
	InstanceID   string `json:"instanceID" yaml:"instanceID"`
	InstanceType string `json:"instanceType" yaml:"instanceType"`
	InternalIP   string `json:"internalIP" yaml:"internalIP"`
	PublicIP     string `json:"publicIP" yaml:"publicIP"`
}

type ClusterVersions struct {
	GPUOperator     string `json:"gpuOperator" yaml:"gpuOperator"`
	LambdaKarpenter string `json:"lambdaKarpenter" yaml:"lambdaKarpenter"`
}

// KubeconfigPath resolves the kubeconfig path relative to clusterDir.
func (c *ClusterConfig) KubeconfigPath(clusterDir string) string {
	return resolvePath(clusterDir, c.Kubeconfig)
}

// GPUValuesPath resolves the GPU values path relative to clusterDir.
func (c *ClusterConfig) GPUValuesPath(clusterDir string) string {
	return resolvePath(clusterDir, c.GPUValues)
}

// ResolveNodeClassFiles resolves nodeclass file paths relative to clusterDir.
func (c *ClusterConfig) ResolveNodeClassFiles(clusterDir string) []string {
	return resolvePaths(clusterDir, c.NodeClassFiles)
}

// ResolveNodePoolFiles resolves nodepool file paths relative to clusterDir.
func (c *ClusterConfig) ResolveNodePoolFiles(clusterDir string) []string {
	return resolvePaths(clusterDir, c.NodePoolFiles)
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

// resolvePaths resolves a list of paths relative to a base directory.
func resolvePaths(base string, paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = resolvePath(base, p)
	}
	return out
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
