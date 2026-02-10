package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer/yaml"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/evecallicoat/lambda-karpenter/api/v1alpha1"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

type k8sOptions struct {
	kubeconfig string
	context    string
	namespace  string
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

func mustK8sClient(opts k8sOptions) client.Client {
	cfg, err := k8sConfig(opts.kubeconfig, opts.context)
	fatalIf(err)
	scheme := runtime.NewScheme()
	fatalIf(v1.AddToScheme(scheme))
	fatalIf(v1alpha1.AddToScheme(scheme))
	return loMust(client.New(cfg, client.Options{Scheme: scheme}))
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
			res := dyn.Resource(gvr)
			name := obj.GetName()
			ns := obj.GetNamespace()
			if ns != "" {
				res = res.Namespace(ns)
			}
			_, err := res.Get(ctx, name, v1.GetOptions{})
			if errors.IsNotFound(err) {
				_, err = res.Create(ctx, obj, v1.CreateOptions{})
				fatalIf(err)
				fmt.Printf("created %s/%s\n", obj.GetKind(), name)
				continue
			}
			fatalIf(err)
			_, err = res.Update(ctx, obj, v1.UpdateOptions{})
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
	if err := res.Delete(ctx, name, v1.DeleteOptions{}); err != nil {
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
	c := mustK8sClient(opts)
	ctx := context.Background()

	var lncList v1alpha1.LambdaNodeClassList
	if err := c.List(ctx, &lncList); err == nil {
		for _, lnc := range lncList.Items {
			fmt.Printf("LambdaNodeClass/%s\n", lnc.Name)
		}
	}

	var npList v1.NodePoolList
	if err := c.List(ctx, &npList); err == nil {
		for _, np := range npList.Items {
			fmt.Printf("NodePool/%s\n", np.Name)
		}
	}

	var ncList v1.NodeClaimList
	if err := c.List(ctx, &ncList); err == nil {
		for _, nc := range ncList.Items {
			ready := nc.StatusConditions().Get(v1.ConditionTypeInitialized).IsTrue()
			fmt.Printf("NodeClaim/%s providerID=%s ready=%t\n", nc.Name, nc.Status.ProviderID, ready)
		}
	}
}

func listNodeClaims(opts k8sOptions) {
	c := mustK8sClient(opts)
	ctx := context.Background()
	var ncList v1.NodeClaimList
	fatalIf(c.List(ctx, &ncList))
	for _, nc := range ncList.Items {
		fmt.Printf("%s\t%s\tlaunched=%t\tregistered=%t\tinitialized=%t\n",
			nc.Name,
			nc.Status.ProviderID,
			nc.StatusConditions().Get(v1.ConditionTypeLaunched).IsTrue(),
			nc.StatusConditions().Get(v1.ConditionTypeRegistered).IsTrue(),
			nc.StatusConditions().Get(v1.ConditionTypeInitialized).IsTrue(),
		)
	}
}

func waitNodeClaimReady(opts k8sOptions, name string, timeout time.Duration) {
	c := mustK8sClient(opts)
	ctx := context.Background()

	err := wait.PollUntilContextTimeout(ctx, 2*time.Second, timeout, true, func(ctx context.Context) (bool, error) {
		var nc v1.NodeClaim
		if err := c.Get(ctx, types.NamespacedName{Name: name}, &nc); err != nil {
			return false, err
		}
		return nc.StatusConditions().Get(v1.ConditionTypeInitialized).IsTrue(), nil
	})
	fatalIf(err)
	fmt.Printf("nodeclaim %s is ready\n", name)
}
