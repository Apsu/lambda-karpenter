package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DeployCmd struct {
	Kubeconfig         string `name:"kubeconfig" env:"KUBECONFIG" help:"Path to kubeconfig."`
	Context            string `name:"context" help:"Kubeconfig context to use."`
	ClusterName        string `name:"cluster-name" env:"CLUSTER_NAME" required:"" help:"Cluster name."`
	LambdaAPIToken     string `name:"lambda-api-token" env:"LAMBDA_API_TOKEN" required:"" help:"Lambda API token for the cluster secret."`
	GPUValues          string `name:"gpu-values" default:"configs/gpu-operator-values.yaml" help:"Path to GPU operator values file."`
	GPUOperatorVersion string `name:"gpu-operator-version" env:"GPU_OPERATOR_VERSION" default:"v25.10.1" help:"GPU operator Helm chart version."`
	NodeclassFile      string `name:"nodeclass-file" default:"configs/lambdanodeclass.yaml" help:"Path to nodeclass YAML."`
	NodepoolFile       string `name:"nodepool-file" default:"configs/nodepool.yaml" help:"Path to nodepool YAML."`
	ImageTag           string `name:"image-tag" env:"VERSION" default:"latest" help:"lambda-karpenter image tag."`
	ChartPath          string `name:"chart-path" default:"charts/lambda-karpenter" help:"Path to lambda-karpenter Helm chart."`
	SkipGPUOperator    bool   `name:"skip-gpu-operator" help:"Skip GPU operator installation."`
	DryRun             bool   `name:"dry-run" help:"Print commands instead of executing."`
}

func (c *DeployCmd) Run() error {
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

	// 4. Apply nodeclass + nodepool.
	var applyPaths []string
	if c.NodeclassFile != "" {
		if _, err := os.Stat(c.NodeclassFile); err == nil {
			applyPaths = append(applyPaths, c.NodeclassFile)
		} else {
			fmt.Fprintf(os.Stderr, "warning: nodeclass file %s not found, skipping\n", c.NodeclassFile)
			fmt.Fprintf(os.Stderr, "  hint: run 'lambdactl k8s bootstrap' to generate it, or cp examples/lambdanodeclass.yaml %s\n", c.NodeclassFile)
		}
	}
	if c.NodepoolFile != "" {
		if _, err := os.Stat(c.NodepoolFile); err == nil {
			applyPaths = append(applyPaths, c.NodepoolFile)
		} else {
			fmt.Fprintf(os.Stderr, "warning: nodepool file %s not found, skipping\n", c.NodepoolFile)
			fmt.Fprintf(os.Stderr, "  hint: cp examples/nodepool.yaml %s\n", c.NodepoolFile)
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
	s := ""
	for i, a := range args {
		if i > 0 {
			s += " "
		}
		s += a
	}
	return s
}
