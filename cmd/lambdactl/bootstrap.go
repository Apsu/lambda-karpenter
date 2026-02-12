package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"github.com/evecallicoat/lambda-karpenter/internal/lambdaclient"
	"golang.org/x/crypto/ssh"
	"sigs.k8s.io/yaml"
)

// BootstrapConfig is the YAML config file format for bootstrap.
type BootstrapConfig struct {
	ClusterName    string   `json:"clusterName" yaml:"clusterName"`
	Region         string   `json:"region" yaml:"region"`
	InstanceType   string   `json:"instanceType" yaml:"instanceType"`
	ImageFamily    string   `json:"imageFamily" yaml:"imageFamily"`
	SSHKeyName     string   `json:"sshKeyName" yaml:"sshKeyName"`
	SSHKeyPath     string   `json:"sshKeyPath" yaml:"sshKeyPath"`
	SSHUser        string   `json:"sshUser" yaml:"sshUser"`
	CloudInit      string   `json:"cloudInit" yaml:"cloudInit"`
	JoinToken      string   `json:"joinToken" yaml:"joinToken"`
	NodeClassFiles []string `json:"nodeClassFiles,omitempty" yaml:"nodeClassFiles,omitempty"`
	NodePoolFiles  []string `json:"nodePoolFiles,omitempty" yaml:"nodePoolFiles,omitempty"`
	GPUValues      string   `json:"gpuValues,omitempty" yaml:"gpuValues,omitempty"`
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

	// Compute cluster dir.
	clusterDir := c.ClusterDir
	if clusterDir == "" {
		clusterDir = filepath.Join("configs", cfg.ClusterName)
	}

	// Resolve relative paths in config file against the config file's directory.
	if configDir != "" {
		cfg.CloudInit = resolvePath(configDir, cfg.CloudInit)
		cfg.NodeClassFiles = resolvePaths(configDir, cfg.NodeClassFiles)
		cfg.NodePoolFiles = resolvePaths(configDir, cfg.NodePoolFiles)
		cfg.GPUValues = resolvePath(configDir, cfg.GPUValues)
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
	ids, err := client.LaunchInstance(ctx, lambdaclient.LaunchRequest{
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
	})
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

	// 4. SSH -> wait for RKE2 kubeconfig -> download.
	//    If the connection drops (e.g. host reboots during RKE2 install),
	//    reconnect and resume waiting.
	sshCfg, err := sshConfig(cfg.SSHUser, cfg.SSHKeyPath)
	fatalIf(err)

	var sshClient *ssh.Client
	var raw []byte
	for reconnects := 0; ; reconnects++ {
		if reconnects > 0 {
			sshClient.Close()
			fmt.Fprintln(os.Stderr, "SSH connection lost, reconnecting...")
		}

		fmt.Printf("waiting for SSH on %s...\n", publicIP)
		sshClient, err = waitSSH(ctx, publicIP, 22, sshCfg, 5*time.Second)
		fatalIf(err)

		fmt.Printf("waiting for %s...\n", rke2KubeconfigPath)
		err = waitRemoteFile(ctx, sshClient, rke2KubeconfigPath, 5*time.Second)
		if isSSHConnectionError(err) {
			continue
		}
		fatalIf(err)

		fmt.Println("downloading kubeconfig...")
		raw, err = sshDownload(sshClient, rke2KubeconfigPath)
		if isSSHConnectionError(err) {
			continue
		}
		fatalIf(err)
		break
	}
	defer sshClient.Close()

	// 5. Parse and rewrite kubeconfig.
	kubeCfg, err := parseKubeconfig(raw)
	fatalIf(err)
	rewriteKubeconfig(kubeCfg, publicIP, cfg.ClusterName)

	data, err := serializeKubeconfig(kubeCfg)
	fatalIf(err)

	// 6. Wait for API readiness.
	fmt.Println("waiting for Kubernetes API...")
	restCfg, err := restConfigFromKubeconfig(kubeCfg)
	fatalIf(err)
	fatalIf(waitAPIReady(ctx, restCfg, 5*time.Second))

	// 7. Detect internal IP.
	internalIP, err := sshRun(sshClient, "hostname -I | awk '{print $1}'")
	fatalIf(err)
	internalIP = strings.TrimSpace(internalIP)

	// 8. Write kubeconfig to cluster dir.
	kubeconfigPath := filepath.Join(clusterDir, "kubeconfig")
	fatalIf(os.MkdirAll(clusterDir, 0755))
	fatalIf(writeKubeconfigFile(kubeconfigPath, data))
	fmt.Printf("kubeconfig written to %s\n", kubeconfigPath)
	fmt.Printf("export KUBECONFIG=%s\n", kubeconfigPath)

	// 9. Write cluster.yaml with all discovered facts.
	cc.Controller = ClusterController{
		InstanceID:   instanceID,
		InstanceType: cfg.InstanceType,
		InternalIP:   internalIP,
		PublicIP:     publicIP,
	}
	cc.Versions = ClusterVersions{
		GPUOperator:     os.Getenv("GPU_OPERATOR_VERSION"),
		LambdaKarpenter: os.Getenv("VERSION"),
	}
	cc.Kubeconfig = "kubeconfig"

	// Store file paths relative to the cluster dir.
	for _, f := range cfg.NodeClassFiles {
		cc.NodeClassFiles = append(cc.NodeClassFiles, relPath(clusterDir, f))
	}
	for _, f := range cfg.NodePoolFiles {
		cc.NodePoolFiles = append(cc.NodePoolFiles, relPath(clusterDir, f))
	}
	if cfg.GPUValues != "" {
		cc.GPUValues = relPath(clusterDir, cfg.GPUValues)
	}

	fatalIf(writeClusterConfig(clusterDir, cc))
	fmt.Printf("cluster config written to %s/cluster.yaml\n", clusterDir)
	fmt.Println()
	fmt.Println("next steps:")
	fmt.Printf("  lambdactl k8s deploy --cluster-dir %s\n", clusterDir)

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
