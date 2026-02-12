package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DeployCmd struct {
	Kubeconfig         string   `name:"kubeconfig" env:"KUBECONFIG" help:"Path to kubeconfig."`
	Context            string   `name:"context" help:"Kubeconfig context to use."`
	ClusterName        string   `name:"cluster-name" env:"CLUSTER_NAME" help:"Cluster name."`
	ClusterDir         string   `name:"cluster-dir" help:"Path to cluster directory containing cluster.yaml."`
	LambdaAPIToken     string   `name:"lambda-api-token" env:"LAMBDA_API_TOKEN" help:"Lambda API token for the cluster secret."`
	TokenFile          string   `name:"token-file" help:"Path to file containing Lambda API token."`
	GPUValues          string   `name:"gpu-values" help:"Path to GPU operator values file."`
	GPUOperatorVersion string   `name:"gpu-operator-version" env:"GPU_OPERATOR_VERSION" default:"v25.10.1" help:"GPU operator Helm chart version."`
	NodeclassFiles     []string `name:"nodeclass-file" help:"Path to nodeclass YAML (repeatable). Files ending in .tmpl are rendered with cluster.yaml data." sep:"none"`
	NodepoolFiles      []string `name:"nodepool-file" help:"Path to nodepool YAML (repeatable). Files ending in .tmpl are rendered with cluster.yaml data." sep:"none"`
	ImageTag           string   `name:"image-tag" env:"VERSION" default:"latest" help:"lambda-karpenter image tag."`
	ChartPath          string   `name:"chart-path" default:"charts/lambda-karpenter" help:"Path to lambda-karpenter Helm chart."`
	SkipGPUOperator    bool     `name:"skip-gpu-operator" help:"Skip GPU operator installation."`
	DryRun             bool     `name:"dry-run" help:"Print commands instead of executing."`
}

func (c *DeployCmd) Run() error {
	// Load cluster.yaml if --cluster-dir is provided.
	var cc *ClusterConfig
	if c.ClusterDir != "" {
		var err error
		cc, err = readClusterConfig(c.ClusterDir)
		fatalIf(err)

		// Populate fields from cluster.yaml; CLI flags still override.
		if c.ClusterName == "" {
			c.ClusterName = cc.ClusterName
		}
		// Kubeconfig from cluster.yaml takes priority when using --cluster-dir,
		// since env vars from .env.local may point to a stale path.
		if kp := cc.KubeconfigPath(c.ClusterDir); kp != "" {
			c.Kubeconfig = kp
		}
		if c.ImageTag == "" || c.ImageTag == "latest" {
			if cc.Versions.LambdaKarpenter != "" {
				c.ImageTag = cc.Versions.LambdaKarpenter
			}
		}
		if c.GPUOperatorVersion == "" || c.GPUOperatorVersion == "v25.10.1" {
			if cc.Versions.GPUOperator != "" {
				c.GPUOperatorVersion = cc.Versions.GPUOperator
			}
		}

		// Merge nodeclass/nodepool files from cluster.yaml if none given on CLI.
		if len(c.NodeclassFiles) == 0 {
			c.NodeclassFiles = cc.ResolveNodeClassFiles(c.ClusterDir)
		}
		if len(c.NodepoolFiles) == 0 {
			c.NodepoolFiles = cc.ResolveNodePoolFiles(c.ClusterDir)
		}

		// GPU values: CLI flag → cluster.yaml → <cluster-dir>/gpu-operator-values.yaml.
		if c.GPUValues == "" {
			if gp := cc.GPUValuesPath(c.ClusterDir); gp != "" {
				c.GPUValues = gp
			} else {
				candidate := filepath.Join(c.ClusterDir, "gpu-operator-values.yaml")
				if _, err := os.Stat(candidate); err == nil {
					c.GPUValues = candidate
				}
			}
		}
	}

	// Default GPU values fallback (no --cluster-dir case).
	if c.GPUValues == "" {
		c.GPUValues = "configs/gpu-operator-values.yaml"
	}

	// Token resolution: flag → token-file → lambda-api.key → error.
	if c.LambdaAPIToken == "" && c.TokenFile == "" {
		if _, err := os.Stat("lambda-api.key"); err == nil {
			c.TokenFile = "lambda-api.key"
		}
	}
	if c.LambdaAPIToken == "" && c.TokenFile != "" {
		data, err := os.ReadFile(c.TokenFile)
		fatalIf(err)
		c.LambdaAPIToken = strings.TrimSpace(string(data))
	}

	// Validate required fields.
	if c.ClusterName == "" {
		fatalf("cluster-name is required (use --cluster-name, --cluster-dir, or CLUSTER_NAME)")
	}
	if c.LambdaAPIToken == "" {
		fatalf("lambda-api-token is required (use --lambda-api-token, --token-file, or LAMBDA_API_TOKEN)")
	}

	// 1. Create karpenter namespace + lambda-api secret.
	if !c.DryRun {
		cs := mustClientset(c.Kubeconfig, c.Context)
		ctx := context.Background()

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: "karpenter"},
		}
		_, err := cs.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
		if err != nil && !apierrors.IsAlreadyExists(err) {
			fatalIf(err)
		}
		fmt.Println("namespace/karpenter ensured")

		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "lambda-api",
				Namespace: "karpenter",
			},
			StringData: map[string]string{
				"token": c.LambdaAPIToken,
			},
		}
		existing, err := cs.CoreV1().Secrets("karpenter").Get(ctx, "lambda-api", metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = cs.CoreV1().Secrets("karpenter").Create(ctx, secret, metav1.CreateOptions{})
			fatalIf(err)
		} else {
			fatalIf(err)
			existing.StringData = secret.StringData
			_, err = cs.CoreV1().Secrets("karpenter").Update(ctx, existing, metav1.UpdateOptions{})
			fatalIf(err)
		}
		fmt.Println("secret/lambda-api ensured in karpenter")
	} else {
		fmt.Println("[dry-run] would create namespace/karpenter")
		fmt.Println("[dry-run] would create secret/lambda-api in karpenter")
	}

	// 2. GPU operator.
	if !c.SkipGPUOperator {
		if _, err := os.Stat(c.GPUValues); err != nil && !c.DryRun {
			fmt.Fprintf(os.Stderr, "warning: gpu-values file %s not found, skipping GPU operator\n", c.GPUValues)
			fmt.Fprintf(os.Stderr, "  hint: cp examples/gpu-operator-values.yaml %s\n", c.GPUValues)
		} else {
			runHelmTolerant(c.DryRun, []string{"helm", "repo", "add", "nvidia", "https://helm.ngc.nvidia.com/nvidia"})
			runHelm(c.DryRun, []string{"helm", "repo", "update"})

			helmGPU := []string{
				"helm", "upgrade", "--install", "gpu-operator", "nvidia/gpu-operator",
				"--namespace", "gpu-operator", "--create-namespace",
				"--version", c.GPUOperatorVersion,
				"-f", c.GPUValues,
			}
			helmGPU = appendKubeFlags(helmGPU, c.Kubeconfig, c.Context)
			runHelm(c.DryRun, helmGPU)
		}
	}

	// 3. lambda-karpenter.
	helmLK := []string{
		"helm", "upgrade", "--install", "lambda-karpenter", c.ChartPath,
		"--namespace", "karpenter", "--create-namespace",
		"--set", "config.clusterName=" + c.ClusterName,
		"--set", "config.apiTokenSecret.name=lambda-api",
		"--set", "config.apiTokenSecret.key=token",
		"--set", "image.tag=" + c.ImageTag,
	}
	helmLK = appendKubeFlags(helmLK, c.Kubeconfig, c.Context)
	runHelm(c.DryRun, helmLK)

	// 4. Apply nodeclasses + nodepools. Render .tmpl files with cluster.yaml data.
	var applyPaths []string
	var generatedFiles []string

	allFiles := make([]struct {
		path string
		kind string
	}, 0, len(c.NodeclassFiles)+len(c.NodepoolFiles))
	for _, f := range c.NodeclassFiles {
		allFiles = append(allFiles, struct {
			path string
			kind string
		}{f, "nodeclass"})
	}
	for _, f := range c.NodepoolFiles {
		allFiles = append(allFiles, struct {
			path string
			kind string
		}{f, "nodepool"})
	}

	for _, entry := range allFiles {
		f := entry.path
		if _, err := os.Stat(f); err != nil {
			fmt.Fprintf(os.Stderr, "warning: %s file %s not found, skipping\n", entry.kind, f)
			continue
		}

		if strings.HasSuffix(f, ".tmpl") {
			if cc == nil {
				fatalf("--cluster-dir is required to render .tmpl file %s", f)
			}
			rendered, err := renderTemplate(f, cc.TemplateData())
			fatalIf(err)

			// Write to .generated.yaml sibling (gitignored).
			genPath := strings.TrimSuffix(f, ".tmpl") + ".generated.yaml"
			fatalIf(os.WriteFile(genPath, rendered, 0644))
			fmt.Printf("rendered %s → %s\n", f, genPath)
			applyPaths = append(applyPaths, genPath)
			generatedFiles = append(generatedFiles, genPath)
		} else {
			applyPaths = append(applyPaths, f)
		}
	}

	if len(applyPaths) > 0 {
		if c.DryRun {
			for _, p := range applyPaths {
				fmt.Printf("[dry-run] would apply %s\n", p)
			}
		} else {
			dyn := mustDynamicFrom(c.Kubeconfig, c.Context)
			applyObjectsDyn(dyn, applyPaths)
		}
	}

	// Clean up generated files.
	for _, f := range generatedFiles {
		os.Remove(f)
	}

	fmt.Println("deploy complete")
	return nil
}

func appendKubeFlags(args []string, kubeconfig, context string) []string {
	if kubeconfig != "" {
		args = append(args, "--kubeconfig", kubeconfig)
	}
	if context != "" {
		args = append(args, "--kube-context", context)
	}
	return args
}

func runHelm(dryRun bool, args []string) {
	if dryRun {
		fmt.Printf("[dry-run] %s\n", formatCmd(args))
		return
	}
	fmt.Printf("running: %s\n", formatCmd(args))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fatalf("%s failed: %v", args[0], err)
	}
}

func runHelmTolerant(dryRun bool, args []string) {
	if dryRun {
		fmt.Printf("[dry-run] %s\n", formatCmd(args))
		return
	}
	fmt.Printf("running: %s\n", formatCmd(args))
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: %s: %v (continuing)\n", formatCmd(args), err)
	}
}

func formatCmd(args []string) string {
	return strings.Join(args, " ")
}
