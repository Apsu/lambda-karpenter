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
)

// templateData is the context available to all Go templates rendered by bootstrap.
type templateData struct {
	ClusterName  string
	Region       string
	InstanceType string
	ImageFamily  string
	SSHKeyName   string
	RKE2Token    string
	ControllerIP string // internal IP, populated after SSH
}

type BootstrapCmd struct {
	APIFlags
	Region            string        `name:"region" required:"" help:"Lambda Cloud region."`
	InstanceType      string        `name:"instance-type" required:"" help:"Instance type."`
	ImageFamily       string        `name:"image-family" required:"" help:"Image family."`
	SSHKey            string        `name:"ssh-key" required:"" help:"Lambda SSH key name."`
	SSHKeyPath        string        `name:"ssh-key-path" help:"Path to local SSH private key."`
	SSHUser           string        `name:"ssh-user" default:"ubuntu" help:"SSH username."`
	CloudInit         string        `name:"cloud-init" required:"" help:"Path to cloud-init template."`
	RKE2Token         string        `name:"rke2-token" required:"" help:"RKE2 join token."`
	ClusterName       string        `name:"cluster-name" required:"" help:"Cluster name."`
	KubeconfigOut     string        `name:"kubeconfig-out" help:"Output kubeconfig path (default CLUSTER_NAME.kubeconfig)."`
	NodeclassOut      string        `name:"nodeclass-out" default:"configs/lambdanodeclass.yaml" help:"Output nodeclass YAML path."`
	NodeclassTemplate string        `name:"nodeclass-template" help:"Path to nodeclass YAML template."`
	Timeout           time.Duration `name:"timeout" default:"30m" help:"Overall timeout."`
}

func (c *BootstrapCmd) Run() error {
	if c.KubeconfigOut == "" {
		c.KubeconfigOut = c.ClusterName + ".kubeconfig"
	}

	client := c.mustClient()

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	td := templateData{
		ClusterName:  c.ClusterName,
		Region:       c.Region,
		InstanceType: c.InstanceType,
		ImageFamily:  c.ImageFamily,
		SSHKeyName:   c.SSHKey,
		RKE2Token:    c.RKE2Token,
	}

	// 1. Render cloud-init template.
	userData, err := renderTemplate(c.CloudInit, td)
	fatalIf(err)

	// 2. Launch instance.
	instanceName := c.ClusterName + "-controller"
	fmt.Printf("launching %s (%s in %s)...\n", instanceName, c.InstanceType, c.Region)
	ids, err := client.LaunchInstance(ctx, lambdaclient.LaunchRequest{
		Name:             instanceName,
		Hostname:         instanceName,
		RegionName:       c.Region,
		InstanceTypeName: c.InstanceType,
		UserData:         string(userData),
		SSHKeyNames:      []string{c.SSHKey},
		Image:            &lambdaclient.ImageSpec{Family: c.ImageFamily},
		Tags: []lambdaclient.TagEntry{
			{Key: "cluster", Value: c.ClusterName},
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
	sshCfg, err := sshConfig(c.SSHUser, c.SSHKeyPath)
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
	rewriteKubeconfig(kubeCfg, publicIP, c.ClusterName)

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

	// 8. Write kubeconfig.
	fatalIf(writeKubeconfigFile(c.KubeconfigOut, data))
	fmt.Printf("kubeconfig written to %s\n", c.KubeconfigOut)
	fmt.Printf("export KUBECONFIG=%s\n", c.KubeconfigOut)

	// 9. Generate nodeclass YAML if template provided.
	if c.NodeclassTemplate != "" {
		if internalIP == "" {
			fmt.Fprintln(os.Stderr, "warning: could not determine internal IP; nodeclass not generated")
		} else {
			td.ControllerIP = internalIP
			rendered, err := renderTemplate(c.NodeclassTemplate, td)
			fatalIf(err)
			fatalIf(os.MkdirAll(filepath.Dir(c.NodeclassOut), 0755))
			fatalIf(os.WriteFile(c.NodeclassOut, rendered, 0644))
			fmt.Printf("nodeclass written to %s\n", c.NodeclassOut)
		}
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
