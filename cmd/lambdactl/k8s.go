package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"flag"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
	"k8s.io/client-go/util/homedir"
)

type k8sOptions struct {
	kubeconfig string
	context    string
	namespace  string
}

func handleK8s(args []string) {
	if len(args) < 1 {
		k8sUsage()
		os.Exit(2)
	}
	sub := args[0]
	rest := args[1:]

	// New lifecycle commands — handled in their own files.
	switch sub {
	case "bootstrap":
		cmdBootstrap(rest)
		return
	case "kubeconfig":
		cmdKubeconfig(rest)
		return
	case "user":
		handleUser(rest)
		return
	case "deploy":
		cmdDeploy(rest)
		return
	}

	// Legacy k8s resource commands use shared k8sOptions.
	switch sub {
	case "apply":
		fs := flag.NewFlagSet("k8s apply", flag.ExitOnError)
		opts := bindK8sFlags(fs)
		nodeClass := fs.String("nodeclass", "", "Path to LambdaNodeClass YAML")
		nodePool := fs.String("nodepool", "", "Path to NodePool YAML")
		pod := fs.String("pod", "", "Path to Pod YAML (optional)")
		_ = fs.Parse(rest)
		var paths []string
		if *nodeClass != "" {
			paths = append(paths, *nodeClass)
		}
		if *nodePool != "" {
			paths = append(paths, *nodePool)
		}
		if *pod != "" {
			paths = append(paths, *pod)
		}
		if len(paths) == 0 {
			fatalf("at least one of --nodeclass, --nodepool, or --pod is required")
		}
		applyObjects(opts, paths)
	case "delete":
		fs := flag.NewFlagSet("k8s delete", flag.ExitOnError)
		opts := bindK8sFlags(fs)
		nodeClass := fs.String("nodeclass", "", "LambdaNodeClass name")
		nodePool := fs.String("nodepool", "", "NodePool name")
		nodeClaim := fs.String("nodeclaim", "", "NodeClaim name")
		_ = fs.Parse(rest)
		if *nodeClass == "" && *nodePool == "" && *nodeClaim == "" {
			fatalf("at least one of --nodeclass, --nodepool, or --nodeclaim is required")
		}
		if *nodeClaim != "" {
			deleteByName(opts, "NodeClaim", *nodeClaim)
		}
		if *nodePool != "" {
			deleteByName(opts, "NodePool", *nodePool)
		}
		if *nodeClass != "" {
			deleteByName(opts, "LambdaNodeClass", *nodeClass)
		}
	case "status":
		fs := flag.NewFlagSet("k8s status", flag.ExitOnError)
		opts := bindK8sFlags(fs)
		_ = fs.Parse(rest)
		listStatus(opts)
	case "nodeclaims":
		fs := flag.NewFlagSet("k8s nodeclaims", flag.ExitOnError)
		opts := bindK8sFlags(fs)
		_ = fs.Parse(rest)
		listNodeClaims(opts)
	case "wait":
		fs := flag.NewFlagSet("k8s wait", flag.ExitOnError)
		opts := bindK8sFlags(fs)
		nodeClaim := fs.String("nodeclaim", "", "NodeClaim name")
		timeout := fs.Duration("timeout", 10*time.Minute, "Timeout")
		_ = fs.Parse(rest)
		if *nodeClaim == "" {
			fatalf("--nodeclaim is required")
		}
		waitNodeClaimReady(opts, *nodeClaim, *timeout)
	default:
		k8sUsage()
		os.Exit(2)
	}
}

func bindK8sFlags(fs *flag.FlagSet) k8sOptions {
	opts := k8sOptions{}
	fs.StringVar(&opts.kubeconfig, "kubeconfig", os.Getenv("KUBECONFIG"), "Path to kubeconfig")
	fs.StringVar(&opts.context, "context", "", "Kubeconfig context")
	fs.StringVar(&opts.namespace, "namespace", "karpenter", "Namespace (default karpenter)")
	return opts
}

func k8sUsage() {
	fmt.Fprintln(os.Stderr, "Usage: lambdactl k8s <command> [flags]")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Cluster lifecycle:")
	fmt.Fprintln(os.Stderr, "  bootstrap      Launch controller, wait for RKE2, extract kubeconfig")
	fmt.Fprintln(os.Stderr, "  kubeconfig     Extract kubeconfig from existing remote RKE2 node")
	fmt.Fprintln(os.Stderr, "  user           Manage per-user SA + token kubeconfigs (create/rotate/cleanup)")
	fmt.Fprintln(os.Stderr, "  deploy         Install GPU operator + lambda-karpenter + apply resources")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Resource management:")
	fmt.Fprintln(os.Stderr, "  apply          Server-side apply resources (--nodeclass, --nodepool, --pod)")
	fmt.Fprintln(os.Stderr, "  delete         Delete resources (--nodeclass, --nodepool, --nodeclaim)")
	fmt.Fprintln(os.Stderr, "  status         Show LambdaNodeClass, NodePool, NodeClaim status")
	fmt.Fprintln(os.Stderr, "  nodeclaims     List NodeClaim details")
	fmt.Fprintln(os.Stderr, "  wait           Wait for NodeClaim to be ready (--nodeclaim, --timeout)")
}

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

func mustDynamic(opts k8sOptions) dynamic.Interface {
	cfg, err := k8sConfig(opts.kubeconfig, opts.context)
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

func applyObjects(opts k8sOptions, paths []string) {
	dyn := mustDynamic(opts)
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

func deleteByName(opts k8sOptions, kind string, name string) {
	dyn := mustDynamic(opts)
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

func listStatus(opts k8sOptions) {
	dyn := mustDynamic(opts)
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

func listNodeClaims(opts k8sOptions) {
	dyn := mustDynamic(opts)
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

func waitNodeClaimReady(opts k8sOptions, name string, timeout time.Duration) {
	dyn := mustDynamic(opts)
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
