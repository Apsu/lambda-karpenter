package provider

import (
	"context"
	"testing"

	"github.com/evecallicoat/lambda-karpenter/api/v1alpha1"
	"github.com/evecallicoat/lambda-karpenter/internal/lambdaclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/karpenter/pkg/apis"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

type fakeLambda struct {
	listInstances []lambdaclient.Instance
	instances     map[string]lambdaclient.Instance
	launchReqs    []lambdaclient.LaunchRequest
	launchIDs     []string
	terminated    []string
}

func (f *fakeLambda) ListInstances(ctx context.Context) ([]lambdaclient.Instance, error) {
	return append([]lambdaclient.Instance(nil), f.listInstances...), nil
}

func (f *fakeLambda) GetInstance(ctx context.Context, id string) (*lambdaclient.Instance, error) {
	if inst, ok := f.instances[id]; ok {
		return &inst, nil
	}
	return nil, context.Canceled
}

func (f *fakeLambda) LaunchInstance(ctx context.Context, req lambdaclient.LaunchRequest) ([]string, error) {
	f.launchReqs = append(f.launchReqs, req)
	return append([]string(nil), f.launchIDs...), nil
}

func (f *fakeLambda) TerminateInstance(ctx context.Context, id string) error {
	f.terminated = append(f.terminated, id)
	return nil
}

func TestProviderCreateLaunchRequest(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	gv := schema.GroupVersion{Group: apis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, gv)
	scheme.AddKnownTypes(gv, &v1.NodePool{}, &v1.NodePoolList{}, &v1.NodeClaim{}, &v1.NodeClaimList{})

	class := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: "lambda-gh200",
		},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:        "us-east-3",
			InstanceType:  "gpu_1x_gh200",
			SSHKeyNames:   []string{"Eve"},
			UserData:      "#cloud-config",
			FirewallRulesetIDs: []string{"fw-1"},
			Tags: map[string]string{"env": "test"},
			Image: &v1alpha1.LambdaImage{
				Family: "lambda-stack-24-04",
			},
		},
	}

	nodePool := &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gh200-pool",
		},
		Spec: v1.NodePoolSpec{
			Limits: v1.Limits{
				corev1.ResourceName("nodes"): resource.MustParse("1"),
			},
		},
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, nodePool).Build()
	fakeAPI := &fakeLambda{
		instances: map[string]lambdaclient.Instance{
			"i-1": {ID: "i-1", Hostname: "gh200-pool-abc", Type: lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200"}, Region: lambdaclient.Region{Name: "us-east-3"}},
		},
		launchIDs: []string{"i-1"},
	}

	p := New(client, fakeAPI, nil, "gh200-test1")

	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gh200-pool-abc",
			Labels: map[string]string{
				v1.NodePoolLabelKey: "gh200-pool",
			},
		},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{
				Group: v1alpha1.Group,
				Kind:  "LambdaNodeClass",
				Name:  "lambda-gh200",
			},
		},
	}

	got, err := p.Create(context.Background(), nc)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(fakeAPI.launchReqs) != 1 {
		t.Fatalf("expected launch request, got %d", len(fakeAPI.launchReqs))
	}
	req := fakeAPI.launchReqs[0]
	if req.RegionName != "us-east-3" || req.InstanceTypeName != "gpu_1x_gh200" {
		t.Fatalf("unexpected launch req: %#v", req)
	}
	if len(req.SSHKeyNames) != 1 || req.SSHKeyNames[0] != "Eve" {
		t.Fatalf("unexpected ssh keys: %#v", req.SSHKeyNames)
	}
	if got.Status.ProviderID == "" {
		t.Fatalf("expected providerID")
	}
}

func TestProviderListFiltersCluster(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{
		listInstances: []lambdaclient.Instance{
			{ID: "i-1", Tags: []lambdaclient.TagEntry{{Key: "karpenter-sh-cluster", Value: "gh200-test1"}}},
			{ID: "i-2", Tags: []lambdaclient.TagEntry{{Key: "karpenter-sh-cluster", Value: "other"}}},
		},
	}
	p := New(client, fakeAPI, nil, "gh200-test1")
	items, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 || items[0].Status.ProviderID != "lambda://i-1" {
		t.Fatalf("unexpected list results: %#v", items)
	}
}

func TestProviderResolveInstanceByHostnameProviderID(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{
		listInstances: []lambdaclient.Instance{
			{ID: "i-1", Hostname: "gh200-pool-xyz", Status: "unhealthy"},
			{ID: "i-2", Hostname: "gh200-pool-xyz"},
		},
	}
	p := New(client, fakeAPI, nil, "gh200-test1")

	nc := &v1.NodeClaim{
		Status: v1.NodeClaimStatus{
			ProviderID: "lambda://gh200-pool-xyz",
		},
	}
	inst, err := p.resolveInstanceForNodeClaim(context.Background(), nc)
	if err != nil {
		t.Fatalf("resolveInstanceForNodeClaim: %v", err)
	}
	if inst == nil || inst.ID != "i-2" {
		t.Fatalf("unexpected instance: %#v", inst)
	}
}

func TestProviderNodeClaimFromInstanceUsesHostnameProviderID(t *testing.T) {
	p := &Provider{}
	inst := &lambdaclient.Instance{
		ID:       "i-1",
		Hostname: "gh200-pool-abc",
		Type:     lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200"},
		Region:   lambdaclient.Region{Name: "us-east-3"},
	}
	nc := p.nodeClaimFromInstance(&v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}, inst)
	if nc.Status.ProviderID != "lambda://i-1" {
		t.Fatalf("unexpected providerID: %s", nc.Status.ProviderID)
	}
	if nc.Labels[corev1.LabelInstanceTypeStable] != "gpu_1x_gh200" {
		t.Fatalf("unexpected instance type label: %s", nc.Labels[corev1.LabelInstanceTypeStable])
	}
}
