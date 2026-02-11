package provider

import (
	"context"
	"fmt"
	"strings"
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
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
)

var testLog = zap.New(zap.UseDevMode(true))

// gh200Specs is a realistic GH200 instance type spec for tests.
var gh200Specs = lambdaclient.InstanceTypeSpec{
	VCPUs:      72,
	MemoryGiB:  480,
	StorageGiB: 0,
	GPUs:       1,
}

type fakeLambda struct {
	listInstances []lambdaclient.Instance
	listErr       error
	instances     map[string]lambdaclient.Instance
	launchReqs    []lambdaclient.LaunchRequest
	launchIDs     []string
	launchErr     error
	terminated    []string
	getErr        error
}

func (f *fakeLambda) ListInstances(ctx context.Context) ([]lambdaclient.Instance, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
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
	if f.launchErr != nil {
		return nil, f.launchErr
	}
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
			"i-1": {ID: "i-1", Hostname: "gh200-pool-abc", IP: "1.2.3.4", PrivateIP: "10.0.0.1",
				Type:   lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs},
				Region: lambdaclient.Region{Name: "us-east-3"},
				Tags: []lambdaclient.TagEntry{
					{Key: "karpenter-lambda-cloud-image-id", Value: "lambda-stack-24-04"},
				}},
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

	// Verify Allocatable/Capacity are populated (prevents duplicate NodeClaim bug).
	if got.Status.Allocatable == nil {
		t.Fatal("expected Allocatable to be set")
	}
	if got.Status.Capacity == nil {
		t.Fatal("expected Capacity to be set")
	}
	cpuQty := got.Status.Allocatable[corev1.ResourceCPU]
	if cpuQty.Value() != 72 {
		t.Fatalf("expected 72 CPUs, got %d", cpuQty.Value())
	}
	gpuQty := got.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
	if gpuQty.Value() != 1 {
		t.Fatalf("expected 1 GPU, got %d", gpuQty.Value())
	}

	// Verify ImageID is populated from the instance tag.
	if got.Status.ImageID != "lambda-stack-24-04" {
		t.Fatalf("expected ImageID lambda-stack-24-04, got %q", got.Status.ImageID)
	}

	// Verify the launch request includes the image tag.
	hasImageTag := false
	for _, tag := range req.Tags {
		if tag.Key == "karpenter-lambda-cloud-image-id" && tag.Value == "lambda-stack-24-04" {
			hasImageTag = true
		}
	}
	if !hasImageTag {
		t.Fatalf("expected image ID tag in launch request, got tags: %v", req.Tags)
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
			"i-1": {ID: "i-1", Hostname: "gh200-pool-abc",
				Type:   lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs},
				Region: lambdaclient.Region{Name: "us-east-3"}},
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
			{ID: "i-1", Hostname: "gh200-pool-xyz", Status: "terminated"},
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
			Image:        &v1alpha1.LambdaImage{Family: "lambda-stack-24-04"},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class).Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	// Not drifted
	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				corev1.LabelTopologyRegion:     "us-east-3",
				corev1.LabelInstanceTypeStable: "gpu_1x_gh200",
			},
		},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "LambdaNodeClass", Name: "lambda-default"},
		},
		Status: v1.NodeClaimStatus{
			ImageID: "lambda-stack-24-04",
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

	// Image drifted
	nc4 := nc.DeepCopy()
	nc4.Status.ImageID = "lambda-stack-22-04"
	reason, err = p.IsDrifted(context.Background(), nc4)
	if err != nil {
		t.Fatalf("IsDrifted: %v", err)
	}
	if reason != "ImageDrifted" {
		t.Fatalf("expected ImageDrifted, got %q", reason)
	}

	// No image drift when ImageID is empty (old instances without the tag)
	nc5 := nc.DeepCopy()
	nc5.Status.ImageID = ""
	reason, err = p.IsDrifted(context.Background(), nc5)
	if err != nil {
		t.Fatalf("IsDrifted: %v", err)
	}
	if reason != "" {
		t.Fatalf("expected no drift for empty ImageID, got %q", reason)
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

func TestProviderDeleteTerminatedInstance(t *testing.T) {
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
	if !cloudprovider.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError, got %v", err)
	}
	if len(fakeAPI.terminated) != 0 {
		t.Fatalf("should not have called TerminateInstance, got %v", fakeAPI.terminated)
	}
}

func TestProviderDeleteTerminatingInstance(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{
		listInstances: []lambdaclient.Instance{
			{ID: "i-1", Name: "dying", Status: "terminating", Tags: []lambdaclient.TagEntry{
				{Key: "karpenter-sh-cluster", Value: "test"},
				{Key: "karpenter-sh-nodeclaim", Value: "dying"},
			}},
		},
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	nc := &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "dying"}}
	err := p.Delete(context.Background(), nc)
	// "terminating" means deletion is in progress — should return nil so Karpenter retries.
	if err != nil {
		t.Fatalf("expected nil for terminating instance, got %v", err)
	}
	if len(fakeAPI.terminated) != 0 {
		t.Fatalf("should not have called TerminateInstance for terminating instance, got %v", fakeAPI.terminated)
	}
}

func TestProviderDeleteUnhealthyInstance(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{
		listInstances: []lambdaclient.Instance{
			{ID: "i-1", Name: "sick", Status: "unhealthy", Tags: []lambdaclient.TagEntry{
				{Key: "karpenter-sh-cluster", Value: "test"},
				{Key: "karpenter-sh-nodeclaim", Value: "sick"},
			}},
		},
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	nc := &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "sick"}}
	err := p.Delete(context.Background(), nc)
	if err != nil {
		t.Fatalf("expected nil (termination triggered), got %v", err)
	}
	// Must actually call TerminateInstance for unhealthy instances.
	if len(fakeAPI.terminated) != 1 || fakeAPI.terminated[0] != "i-1" {
		t.Fatalf("expected TerminateInstance call for i-1, got %v", fakeAPI.terminated)
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
	if !cloudprovider.IsNodeClaimNotFoundError(err) {
		t.Fatalf("expected NodeClaimNotFoundError, got %T: %v", err, err)
	}
}

func TestProviderGetTransientError(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	// Both GetInstance and List fail — simulates transient API outage.
	fakeAPI := &fakeLambda{
		getErr:  fmt.Errorf("lambda api GET failed: 500: internal server error"),
		listErr: fmt.Errorf("lambda api GET failed: 500: internal server error"),
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	_, err := p.Get(context.Background(), "lambda://i-123")
	if err == nil {
		t.Fatal("expected error")
	}
	// Must NOT be NodeClaimNotFoundError — transient errors should trigger retry, not finalization.
	if cloudprovider.IsNodeClaimNotFoundError(err) {
		t.Fatalf("transient error should NOT be NodeClaimNotFoundError, got: %v", err)
	}
}

func TestProviderListFiltersTerminalInstances(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{
		listInstances: []lambdaclient.Instance{
			{ID: "i-active", Status: "active", Tags: []lambdaclient.TagEntry{{Key: "karpenter-sh-cluster", Value: "test"}}},
			{ID: "i-terminated", Status: "terminated", Tags: []lambdaclient.TagEntry{{Key: "karpenter-sh-cluster", Value: "test"}}},
			{ID: "i-preempted", Status: "preempted", Tags: []lambdaclient.TagEntry{{Key: "karpenter-sh-cluster", Value: "test"}}},
			{ID: "i-unhealthy", Status: "unhealthy", Tags: []lambdaclient.TagEntry{{Key: "karpenter-sh-cluster", Value: "test"}}},
			{ID: "i-terminating", Status: "terminating", Tags: []lambdaclient.TagEntry{{Key: "karpenter-sh-cluster", Value: "test"}}},
		},
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	items, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 active instance, got %d", len(items))
	}
	if items[0].Status.ProviderID != "lambda://i-active" {
		t.Fatalf("expected active instance, got %s", items[0].Status.ProviderID)
	}
}

func TestProviderNodeClaimCapacityTypeLabel(t *testing.T) {
	p := &Provider{}
	inst := &lambdaclient.Instance{
		ID:     "i-1",
		Type:   lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs},
		Region: lambdaclient.Region{Name: "us-east-3"},
	}
	// Create path: seed is non-nil.
	nc := p.nodeClaimFromInstance(&v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}, inst)
	if nc.Labels[v1.CapacityTypeLabelKey] != v1.CapacityTypeOnDemand {
		t.Fatalf("expected on-demand capacity type on Create path, got %q", nc.Labels[v1.CapacityTypeLabelKey])
	}
	// List/Get path: seed is nil.
	nc2 := p.nodeClaimFromInstance(nil, inst)
	if nc2.Labels[v1.CapacityTypeLabelKey] != v1.CapacityTypeOnDemand {
		t.Fatalf("expected on-demand capacity type on List path, got %q", nc2.Labels[v1.CapacityTypeLabelKey])
	}
}

// testSchemeAndClass returns a scheme and LambdaNodeClass suitable for Create tests.
func testSchemeAndClass(t *testing.T) (*v1alpha1.LambdaNodeClass, *v1.NodePool, runtime.Scheme) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	gv := schema.GroupVersion{Group: apis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, gv)
	scheme.AddKnownTypes(gv, &v1.NodePool{}, &v1.NodePoolList{}, &v1.NodeClaim{}, &v1.NodeClaimList{})

	class := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "lambda-gh200"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"Eve"},
			UserData:     "#cloud-config",
		},
	}
	nodePool := &v1.NodePool{
		ObjectMeta: metav1.ObjectMeta{Name: "gh200-pool"},
	}
	return class, nodePool, *scheme
}

// testNodeClaim returns a minimal NodeClaim for Create tests.
func testNodeClaim() *v1.NodeClaim {
	return &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "gh200-pool-abc",
			Labels: map[string]string{v1.NodePoolLabelKey: "gh200-pool"},
		},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{
				Group: v1alpha1.Group,
				Kind:  "LambdaNodeClass",
				Name:  "lambda-gh200",
			},
		},
	}
}

func TestProviderCreateCapacityError(t *testing.T) {
	class, nodePool, scheme := testSchemeAndClass(t)
	client := fake.NewClientBuilder().WithScheme(&scheme).WithObjects(class, nodePool).Build()
	fakeAPI := &fakeLambda{
		launchErr: fmt.Errorf("lambda api POST /api/v1/instance-operations/launch failed: 503: insufficient capacity"),
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	_, err := p.Create(context.Background(), testNodeClaim())
	if err == nil {
		t.Fatal("expected error")
	}
	// Karpenter wraps capacity errors so it knows to retry later.
	if !cloudprovider.IsInsufficientCapacityError(err) {
		t.Fatalf("expected InsufficientCapacityError, got %T: %v", err, err)
	}
}

func TestProviderCreateNonCapacityError(t *testing.T) {
	class, nodePool, scheme := testSchemeAndClass(t)
	client := fake.NewClientBuilder().WithScheme(&scheme).WithObjects(class, nodePool).Build()
	fakeAPI := &fakeLambda{
		launchErr: fmt.Errorf("lambda api POST /api/v1/instance-operations/launch failed: 400: invalid ssh key"),
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	_, err := p.Create(context.Background(), testNodeClaim())
	if err == nil {
		t.Fatal("expected error")
	}
	if cloudprovider.IsInsufficientCapacityError(err) {
		t.Fatalf("should not be InsufficientCapacityError for 400: %v", err)
	}
}

func TestProviderCreateEmptyIDs(t *testing.T) {
	class, nodePool, scheme := testSchemeAndClass(t)
	client := fake.NewClientBuilder().WithScheme(&scheme).WithObjects(class, nodePool).Build()
	fakeAPI := &fakeLambda{
		launchIDs: []string{}, // API returns success but no IDs
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	_, err := p.Create(context.Background(), testNodeClaim())
	if err == nil {
		t.Fatal("expected error for empty launch IDs")
	}
	if !strings.Contains(err.Error(), "no instance ids") {
		t.Fatalf("expected 'no instance ids' error, got: %v", err)
	}
}

func TestProviderCreateGetInstanceFailure(t *testing.T) {
	class, nodePool, scheme := testSchemeAndClass(t)
	client := fake.NewClientBuilder().WithScheme(&scheme).WithObjects(class, nodePool).Build()
	fakeAPI := &fakeLambda{
		launchIDs: []string{"i-1"},
		// instances map is empty — GetInstance will return 404 for i-1
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	_, err := p.Create(context.Background(), testNodeClaim())
	if err == nil {
		t.Fatal("expected error when GetInstance fails after launch")
	}
	// The launch itself succeeded, so verify it was called.
	if len(fakeAPI.launchReqs) != 1 {
		t.Fatalf("expected 1 launch request, got %d", len(fakeAPI.launchReqs))
	}
}

func TestProviderCreateIdempotentByTag(t *testing.T) {
	class, nodePool, scheme := testSchemeAndClass(t)
	client := fake.NewClientBuilder().WithScheme(&scheme).WithObjects(class, nodePool).Build()

	// An instance already exists for this NodeClaim (found by tag).
	fakeAPI := &fakeLambda{
		listInstances: []lambdaclient.Instance{
			{
				ID:     "i-existing",
				Status: "active",
				Type:   lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs},
				Region: lambdaclient.Region{Name: "us-east-3"},
				Tags: []lambdaclient.TagEntry{
					{Key: "karpenter-sh-cluster", Value: "test"},
					{Key: "karpenter-sh-nodeclaim", Value: "gh200-pool-abc"},
				},
			},
		},
	}
	p := New(client, fakeAPI, fakeAPI, nil, "test", testLog)

	got, err := p.Create(context.Background(), testNodeClaim())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Should NOT have launched a new instance.
	if len(fakeAPI.launchReqs) != 0 {
		t.Fatalf("expected no launch requests (idempotent), got %d", len(fakeAPI.launchReqs))
	}
	if got.Status.ProviderID != "lambda://i-existing" {
		t.Fatalf("expected providerID lambda://i-existing, got %s", got.Status.ProviderID)
	}
	// Verify Allocatable is populated even on idempotent return.
	if got.Status.Allocatable == nil {
		t.Fatal("expected Allocatable on idempotent Create")
	}
	cpuQty := got.Status.Allocatable[corev1.ResourceCPU]
	if cpuQty.Value() != 72 {
		t.Fatalf("expected 72 CPUs, got %d", cpuQty.Value())
	}
}

func TestProviderNodeClaimFromInstanceAllocatable(t *testing.T) {
	p := &Provider{}
	inst := &lambdaclient.Instance{
		ID:     "i-1",
		Type:   lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs},
		Region: lambdaclient.Region{Name: "us-east-3"},
	}
	nc := p.nodeClaimFromInstance(&v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}, inst)

	if nc.Status.Capacity == nil {
		t.Fatal("expected Capacity to be set")
	}
	if nc.Status.Allocatable == nil {
		t.Fatal("expected Allocatable to be set")
	}

	cpu := nc.Status.Allocatable[corev1.ResourceCPU]
	if cpu.Value() != 72 {
		t.Fatalf("expected 72 CPUs, got %d", cpu.Value())
	}
	mem := nc.Status.Allocatable[corev1.ResourceMemory]
	expectedMem := int64(480) << 30
	if mem.Value() != expectedMem {
		t.Fatalf("expected %d memory, got %d", expectedMem, mem.Value())
	}
	gpu := nc.Status.Allocatable[corev1.ResourceName("nvidia.com/gpu")]
	if gpu.Value() != 1 {
		t.Fatalf("expected 1 GPU, got %d", gpu.Value())
	}
	pods := nc.Status.Allocatable[corev1.ResourcePods]
	if pods.Value() != 110 {
		t.Fatalf("expected 110 pods, got %d", pods.Value())
	}
}

func TestProviderNodeClaimFromInstanceZeroSpecs(t *testing.T) {
	// Edge case: instance type specs are all zero (e.g., API returned no spec data).
	// Should still produce a valid NodeClaim without panicking.
	p := &Provider{}
	inst := &lambdaclient.Instance{
		ID:     "i-1",
		Type:   lambdaclient.InstanceTypeRef{Name: "unknown-type"},
		Region: lambdaclient.Region{Name: "us-east-3"},
	}
	nc := p.nodeClaimFromInstance(&v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{}}}, inst)

	if nc.Status.Capacity == nil {
		t.Fatal("expected Capacity to be set even with zero specs")
	}
	// CPU and memory should be 0, no GPU or storage keys.
	cpu := nc.Status.Capacity[corev1.ResourceCPU]
	if cpu.Value() != 0 {
		t.Fatalf("expected 0 CPUs for zero specs, got %d", cpu.Value())
	}
	if _, ok := nc.Status.Capacity[corev1.ResourceName("nvidia.com/gpu")]; ok {
		t.Fatal("expected no GPU resource for zero-GPU specs")
	}
	if _, ok := nc.Status.Capacity[corev1.ResourceEphemeralStorage]; ok {
		t.Fatal("expected no ephemeral storage for zero-storage specs")
	}
}
