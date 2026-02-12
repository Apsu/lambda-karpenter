package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/util/wait"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
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
	Deploy     DeployCmd     `cmd:"" help:"Install GPU operator + lambda-karpenter + apply resources."`
	Apply      ApplyCmd      `cmd:"" help:"Server-side apply resources."`
	Delete     DeleteCmd     `cmd:"" help:"Delete resources."`
	Status     StatusCmd     `cmd:"" help:"Show LambdaNodeClass, NodePool, NodeClaim status."`
	Nodeclaims NodeclaimsCmd `cmd:"" help:"List NodeClaim details."`
	Wait       WaitCmd       `cmd:"" help:"Wait for NodeClaim to be ready."`
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

// --- Resource commands ---

type ApplyCmd struct {
	K8sFlags
	NodeClass []string `name:"nodeclass" help:"Path to LambdaNodeClass YAML (repeatable)." sep:"none"`
	NodePool  []string `name:"nodepool" help:"Path to NodePool YAML (repeatable)." sep:"none"`
	Pod       string   `name:"pod" help:"Path to Pod YAML."`
}

func (c *ApplyCmd) Run() error {
	c.resolveKubeconfig()
	var paths []string
	paths = append(paths, c.NodeClass...)
	paths = append(paths, c.NodePool...)
	if c.Pod != "" {
		paths = append(paths, c.Pod)
	}
	if len(paths) == 0 {
		fatalf("at least one of --nodeclass, --nodepool, or --pod is required")
	}
	applyObjects(c.K8sFlags, paths)
	return nil
}

type DeleteCmd struct {
	K8sFlags
	NodeClass string `name:"nodeclass" help:"LambdaNodeClass name."`
	NodePool  string `name:"nodepool" help:"NodePool name."`
	NodeClaim string `name:"nodeclaim" help:"NodeClaim name."`
}

func (c *DeleteCmd) Run() error {
	c.resolveKubeconfig()
	if c.NodeClass == "" && c.NodePool == "" && c.NodeClaim == "" {
		fatalf("at least one of --nodeclass, --nodepool, or --nodeclaim is required")
	}
	if c.NodeClaim != "" {
		deleteByName(c.K8sFlags, "NodeClaim", c.NodeClaim)
	}
	if c.NodePool != "" {
		deleteByName(c.K8sFlags, "NodePool", c.NodePool)
	}
	if c.NodeClass != "" {
		deleteByName(c.K8sFlags, "LambdaNodeClass", c.NodeClass)
	}
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

type NodeclaimsCmd struct {
	K8sFlags
}

func (c *NodeclaimsCmd) Run() error {
	c.resolveKubeconfig()
	listNodeClaims(c.K8sFlags)
	return nil
}

type WaitCmd struct {
	K8sFlags
	NodeClaim string        `name:"nodeclaim" required:"" help:"NodeClaim name."`
	Timeout   time.Duration `name:"timeout" default:"10m" help:"Timeout."`
}

func (c *WaitCmd) Run() error {
	c.resolveKubeconfig()
	waitNodeClaimReady(c.K8sFlags, c.NodeClaim, c.Timeout)
	return nil
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

func mustDynamicFrom(kubeconfig, contextName string) dynamic.Interface {
	cfg, err := k8sConfig(kubeconfig, contextName)
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
	if err != nil {
		fatalIf(err)
	}
	return v
}

func readUnstructured(path string) ([]*unstructured.Unstructured, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	decoder := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	yamlReader := utilyaml.NewYAMLOrJSONDecoder(f, 4096)
	var objs []*unstructured.Unstructured
	for {
		var raw json.RawMessage
		if err := yamlReader.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if len(raw) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{}
		if _, _, err := decoder.Decode(raw, nil, obj); err != nil {
			return nil, err
		}
		objs = append(objs, obj)
	}
	return objs, nil
}

func applyObjects(flags K8sFlags, paths []string) {
	dyn := mustDynamic(flags)
	ctx := context.Background()

	for _, path := range paths {
		objs, err := readUnstructured(path)
		fatalIf(err)
		for _, obj := range objs {
			gvr := gvrForGVK(obj.GroupVersionKind())
			if gvr.Resource == "" {
				fatalf("unsupported kind %s", obj.GetKind())
			}
			var res dynamic.ResourceInterface
			ns := obj.GetNamespace()
			if ns != "" {
				res = dyn.Resource(gvr).Namespace(ns)
			} else {
				res = dyn.Resource(gvr)
			}
			name := obj.GetName()
			// Clear resourceVersion to avoid conflicts with server-side apply.
			obj.SetResourceVersion("")
			obj.SetManagedFields(nil)
			_, err = res.Apply(ctx, name, obj, metav1.ApplyOptions{
				FieldManager: "lambdactl",
				Force:        true,
			})
			fatalIf(err)
			fmt.Printf("applied %s/%s\n", obj.GetKind(), name)
		}
	}
}

func applyObjectsDyn(dyn dynamic.Interface, paths []string) {
	ctx := context.Background()
	for _, path := range paths {
		objs, err := readUnstructured(path)
		fatalIf(err)
		for _, obj := range objs {
			gvr := gvrForGVK(obj.GroupVersionKind())
			if gvr.Resource == "" {
				fatalf("unsupported kind %s", obj.GetKind())
			}
			var res dynamic.ResourceInterface
			if ns := obj.GetNamespace(); ns != "" {
				res = dyn.Resource(gvr).Namespace(ns)
			} else {
				res = dyn.Resource(gvr)
			}
			name := obj.GetName()
			obj.SetResourceVersion("")
			obj.SetManagedFields(nil)
			_, err = res.Apply(ctx, name, obj, metav1.ApplyOptions{
				FieldManager: "lambdactl",
				Force:        true,
			})
			fatalIf(err)
			fmt.Printf("applied %s/%s\n", obj.GetKind(), name)
		}
	}
}

func deleteByName(flags K8sFlags, kind string, name string) {
	dyn := mustDynamic(flags)
	ctx := context.Background()
	gvr := gvrForKind(kind)
	if gvr.Resource == "" {
		fatalf("unsupported kind %s", kind)
	}
	res := dyn.Resource(gvr)
	if err := res.Delete(ctx, name, metav1.DeleteOptions{}); err != nil {
		fatalIf(err)
	}
	fmt.Printf("deleted %s/%s\n", kind, name)
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

func gvrForGVK(gvk schema.GroupVersionKind) schema.GroupVersionResource {
	switch gvk.Kind {
	case "LambdaNodeClass":
		return gvrForKind("LambdaNodeClass")
	case "NodePool":
		return gvrForKind("NodePool")
	case "NodeClaim":
		return gvrForKind("NodeClaim")
	case "Namespace":
		return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "namespaces"}
	case "Secret":
		return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "secrets"}
	case "ServiceAccount":
		return schema.GroupVersionResource{Group: "", Version: "v1", Resource: "serviceaccounts"}
	case "ClusterRoleBinding":
		return schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}
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

func listNodeClaims(flags K8sFlags) {
	dyn := mustDynamic(flags)
	ctx := context.Background()
	items := listByKind(ctx, dyn, "NodeClaim")
	if len(items) == 0 {
		fmt.Println("no NodeClaims found")
		return
	}
	for _, item := range items {
		providerID, _, _ := unstructured.NestedString(item.Object, "status", "providerID")
		fmt.Printf("%s\t%s\tlaunched=%t\tregistered=%t\tinitialized=%t\n",
			item.GetName(),
			providerID,
			hasCondition(item, "Launched"),
			hasCondition(item, "Registered"),
			hasCondition(item, "Initialized"),
		)
	}
}

func waitNodeClaimReady(flags K8sFlags, name string, timeout time.Duration) {
	dyn := mustDynamic(flags)
	ctx := context.Background()
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
