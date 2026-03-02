package main

import (
	"context"
	"fmt"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/homedir"
)

// K8sCmd is the parent for all kubernetes subcommands.
type K8sCmd struct {
	Bootstrap  BootstrapCmd  `cmd:"" help:"Launch controller, wait for RKE2, extract kubeconfig."`
	Kubeconfig KubeconfigCmd `cmd:"" help:"Extract kubeconfig from existing remote RKE2 node."`
	Gather     GatherCmd     `cmd:"" help:"SSH into controller and populate missing cluster.yaml fields (kubeconfig, internalIP)."`
	User       UserCmd       `cmd:"" help:"Manage per-user SA + token kubeconfigs."`
	Status    StatusCmd    `cmd:"" help:"Show LambdaNodeClass, NodePool, NodeClaim status."`
	NodeClaim NodeClaimCmd `cmd:"" name:"nodeclaim" help:"Show or wait for NodeClaims."`
}

// K8sFlags are shared flags for k8s resource commands.
type K8sFlags struct {
	ClusterDir string `name:"cluster-dir" help:"Path to cluster directory (resolves kubeconfig from cluster.yaml)."`
	Kubeconfig string `name:"kubeconfig" env:"KUBECONFIG" help:"Path to kubeconfig."`
	Context    string `name:"context" help:"Kubeconfig context."`
	Namespace  string `name:"namespace" default:"karpenter" help:"Namespace."`
}

// resolveKubeconfig sets Kubeconfig from cluster.yaml when --kubeconfig is not
// explicitly provided and a cluster directory is available.
func (f *K8sFlags) resolveKubeconfig() {
	if f.Kubeconfig != "" {
		return
	}
	dir := findClusterDir(f.ClusterDir)
	if dir == "" {
		return
	}
	cc, err := readClusterConfig(dir)
	if err != nil {
		return
	}
	if kp := cc.KubeconfigPath(dir); kp != "" {
		f.Kubeconfig = kp
	}
}

// GatherCmd SSHes into the controller and populates missing cluster.yaml
// fields (kubeconfig, internalIP). Standalone recovery after partial bootstrap.
type GatherCmd struct {
	ClusterDir string        `name:"cluster-dir" help:"Path to cluster directory."`
	SSHUser    string        `name:"ssh-user" default:"ubuntu" help:"SSH username."`
	SSHKeyPath string        `name:"ssh-key-path" help:"Path to SSH private key."`
	Timeout    time.Duration `name:"timeout" default:"10m" help:"Timeout."`
}

func (c *GatherCmd) Run() error {
	dir := findClusterDir(c.ClusterDir)
	if dir == "" {
		fatalf("no cluster.yaml found; specify --cluster-dir")
	}
	cc, err := readClusterConfig(dir)
	fatalIf(err)

	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()

	fatalIf(gatherClusterInfo(ctx, cc, dir, c.SSHUser, c.SSHKeyPath))
	return nil
}

type StatusCmd struct {
	K8sFlags
}

func (c *StatusCmd) Run() error {
	c.resolveKubeconfig()
	listStatus(c.K8sFlags)
	return nil
}

type NodeClaimCmd struct {
	K8sFlags
	Name    string        `arg:"" optional:"" help:"NodeClaim name."`
	Wait    bool          `name:"wait" short:"w" help:"Wait for NodeClaim(s) to be initialized."`
	Timeout time.Duration `name:"timeout" default:"10m" help:"Timeout for --wait."`
}

func (c *NodeClaimCmd) Run() error {
	c.resolveKubeconfig()
	dyn := mustDynamic(c.K8sFlags)
	ctx := context.Background()

	if c.Name != "" {
		if c.Wait {
			waitNodeClaimReady(dyn, ctx, c.Name, c.Timeout)
		} else {
			showNodeClaim(dyn, ctx, c.Name)
		}
		return nil
	}

	if c.Wait {
		waitAllNodeClaimsReady(dyn, ctx, c.Timeout)
	} else {
		listNodeClaims(dyn, ctx)
	}
	return nil
}

func showNodeClaim(dyn dynamic.Interface, ctx context.Context, name string) {
	item, err := dyn.Resource(gvrForKind("NodeClaim")).Get(ctx, name, metav1.GetOptions{})
	fatalIf(err)
	providerID, _, _ := unstructured.NestedString(item.Object, "status", "providerID")
	fmt.Printf("NodeClaim/%s providerID=%s launched=%t registered=%t initialized=%t\n",
		item.GetName(), providerID,
		hasCondition(item, "Launched"),
		hasCondition(item, "Registered"),
		hasCondition(item, "Initialized"),
	)
}

func listNodeClaims(dyn dynamic.Interface, ctx context.Context) {
	nodeClaims := listByKind(ctx, dyn, "NodeClaim")
	if len(nodeClaims) == 0 {
		fmt.Println("No NodeClaims found.")
		return
	}
	for _, item := range nodeClaims {
		providerID, _, _ := unstructured.NestedString(item.Object, "status", "providerID")
		fmt.Printf("NodeClaim/%s providerID=%s launched=%t registered=%t initialized=%t\n",
			item.GetName(), providerID,
			hasCondition(item, "Launched"),
			hasCondition(item, "Registered"),
			hasCondition(item, "Initialized"),
		)
	}
}

func waitAllNodeClaimsReady(dyn dynamic.Interface, ctx context.Context, timeout time.Duration) {
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		nodeClaims := listByKind(ctx, dyn, "NodeClaim")
		if len(nodeClaims) == 0 {
			return false, nil
		}
		for _, item := range nodeClaims {
			if !hasCondition(item, "Initialized") {
				return false, nil
			}
		}
		return true, nil
	})
	fatalIf(err)
	fmt.Println("all nodeclaims ready")
}

// --- K8s helpers ---

func k8sConfig(kubeconfig, contextName string) (*rest.Config, error) {
	if kubeconfig == "" {
		if home := homedir.HomeDir(); home != "" {
			kubeconfig = home + "/.kube/config"
		}
	}
	overrides := &clientcmd.ConfigOverrides{}
	if contextName != "" {
		overrides.CurrentContext = contextName
	}
	loading := &clientcmd.ClientConfigLoadingRules{ExplicitPath: kubeconfig}
	cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loading, overrides)
	return cfg.ClientConfig()
}

func mustDynamic(flags K8sFlags) dynamic.Interface {
	cfg, err := k8sConfig(flags.Kubeconfig, flags.Context)
	fatalIf(err)
	return loMust(dynamic.NewForConfig(cfg))
}

func mustClientset(kubeconfig, contextName string) *kubernetes.Clientset {
	cfg, err := k8sConfig(kubeconfig, contextName)
	fatalIf(err)
	cs, err := kubernetes.NewForConfig(cfg)
	fatalIf(err)
	return cs
}

func restConfigFromKubeconfig(cfg *clientcmdapi.Config) (*rest.Config, error) {
	return clientcmd.NewDefaultClientConfig(*cfg, &clientcmd.ConfigOverrides{}).ClientConfig()
}

func waitAPIReady(ctx context.Context, cfg *rest.Config, poll time.Duration) error {
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	start := time.Now()
	attempt := 0
	for {
		_, err := cs.Discovery().ServerVersion()
		if err == nil {
			return nil
		}
		attempt++
		if attempt%6 == 0 {
			fmt.Fprintf(os.Stderr, "  still waiting for API (%s)\n", time.Since(start).Round(time.Second))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for Kubernetes API after %s: %w",
				time.Since(start).Round(time.Second), ctx.Err())
		case <-time.After(poll):
		}
	}
}

func loMust[T any](v T, err error) T {
	fatalIf(err)
	return v
}

func gvrForKind(kind string) schema.GroupVersionResource {
	switch kind {
	case "LambdaNodeClass":
		return schema.GroupVersionResource{Group: "karpenter.lambda.cloud", Version: "v1alpha1", Resource: "lambdanodeclasses"}
	case "NodePool":
		return schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	case "NodeClaim":
		return schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodeclaims"}
	default:
		return schema.GroupVersionResource{}
	}
}

func listStatus(flags K8sFlags) {
	dyn := mustDynamic(flags)
	ctx := context.Background()

	nodeClasses := listByKind(ctx, dyn, "LambdaNodeClass")
	if len(nodeClasses) == 0 {
		fmt.Println("LambdaNodeClass: none")
	} else {
		for _, item := range nodeClasses {
			fmt.Printf("LambdaNodeClass/%s ready=%t\n", item.GetName(), hasCondition(item, "Ready"))
		}
	}

	nodePools := listByKind(ctx, dyn, "NodePool")
	if len(nodePools) == 0 {
		fmt.Println("NodePool: none")
	} else {
		for _, item := range nodePools {
			nodes, _, _ := unstructured.NestedInt64(item.Object, "status", "nodes")
			limitsNodes, _, _ := unstructured.NestedInt64(item.Object, "spec", "limits", "nodes")
			replicas, hasReplicas, _ := unstructured.NestedInt64(item.Object, "spec", "replicas")
			replicaStr := "dynamic"
			if hasReplicas {
				replicaStr = fmt.Sprintf("%d", replicas)
			}
			fmt.Printf("NodePool/%s ready=%t nodes=%d limit=%d replicas=%s\n",
				item.GetName(),
				hasCondition(item, "Ready"),
				nodes,
				limitsNodes,
				replicaStr,
			)
		}
	}

	nodeClaims := listByKind(ctx, dyn, "NodeClaim")
	if len(nodeClaims) == 0 {
		fmt.Println("NodeClaim: none")
	} else {
		for _, item := range nodeClaims {
			providerID, _, _ := unstructured.NestedString(item.Object, "status", "providerID")
			fmt.Printf("NodeClaim/%s providerID=%s launched=%t registered=%t initialized=%t\n",
				item.GetName(),
				providerID,
				hasCondition(item, "Launched"),
				hasCondition(item, "Registered"),
				hasCondition(item, "Initialized"),
			)
		}
	}
}

func waitNodeClaimReady(dyn dynamic.Interface, ctx context.Context, name string, timeout time.Duration) {
	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		item, err := dyn.Resource(gvrForKind("NodeClaim")).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return false, err
		}
		return hasCondition(item, "Initialized"), nil
	})
	fatalIf(err)
	fmt.Printf("nodeclaim %s is ready\n", name)
}

func listByKind(ctx context.Context, dyn dynamic.Interface, kind string) []*unstructured.Unstructured {
	gvr := gvrForKind(kind)
	if gvr.Resource == "" {
		return nil
	}
	list, err := dyn.Resource(gvr).List(ctx, metav1.ListOptions{})
	fatalIf(err)
	out := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		item := list.Items[i]
		out = append(out, &item)
	}
	return out
}

func hasCondition(obj *unstructured.Unstructured, condType string) bool {
	conds, found, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if !found {
		return false
	}
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		t, _ := m["type"].(string)
		s, _ := m["status"].(string)
		if t == condType && s == "True" {
			return true
		}
	}
	return false
}
