package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"text/template"
	"time"

	"github.com/evecallicoat/lambda-karpenter/internal/lambdaclient"
	"sigs.k8s.io/yaml"
)

// BootstrapConfig is the YAML config file format for bootstrap.
type BootstrapConfig struct {
	ClusterName          string   `json:"clusterName" yaml:"clusterName"`
	ClusterType          string   `json:"clusterType" yaml:"clusterType"` // "kubeadm" or "rke2"
	Region               string   `json:"region" yaml:"region"`
	InstanceType         string   `json:"instanceType" yaml:"instanceType"`
	ImageFamily          string   `json:"imageFamily" yaml:"imageFamily"`
	SSHKeyName           string   `json:"sshKeyName" yaml:"sshKeyName"`
	SSHKeyPath           string   `json:"sshKeyPath" yaml:"sshKeyPath"`
	SSHUser              string   `json:"sshUser" yaml:"sshUser"`
	CloudInit            string   `json:"cloudInit" yaml:"cloudInit"`
	JoinToken            string   `json:"joinToken" yaml:"joinToken"`
	FirewallRulesetIDs   []string `json:"firewallRulesetIDs,omitempty" yaml:"firewallRulesetIDs,omitempty"`
	KubeconfigRemotePath string   `json:"kubeconfigRemotePath,omitempty" yaml:"kubeconfigRemotePath,omitempty"`
}

type BootstrapCmd struct {
	APIFlags
	Config       string        `name:"config" help:"Path to YAML config."`
	Region       string        `name:"region" help:"Lambda Cloud region."`
	InstanceType string        `name:"instance-type" help:"Instance type."`
	ImageFamily  string        `name:"image-family" help:"Image family."`
	SSHKey       string        `name:"ssh-key" help:"Lambda SSH key name."`
	SSHKeyPath   string        `name:"ssh-key-path" help:"Path to local SSH private key."`
	SSHUser      string        `name:"ssh-user" help:"SSH username (default ubuntu)."`
	CloudInit    string        `name:"cloud-init" help:"Path to cloud-init template."`
	JoinToken    string        `name:"join-token" help:"Cluster join token."`
	ClusterName  string        `name:"cluster-name" help:"Cluster name."`
	FirewallIDs  []string      `name:"firewall-id" help:"Firewall ruleset ID to attach (repeatable)."`
	ClusterDir   string        `name:"cluster-dir" help:"Output directory for cluster.yaml and kubeconfig (default configs/<cluster-name>/)."`
	Timeout      time.Duration `name:"timeout" default:"30m" help:"Overall timeout."`
}

func (c *BootstrapCmd) Run() error {
	var cfg BootstrapConfig
	var configDir string // directory containing the config file, for relative path resolution
	if c.Config != "" {
		data, err := os.ReadFile(c.Config)
		fatalIf(err)
		fatalIf(yaml.Unmarshal(data, &cfg))
		configDir = filepath.Dir(c.Config)
	}

	// CLI flags override config values.
	applyStringOverride(&cfg.ClusterName, c.ClusterName)
	applyStringOverride(&cfg.Region, c.Region)
	applyStringOverride(&cfg.InstanceType, c.InstanceType)
	applyStringOverride(&cfg.ImageFamily, c.ImageFamily)
	applyStringOverride(&cfg.SSHKeyName, c.SSHKey)
	applyStringOverride(&cfg.SSHKeyPath, c.SSHKeyPath)
	applyStringOverride(&cfg.SSHUser, c.SSHUser)
	applyStringOverride(&cfg.CloudInit, c.CloudInit)
	applyStringOverride(&cfg.JoinToken, c.JoinToken)
	if len(c.FirewallIDs) > 0 {
		cfg.FirewallRulesetIDs = append([]string(nil), c.FirewallIDs...)
	}

	// Apply defaults.
	if cfg.SSHUser == "" {
		cfg.SSHUser = "ubuntu"
	}

	// Validate required fields.
	if cfg.ClusterName == "" {
		fatalf("cluster-name is required")
	}
	if cfg.Region == "" {
		fatalf("region is required")
	}
	if cfg.InstanceType == "" {
		fatalf("instance-type is required")
	}
	if cfg.ImageFamily == "" {
		fatalf("image-family is required")
	}
	if cfg.SSHKeyName == "" {
		fatalf("ssh-key is required")
	}
	if cfg.CloudInit == "" {
		fatalf("cloud-init is required")
	}
	if cfg.JoinToken == "" {
		fatalf("join-token is required")
	}
	if cfg.KubeconfigRemotePath == "" {
		cfg.KubeconfigRemotePath = defaultKubeconfigRemotePath
	}

	// Compute cluster dir.
	clusterDir := c.ClusterDir
	if clusterDir == "" {
		clusterDir = filepath.Join("configs", cfg.ClusterName)
	}

	// Resolve relative paths in config file against the config file's directory.
	if configDir != "" {
		cfg.CloudInit = resolvePath(configDir, cfg.CloudInit)
	}

	// Build partial ClusterConfig for template rendering.
	cc := &ClusterConfig{
		ClusterName: cfg.ClusterName,
		Region:      cfg.Region,
		ImageFamily: cfg.ImageFamily,
		SSHKeyName:  cfg.SSHKeyName,
		JoinToken:   cfg.JoinToken,
	}

	client := c.mustClient()

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	// 1. Render cloud-init template.
	userData, err := renderTemplate(cfg.CloudInit, cc.TemplateData())
	fatalIf(err)

	// 2. Launch instance.
	instanceName := cfg.ClusterName + "-controller"
	fmt.Printf("launching %s (%s in %s)...\n", instanceName, cfg.InstanceType, cfg.Region)
	launchReq := lambdaclient.LaunchRequest{
		Name:             instanceName,
		Hostname:         instanceName,
		RegionName:       cfg.Region,
		InstanceTypeName: cfg.InstanceType,
		UserData:         string(userData),
		SSHKeyNames:      []string{cfg.SSHKeyName},
		Image:            &lambdaclient.ImageSpec{Family: cfg.ImageFamily},
		Tags: []lambdaclient.TagEntry{
			{Key: "cluster", Value: cfg.ClusterName},
			{Key: "role", Value: "controller"},
		},
	}
	for _, id := range cfg.FirewallRulesetIDs {
		launchReq.FirewallRulesets = append(launchReq.FirewallRulesets, lambdaclient.FirewallRulesetEntry{ID: id})
	}
	ids, err := client.LaunchInstance(ctx, launchReq)
	fatalIf(err)
	if len(ids) == 0 {
		fatalf("no instance ID returned from launch")
	}
	instanceID := ids[0]
	fmt.Printf("launched instance: %s\n", instanceID)

	// 3. Poll until active + has IP. Tolerate transient API errors.
	fmt.Println("waiting for instance to become active...")
	var publicIP string
	pollStart := time.Now()
	var apiErrors int
	for i := 0; ; i++ {
		inst, err := client.GetInstance(ctx, instanceID)
		if err != nil {
			apiErrors++
			fmt.Fprintf(os.Stderr, "  API error (%d/10): %v\n", apiErrors, err)
			if apiErrors >= 10 {
				fatalf("giving up after %d consecutive API errors", apiErrors)
			}
		} else {
			apiErrors = 0
			if inst.Status == "active" && inst.IP != "" {
				publicIP = inst.IP
				break
			}
			if inst.Status == "terminated" || inst.Status == "error" {
				fatalf("instance %s entered %s state", instanceID, inst.Status)
			}
			if i > 0 && i%6 == 0 {
				fmt.Printf("  status=%s (%s)\n", inst.Status, time.Since(pollStart).Round(time.Second))
			}
		}
		select {
		case <-ctx.Done():
			fatalf("timed out waiting for instance to become active (%s)", time.Since(pollStart).Round(time.Second))
		case <-time.After(5 * time.Second):
		}
	}
	fmt.Printf("instance active at %s\n", publicIP)

	// Write cluster.yaml early so it survives SSH failures or Ctrl+C.
	// We'll update it with internalIP and kubeconfig path once those are known.
	cc.Controller = ClusterController{
		InstanceID:   instanceID,
		InstanceType: cfg.InstanceType,
		PublicIP:     publicIP,
	}
	cc.KubeconfigRemotePath = cfg.KubeconfigRemotePath
	fatalIf(os.MkdirAll(clusterDir, 0755))
	fatalIf(writeClusterConfig(clusterDir, cc))
	fmt.Printf("cluster config written to %s/cluster.yaml\n", clusterDir)

	// 4. SSH -> gather kubeconfig + internalIP -> update cluster.yaml.
	fatalIf(gatherClusterInfo(ctx, cc, clusterDir, cfg.SSHUser, cfg.SSHKeyPath))

	// 5. Write Helm values file for the user.
	valuesPath := filepath.Join(clusterDir, "lambda-karpenter-values.yaml")
	fatalIf(writeHelmValues(valuesPath, cfg, cc))
	fmt.Printf("helm values written to %s\n", valuesPath)

	fmt.Println()
	fmt.Println("next steps:")
	fmt.Printf("  helm install lambda-karpenter charts/lambda-karpenter \\\n")
	fmt.Printf("    -n karpenter --create-namespace \\\n")
	fmt.Printf("    -f %s \\\n", valuesPath)
	fmt.Printf("    --set secret.create=true --set secret.token=$LAMBDA_API_TOKEN\n")

	return nil
}

// writeHelmValues generates a lambda-karpenter Helm values file from the
// bootstrap config and cluster config gathered during bootstrap.
func writeHelmValues(path string, cfg BootstrapConfig, cc *ClusterConfig) error {
	vals := map[string]any{
		"config": map[string]any{
			"clusterName": cfg.ClusterName,
		},
		"cluster": map[string]any{
			"type":         cfg.ClusterType,
			"controllerIP": cc.Controller.InternalIP,
			"joinToken":    cfg.JoinToken,
		},
		"nodeClass": map[string]any{
			"enabled":      true,
			"name":         cfg.ClusterName,
			"region":       cfg.Region,
			"instanceType": cfg.InstanceType,
			"image": map[string]any{
				"family": cfg.ImageFamily,
			},
			"sshKeyNames": []string{cfg.SSHKeyName},
		},
		"nodePool": map[string]any{
			"enabled":       true,
			"name":          cfg.ClusterName,
			"instanceTypes": []string{cfg.InstanceType},
			"limits": map[string]any{
				"nodes": 1,
			},
		},
	}
	data, err := yaml.Marshal(vals)
	if err != nil {
		return fmt.Errorf("marshaling helm values: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// renderTemplate parses path as a Go text/template and executes it with data.
// Shell variables like ${VAR} pass through untouched since Go templates only
// interpret {{.Field}} actions.
func renderTemplate(path string, data any) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New(filepath.Base(path)).Option("missingkey=error").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parsing template %s: %w", path, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("rendering template %s: %w", path, err)
	}
	return buf.Bytes(), nil
}
