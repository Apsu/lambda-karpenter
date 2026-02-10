package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/evecallicoat/lambda-karpenter/api/v1alpha1"
	"github.com/evecallicoat/lambda-karpenter/internal/lambdaclient"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/karpenter/pkg/apis"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
)

var testLog = zap.New(zap.UseDevMode(true))

type fakeLambda struct {
	listInstances []lambdaclient.Instance
	instances     map[string]lambdaclient.Instance
	launchReqs    []lambdaclient.LaunchRequest
	launchIDs     []string
	terminated    []string
	getErr        error
}

func (f *fakeLambda) ListInstances(ctx context.Context) ([]lambdaclient.Instance, error) {
	return append([]lambdaclient.Instance(nil), f.listInstances...), nil
}

// List implements InstanceLister so fakeLambda can serve as both LambdaAPI and InstanceLister in tests.
func (f *fakeLambda) List(ctx context.Context) ([]lambdaclient.Instance, error) {
	return f.ListInstances(ctx)
}

func (f *fakeLambda) GetInstance(ctx context.Context, id string) (*lambdaclient.Instance, error) {
	if inst, ok := f.instances[id]; ok {
		return &inst, nil
	}
	if f.getErr != nil {
		return nil, f.getErr
	}
	return nil, fmt.Errorf("lambda api GET /api/v1/instances/%s failed: 404: not found", id)
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
			"i-1": {ID: "i-1", Hostname: "gh200-pool-abc", IP: "1.2.3.4", PrivateIP: "10.0.0.1", Type: lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200"}, Region: lambdaclient.Region{Name: "us-east-3"}},
		},
		launchIDs: []string{"i-1"},
	}

	p := New(client, fakeAPI, fakeAPI, nil, "gh200-test1", testLog)

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

func TestProviderCreateLaunchRequest_NoLimit(t *testing.T) {
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
	}

	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, nodePool).Build()
	fakeAPI := &fakeLambda{
		instances: map[string]lambdaclient.Instance{
			"i-1": {ID: "i-1", Hostname: "gh200-pool-abc", Type: lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200"}, Region: lambdaclient.Region{Name: "us-east-3"}},
		},
		launchIDs: []string{"i-1"},
	}

	p := New(client, fakeAPI, fakeAPI, nil, "gh200-test1", testLog)

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

	if _, err := p.Create(context.Background(), nc); err != nil {
		t.Fatalf("Create: %v", err)
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
	p := New(client, fakeAPI, fakeAPI, nil, "gh200-test1", testLog)
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
	p := New(client, fakeAPI, fakeAPI, nil, "gh200-test1", testLog)

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
		ID:        "i-1",
		Hostname:  "gh200-pool-abc",
		IP:        "1.2.3.4",
		PrivateIP: "10.0.0.1",
		Type:      lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200"},
		Region:    lambdaclient.Region{Name: "us-east-3"},
	}
	nc := p.nodeClaimFromInstance(&v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}, inst)
	if nc.Status.ProviderID != "lambda://i-1" {
		t.Fatalf("unexpected providerID: %s", nc.Status.ProviderID)
	}
	if nc.Labels[corev1.LabelInstanceTypeStable] != "gpu_1x_gh200" {
		t.Fatalf("unexpected instance type label: %s", nc.Labels[corev1.LabelInstanceTypeStable])
	}
	// Verify zone is synthetic
	if nc.Labels[corev1.LabelTopologyZone] != "us-east-3a" {
		t.Fatalf("expected zone us-east-3a, got %s", nc.Labels[corev1.LabelTopologyZone])
	}
}

func TestProviderIsDrifted(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	gv := schema.GroupVersion{Group: apis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, gv)
	scheme.AddKnownTypes(gv, &v1.NodePool{}, &v1.NodePoolList{}, &v1.NodeClaim{}, &v1.NodeClaimList{})

	class := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "lambda-default"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"Eve"},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class).Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	// Not drifted
	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				corev1.LabelTopologyRegion:    "us-east-3",
				corev1.LabelInstanceTypeStable: "gpu_1x_gh200",
			},
		},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "LambdaNodeClass", Name: "lambda-default"},
		},
	}
	reason, err := p.IsDrifted(context.Background(), nc)
	if err != nil {
		t.Fatalf("IsDrifted: %v", err)
	}
	if reason != "" {
		t.Fatalf("expected no drift, got %q", reason)
	}

	// Region drifted
	nc2 := nc.DeepCopy()
	nc2.Labels[corev1.LabelTopologyRegion] = "us-west-1"
	reason, err = p.IsDrifted(context.Background(), nc2)
	if err != nil {
		t.Fatalf("IsDrifted: %v", err)
	}
	if reason != "RegionDrifted" {
		t.Fatalf("expected RegionDrifted, got %q", reason)
	}

	// InstanceType drifted
	nc3 := nc.DeepCopy()
	nc3.Labels[corev1.LabelInstanceTypeStable] = "gpu_1x_a10"
	reason, err = p.IsDrifted(context.Background(), nc3)
	if err != nil {
		t.Fatalf("IsDrifted: %v", err)
	}
	if reason != "InstanceTypeDrifted" {
		t.Fatalf("expected InstanceTypeDrifted, got %q", reason)
	}
}

func TestProviderRepairPolicies(t *testing.T) {
	p := &Provider{}
	policies := p.RepairPolicies()
	if len(policies) != 1 {
		t.Fatalf("expected 1 repair policy, got %d", len(policies))
	}
	if policies[0].ConditionType != "Ready" {
		t.Fatalf("expected Ready condition type, got %s", policies[0].ConditionType)
	}
}

func TestProviderDeleteNotFound(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "missing"},
		Status: v1.NodeClaimStatus{
			ProviderID: "lambda://i-missing",
		},
	}
	err := p.Delete(context.Background(), nc)
	if err == nil {
		t.Fatal("expected error for missing instance")
	}
}

func TestProviderDeleteTerminalInstance(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{
		listInstances: []lambdaclient.Instance{
			{ID: "i-1", Name: "dead", Status: "terminated", Tags: []lambdaclient.TagEntry{
				{Key: "karpenter-sh-cluster", Value: "test"},
				{Key: "karpenter-sh-nodeclaim", Value: "dead"},
			}},
		},
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "dead"},
	}
	err := p.Delete(context.Background(), nc)
	if err == nil {
		t.Fatal("expected NodeClaimNotFoundError for terminal instance")
	}
	if len(fakeAPI.terminated) != 0 {
		t.Fatalf("should not have called TerminateInstance, got %v", fakeAPI.terminated)
	}
}

func TestProviderGetInvalidProviderID(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	_, err := p.Get(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty provider ID")
	}
}

func TestProviderGetNotFound(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	_, err := p.Get(context.Background(), "lambda://i-nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent instance")
	}
}
