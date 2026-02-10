package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"flag"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

type k8sOptions struct {
	kubeconfig string
	context    string
	namespace  string
}

func handleK8s(args []string) {
	fs := flag.NewFlagSet("k8s", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	opts := bindK8sFlags(fs)
	if err := fs.Parse(args); err != nil {
		k8sUsage()
		os.Exit(2)
	}
	rest := fs.Args()
	if len(rest) < 1 {
		k8sUsage()
		os.Exit(2)
	}
	sub := rest[0]
	switch sub {
	case "apply":
		fs := flag.NewFlagSet("k8s apply", flag.ExitOnError)
		opts = bindK8sFlags(fs)
		nodeClass := fs.String("nodeclass", "", "Path to LambdaNodeClass YAML")
		nodePool := fs.String("nodepool", "", "Path to NodePool YAML")
		pod := fs.String("pod", "", "Path to Pod YAML (optional)")
		_ = fs.Parse(rest[1:])
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
		opts = bindK8sFlags(fs)
		nodeClass := fs.String("nodeclass", "", "LambdaNodeClass name")
		nodePool := fs.String("nodepool", "", "NodePool name")
		nodeClaim := fs.String("nodeclaim", "", "NodeClaim name")
		_ = fs.Parse(rest[1:])
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
		opts = bindK8sFlags(fs)
		_ = fs.Parse(rest[1:])
		listStatus(opts)
	case "nodeclaims":
		fs := flag.NewFlagSet("k8s nodeclaims", flag.ExitOnError)
		opts = bindK8sFlags(fs)
		_ = fs.Parse(rest[1:])
		listNodeClaims(opts)
	case "wait":
		fs := flag.NewFlagSet("k8s wait", flag.ExitOnError)
		opts = bindK8sFlags(fs)
		nodeClaim := fs.String("nodeclaim", "", "NodeClaim name")
		timeout := fs.Duration("timeout", 10*time.Minute, "Timeout")
		_ = fs.Parse(rest[1:])
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
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  apply --nodeclass <file> [--nodepool <file>] [--pod <file>]")
	fmt.Fprintln(os.Stderr, "  delete --nodeclass <name> [--nodepool <name>] [--nodeclaim <name>]")
	fmt.Fprintln(os.Stderr, "  status")
	fmt.Fprintln(os.Stderr, "  nodeclaims")
	fmt.Fprintln(os.Stderr, "  wait --nodeclaim <name> [--timeout 10m]")
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

func loMust[T any](v T, err error) T {
	if err != nil {
		fatalIf(err)
	}
	return v
}

func readUnstructured(path string) ([]*unstructured.Unstructured, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecodingSerializer(unstructured.UnstructuredJSONScheme)
	var objs []*unstructured.Unstructured
	for _, doc := range splitYAML(data) {
		if len(doc) == 0 {
			continue
		}
		obj := &unstructured.Unstructured{}
		_, _, err := decoder.Decode(doc, nil, obj)
		if err != nil {
			return nil, err
		}
		objs = append(objs, obj)
	}
	return objs, nil
}

func splitYAML(data []byte) [][]byte {
	var docs [][]byte
	start := 0
	for i := 0; i+3 <= len(data); i++ {
		if string(data[i:i+3]) == "---" {
			docs = append(docs, data[start:i])
			start = i + 3
		}
	}
	docs = append(docs, data[start:])
	return docs
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
			_, err := res.Get(ctx, name, metav1.GetOptions{})
			if errors.IsNotFound(err) {
				_, err = res.Create(ctx, obj, metav1.CreateOptions{})
				fatalIf(err)
				fmt.Printf("created %s/%s\n", obj.GetKind(), name)
				continue
			}
			fatalIf(err)
			_, err = res.Update(ctx, obj, metav1.UpdateOptions{})
			fatalIf(err)
			fmt.Printf("updated %s/%s\n", obj.GetKind(), name)
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
