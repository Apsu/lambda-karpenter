package provider

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lambdal/lambda-karpenter/api/v1alpha1"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
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

// gh200Specs is the real GH200 instance type spec from the Lambda API.
var gh200Specs = lambdaclient.InstanceTypeSpec{
	VCPUs:      64,
	MemoryGiB:  432,
	StorageGiB: 4096,
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
	terminateErr  error
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
	if f.terminateErr != nil {
		return f.terminateErr
	}
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
			Region:             "us-east-3",
			InstanceType:       "gpu_1x_gh200",
			SSHKeyNames:        []string{"Eve"},
			UserData:           "#cloud-config",
			FirewallRulesetIDs: []string{"fw-1"},
			Tags:               map[string]string{"env": "test"},
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

	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "gh200-test1", testLog)

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
	if cpuQty.Value() != 64 {
		t.Fatalf("expected 64 CPUs, got %d", cpuQty.Value())
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
			Region:             "us-east-3",
			InstanceType:       "gpu_1x_gh200",
			SSHKeyNames:        []string{"Eve"},
			UserData:           "#cloud-config",
			FirewallRulesetIDs: []string{"fw-1"},
			Tags:               map[string]string{"env": "test"},
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

	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "gh200-test1", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "gh200-test1", testLog)
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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "gh200-test1", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	_, err := p.Get(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty provider ID")
	}
}

func TestProviderGetNotFound(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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

// testInstanceTypeCache creates a seeded cache with a long TTL for tests.
func testInstanceTypeCache(data map[string]lambdaclient.InstanceTypesItem) *lambdaclient.InstanceTypeCache {
	c := &lambdaclient.InstanceTypeCache{TTL: 1 * time.Hour}
	c.Seed(data)
	return c
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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

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
	if cpuQty.Value() != 64 {
		t.Fatalf("expected 64 CPUs, got %d", cpuQty.Value())
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
	if cpu.Value() != 64 {
		t.Fatalf("expected 64 CPUs, got %d", cpu.Value())
	}
	mem := nc.Status.Allocatable[corev1.ResourceMemory]
	expectedMem := int64(432) << 30
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

// --- GetInstanceTypes / instanceTypeFromItem ---

func TestProviderInstanceTypeFromItem(t *testing.T) {
	p := &Provider{}

	item := lambdaclient.InstanceTypesItem{
		InstanceType: lambdaclient.InstanceTypeRef{
			Name:       "gpu_1x_gh200",
			PriceCents: 199,
			Specs:      gh200Specs,
		},
		Regions: []lambdaclient.Region{
			{Name: "us-east-3"},
			{Name: "us-west-1"},
		},
	}

	it := p.instanceTypeFromItem("gpu_1x_gh200", item)

	if it.Name != "gpu_1x_gh200" {
		t.Fatalf("expected name gpu_1x_gh200, got %s", it.Name)
	}

	// Architecture: gh200 → arm64
	archReq := it.Requirements.Get(corev1.LabelArchStable)
	if !archReq.Has("arm64") {
		t.Fatalf("expected arm64 for gh200, got %v", archReq)
	}

	// OS: linux
	osReq := it.Requirements.Get(corev1.LabelOSStable)
	if !osReq.Has("linux") {
		t.Fatalf("expected linux OS, got %v", osReq)
	}

	// Instance type label
	itReq := it.Requirements.Get(corev1.LabelInstanceTypeStable)
	if !itReq.Has("gpu_1x_gh200") {
		t.Fatalf("expected instance type label, got %v", itReq)
	}

	// Offerings: one per region, each on-demand with synthetic zone
	if len(it.Offerings) != 2 {
		t.Fatalf("expected 2 offerings (one per region), got %d", len(it.Offerings))
	}
	for _, off := range it.Offerings {
		zoneReq := off.Requirements.Get(corev1.LabelTopologyZone)
		regionReq := off.Requirements.Get(corev1.LabelTopologyRegion)
		capReq := off.Requirements.Get(v1.CapacityTypeLabelKey)
		if !capReq.Has(v1.CapacityTypeOnDemand) {
			t.Fatalf("expected on-demand capacity, got %v", capReq)
		}
		// Zone should be region + "a"
		region := regionReq.Values()[0]
		zone := zoneReq.Values()[0]
		if zone != region+"a" {
			t.Fatalf("expected zone %sa, got %s", region, zone)
		}
		if !off.Available {
			t.Fatal("expected offering to be available")
		}
		// Price: 199 cents → $1.99
		if off.Price != 1.99 {
			t.Fatalf("expected price 1.99, got %f", off.Price)
		}
	}

	// Capacity
	cpu := it.Capacity[corev1.ResourceCPU]
	if cpu.Value() != 64 {
		t.Fatalf("expected 64 CPUs, got %d", cpu.Value())
	}
	gpu := it.Capacity[corev1.ResourceName("nvidia.com/gpu")]
	if gpu.Value() != 1 {
		t.Fatalf("expected 1 GPU, got %d", gpu.Value())
	}
}

func TestProviderInstanceTypeFromItemAmd64(t *testing.T) {
	p := &Provider{}

	item := lambdaclient.InstanceTypesItem{
		InstanceType: lambdaclient.InstanceTypeRef{
			Name:       "gpu_1x_a100",
			PriceCents: 110,
			Specs: lambdaclient.InstanceTypeSpec{
				VCPUs: 30, MemoryGiB: 200, GPUs: 1,
			},
		},
		Regions: []lambdaclient.Region{{Name: "us-east-1"}},
	}

	it := p.instanceTypeFromItem("gpu_1x_a100", item)

	// Non-GH200 → amd64
	archReq := it.Requirements.Get(corev1.LabelArchStable)
	if !archReq.Has("amd64") {
		t.Fatalf("expected amd64 for a100, got %v", archReq)
	}
	if archReq.Has("arm64") {
		t.Fatal("a100 should not be arm64")
	}
}

func TestProviderInstanceTypeFromItemNoRegions(t *testing.T) {
	p := &Provider{}

	item := lambdaclient.InstanceTypesItem{
		InstanceType: lambdaclient.InstanceTypeRef{
			Name:       "gpu_1x_h100",
			PriceCents: 250,
			Specs: lambdaclient.InstanceTypeSpec{
				VCPUs: 26, MemoryGiB: 200, GPUs: 1,
			},
		},
		Regions: nil, // no regions with capacity
	}

	it := p.instanceTypeFromItem("gpu_1x_h100", item)

	// Should still have 1 offering with "unknown" region, marked unavailable.
	if len(it.Offerings) != 1 {
		t.Fatalf("expected 1 fallback offering, got %d", len(it.Offerings))
	}
	if it.Offerings[0].Available {
		t.Fatal("expected fallback offering to be unavailable")
	}
	zoneReq := it.Offerings[0].Requirements.Get(corev1.LabelTopologyZone)
	if !zoneReq.Has("unknowna") {
		t.Fatalf("expected zone unknowna, got %v", zoneReq)
	}
}

// --- Get happy path + list fallback ---

func TestProviderGetHappyPath(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{
		instances: map[string]lambdaclient.Instance{
			"i-123": {
				ID:     "i-123",
				Type:   lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs},
				Region: lambdaclient.Region{Name: "us-east-3"},
				Tags: []lambdaclient.TagEntry{
					{Key: "karpenter-lambda-cloud-image-id", Value: "my-image"},
				},
			},
		},
	}
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nc, err := p.Get(context.Background(), "lambda://i-123")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if nc.Status.ProviderID != "lambda://i-123" {
		t.Fatalf("expected providerID lambda://i-123, got %s", nc.Status.ProviderID)
	}
	if nc.Labels[corev1.LabelInstanceTypeStable] != "gpu_1x_gh200" {
		t.Fatalf("expected instance type label, got %s", nc.Labels[corev1.LabelInstanceTypeStable])
	}
	if nc.Status.ImageID != "my-image" {
		t.Fatalf("expected ImageID my-image, got %q", nc.Status.ImageID)
	}
	if nc.Status.Allocatable == nil {
		t.Fatal("expected Allocatable")
	}
}

func TestProviderGetFallbackToList(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	// GetInstance fails for hostname-based providerID, but list finds it.
	fakeAPI := &fakeLambda{
		listInstances: []lambdaclient.Instance{
			{ID: "i-real", Hostname: "my-node", Status: "active",
				Type:   lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs},
				Region: lambdaclient.Region{Name: "us-east-3"}},
		},
	}
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nc, err := p.Get(context.Background(), "lambda://my-node")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if nc.Status.ProviderID != "lambda://i-real" {
		t.Fatalf("expected providerID lambda://i-real, got %s", nc.Status.ProviderID)
	}
}

// --- List happy path ---

func TestProviderListHappyPath(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{
		listInstances: []lambdaclient.Instance{
			{
				ID: "i-1", Status: "active",
				Type:   lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs},
				Region: lambdaclient.Region{Name: "us-east-3"},
				Tags: []lambdaclient.TagEntry{
					{Key: "karpenter-sh-cluster", Value: "test"},
					{Key: "karpenter-sh-nodepool", Value: "pool-a"},
					{Key: "karpenter-lambda-cloud-lambdanodeclass", Value: "nc-1"},
					{Key: "karpenter-lambda-cloud-image-id", Value: "img-1"},
				},
			},
			{
				ID: "i-2", Status: "booting",
				Type:   lambdaclient.InstanceTypeRef{Name: "gpu_1x_a100", Specs: lambdaclient.InstanceTypeSpec{VCPUs: 30, MemoryGiB: 200, StorageGiB: 512, GPUs: 1}},
				Region: lambdaclient.Region{Name: "us-west-1"},
				Tags: []lambdaclient.TagEntry{
					{Key: "karpenter-sh-cluster", Value: "test"},
					{Key: "karpenter-sh-nodepool", Value: "pool-b"},
				},
			},
		},
	}
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	items, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	// Find each by providerID (map order isn't guaranteed from list).
	byID := map[string]*v1.NodeClaim{}
	for _, nc := range items {
		byID[nc.Status.ProviderID] = nc
	}

	nc1 := byID["lambda://i-1"]
	if nc1 == nil {
		t.Fatal("missing i-1")
	}
	if nc1.Labels[v1.NodePoolLabelKey] != "pool-a" {
		t.Fatalf("expected nodepool label pool-a, got %q", nc1.Labels[v1.NodePoolLabelKey])
	}
	ncLabelKey := v1.NodeClassLabelKey(schema.GroupKind{Group: v1alpha1.Group, Kind: "LambdaNodeClass"})
	if nc1.Labels[ncLabelKey] != "nc-1" {
		t.Fatalf("expected nodeclass label nc-1, got %q", nc1.Labels[ncLabelKey])
	}
	if nc1.Labels[v1.CapacityTypeLabelKey] != v1.CapacityTypeOnDemand {
		t.Fatalf("expected on-demand, got %q", nc1.Labels[v1.CapacityTypeLabelKey])
	}
	if nc1.Status.ImageID != "img-1" {
		t.Fatalf("expected ImageID img-1, got %q", nc1.Status.ImageID)
	}
	if nc1.Status.Allocatable == nil {
		t.Fatal("expected Allocatable on list item")
	}

	nc2 := byID["lambda://i-2"]
	if nc2 == nil {
		t.Fatal("missing i-2")
	}
	if nc2.Labels[corev1.LabelTopologyRegion] != "us-west-1" {
		t.Fatalf("expected region us-west-1, got %q", nc2.Labels[corev1.LabelTopologyRegion])
	}
	if nc2.Labels[v1.NodePoolLabelKey] != "pool-b" {
		t.Fatalf("expected nodepool label pool-b, got %q", nc2.Labels[v1.NodePoolLabelKey])
	}
}

// --- Delete API error ---

func TestProviderDeleteAPIError(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{
		instances: map[string]lambdaclient.Instance{
			"i-1": {ID: "i-1", Status: "active"},
		},
	}
	// Override TerminateInstance to return an error.
	terminateErr := fmt.Errorf("lambda api DELETE failed: 500: internal error")
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nc := &v1.NodeClaim{
		Status: v1.NodeClaimStatus{ProviderID: "lambda://i-1"},
	}

	// We need to make TerminateInstance fail. The current fake always succeeds.
	// Add terminateErr support to fakeLambda.
	fakeAPI.terminateErr = terminateErr
	err := p.Delete(context.Background(), nc)
	if err == nil {
		t.Fatal("expected error from TerminateInstance")
	}
	if err.Error() != terminateErr.Error() {
		t.Fatalf("expected %q, got %q", terminateErr, err)
	}
	// Should NOT be NodeClaimNotFoundError.
	if cloudprovider.IsNodeClaimNotFoundError(err) {
		t.Fatal("TerminateInstance error should not be NodeClaimNotFoundError")
	}
}

// --- Create validation ---

func TestProviderCreateMissingNodeClassRef(t *testing.T) {
	client := fake.NewClientBuilder().Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec:       v1.NodeClaimSpec{}, // no NodeClassRef
	}
	_, err := p.Create(context.Background(), nc)
	if err == nil {
		t.Fatal("expected error for missing nodeClassRef")
	}
	if !strings.Contains(err.Error(), "nodeClassRef") {
		t.Fatalf("expected nodeClassRef error, got: %v", err)
	}
}

func TestProviderCreateWrongGVK(t *testing.T) {
	class, nodePool, scheme := testSchemeAndClass(t)
	client := fake.NewClientBuilder().WithScheme(&scheme).WithObjects(class, nodePool).Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "test",
			Labels: map[string]string{v1.NodePoolLabelKey: "gh200-pool"},
		},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{
				Group: "wrong.group",
				Kind:  "WrongKind",
				Name:  "lambda-gh200",
			},
		},
	}
	_, err := p.Create(context.Background(), nc)
	if err == nil {
		t.Fatal("expected error for wrong GVK")
	}
	if !strings.Contains(err.Error(), "unsupported nodeclass") {
		t.Fatalf("expected 'unsupported nodeclass' error, got: %v", err)
	}
}

func TestProviderCreateNoSSHKeys(t *testing.T) {
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
			SSHKeyNames:  nil, // no SSH keys
		},
	}
	nodePool := &v1.NodePool{ObjectMeta: metav1.ObjectMeta{Name: "gh200-pool"}}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class, nodePool).Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	_, err := p.Create(context.Background(), testNodeClaim())
	if err == nil {
		t.Fatal("expected error for no SSH keys")
	}
	if !strings.Contains(err.Error(), "sshKeyNames") {
		t.Fatalf("expected sshKeyNames error, got: %v", err)
	}
}

// --- buildLaunchRequest ---

func TestProviderBuildLaunchRequestFullSpec(t *testing.T) {
	p := &Provider{clusterName: "my-cluster"}

	class := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "lambda-gh200"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:             "us-east-3",
			InstanceType:       "gpu_1x_gh200",
			SSHKeyNames:        []string{"key1", "key2"},
			UserData:           "#cloud-config\npackages: [vim]",
			FirewallRulesetIDs: []string{"fw-aaa", "fw-bbb"},
			Tags:               map[string]string{"team": "infra", "env": "staging"},
			Pool:               "reserved-pool",
			Image:              &v1alpha1.LambdaImage{ID: "img-123"},
		},
	}
	boolTrue := true
	class.Spec.PublicIP = &boolTrue

	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name: "my-node-abc",
			Labels: map[string]string{
				v1.NodePoolLabelKey: "my-pool",
				v1.NodeClassLabelKey(schema.GroupKind{Group: v1alpha1.Group, Kind: "LambdaNodeClass"}): "lambda-gh200",
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

	req, err := p.buildLaunchRequest(nc, class)
	if err != nil {
		t.Fatalf("buildLaunchRequest: %v", err)
	}

	// Basic fields
	if req.RegionName != "us-east-3" {
		t.Fatalf("expected region us-east-3, got %s", req.RegionName)
	}
	if req.InstanceTypeName != "gpu_1x_gh200" {
		t.Fatalf("expected instanceType gpu_1x_gh200, got %s", req.InstanceTypeName)
	}
	if req.UserData != "#cloud-config\npackages: [vim]" {
		t.Fatalf("unexpected userData: %s", req.UserData)
	}
	if len(req.SSHKeyNames) != 2 || req.SSHKeyNames[0] != "key1" {
		t.Fatalf("unexpected SSHKeyNames: %v", req.SSHKeyNames)
	}
	if req.Pool != "reserved-pool" {
		t.Fatalf("expected pool reserved-pool, got %s", req.Pool)
	}
	if req.PublicIP == nil || *req.PublicIP != true {
		t.Fatal("expected PublicIP=true")
	}

	// Image
	if req.Image == nil || req.Image.ID != "img-123" {
		t.Fatalf("expected image ID img-123, got %v", req.Image)
	}

	// Firewall rulesets
	if len(req.FirewallRulesets) != 2 {
		t.Fatalf("expected 2 firewall rulesets, got %d", len(req.FirewallRulesets))
	}
	if req.FirewallRulesets[0].ID != "fw-aaa" || req.FirewallRulesets[1].ID != "fw-bbb" {
		t.Fatalf("unexpected firewall rulesets: %v", req.FirewallRulesets)
	}

	// Tags: should include custom tags + system tags + image tag
	tagMap := map[string]string{}
	for _, tag := range req.Tags {
		tagMap[tag.Key] = tag.Value
	}
	if tagMap["team"] != "infra" {
		t.Fatalf("expected custom tag team=infra, got %q", tagMap["team"])
	}
	if tagMap["env"] != "staging" {
		t.Fatalf("expected custom tag env=staging, got %q", tagMap["env"])
	}
	if tagMap[tagCluster] != "my-cluster" {
		t.Fatalf("expected cluster tag, got %q", tagMap[tagCluster])
	}
	if tagMap[tagNodeClaim] != "my-node-abc" {
		t.Fatalf("expected nodeclaim tag, got %q", tagMap[tagNodeClaim])
	}
	if tagMap[tagNodePool] != "my-pool" {
		t.Fatalf("expected nodepool tag, got %q", tagMap[tagNodePool])
	}
	if tagMap[tagImageID] != "img-123" {
		t.Fatalf("expected image tag img-123, got %q", tagMap[tagImageID])
	}
}

func TestProviderBuildLaunchRequestFilesystemMounts(t *testing.T) {
	p := &Provider{clusterName: "test"}

	class := &v1alpha1.LambdaNodeClass{
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:          "us-east-3",
			InstanceType:    "gpu_1x_gh200",
			SSHKeyNames:     []string{"key"},
			FileSystemNames: []string{"my-fs"},
			FileSystemMounts: []v1alpha1.FileSystemMount{
				{MountPoint: "/mnt/data", FileSystemID: "fs-123"},
				{MountPoint: "/mnt/models", FileSystemID: "fs-456"},
			},
		},
	}
	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "LambdaNodeClass", Name: "test"},
		},
	}

	req, err := p.buildLaunchRequest(nc, class)
	if err != nil {
		t.Fatalf("buildLaunchRequest: %v", err)
	}

	if len(req.FileSystemNames) != 1 || req.FileSystemNames[0] != "my-fs" {
		t.Fatalf("expected fileSystemNames [my-fs], got %v", req.FileSystemNames)
	}
	if len(req.FileSystemMounts) != 2 {
		t.Fatalf("expected 2 filesystem mounts, got %d", len(req.FileSystemMounts))
	}
	if req.FileSystemMounts[0].MountPoint != "/mnt/data" || req.FileSystemMounts[0].FileSystemID != "fs-123" {
		t.Fatalf("unexpected first mount: %+v", req.FileSystemMounts[0])
	}
	if req.FileSystemMounts[1].MountPoint != "/mnt/models" || req.FileSystemMounts[1].FileSystemID != "fs-456" {
		t.Fatalf("unexpected second mount: %+v", req.FileSystemMounts[1])
	}
}

func TestProviderBuildLaunchRequestImageFamily(t *testing.T) {
	p := &Provider{clusterName: "test"}

	class := &v1alpha1.LambdaNodeClass{
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"key"},
			Image:        &v1alpha1.LambdaImage{Family: "ubuntu-22-04"},
		},
	}
	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "LambdaNodeClass", Name: "test"},
		},
	}

	req, err := p.buildLaunchRequest(nc, class)
	if err != nil {
		t.Fatalf("buildLaunchRequest: %v", err)
	}

	if req.Image == nil || req.Image.Family != "ubuntu-22-04" {
		t.Fatalf("expected image family ubuntu-22-04, got %v", req.Image)
	}

	// Image tag should be family when ID is empty.
	tagMap := map[string]string{}
	for _, tag := range req.Tags {
		tagMap[tag.Key] = tag.Value
	}
	if tagMap[tagImageID] != "ubuntu-22-04" {
		t.Fatalf("expected image tag ubuntu-22-04, got %q", tagMap[tagImageID])
	}
}

func TestProviderBuildLaunchRequestMissingRegion(t *testing.T) {
	p := &Provider{}
	class := &v1alpha1.LambdaNodeClass{
		Spec: v1alpha1.LambdaNodeClassSpec{InstanceType: "gpu_1x_gh200"},
	}
	nc := &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
	_, err := p.buildLaunchRequest(nc, class)
	if err == nil || !strings.Contains(err.Error(), "region") {
		t.Fatalf("expected region error, got: %v", err)
	}
}

func TestProviderBuildLaunchRequestMissingInstanceType(t *testing.T) {
	p := &Provider{}
	class := &v1alpha1.LambdaNodeClass{
		Spec: v1alpha1.LambdaNodeClassSpec{Region: "us-east-3"},
	}
	nc := &v1.NodeClaim{ObjectMeta: metav1.ObjectMeta{Name: "test"}}
	_, err := p.buildLaunchRequest(nc, class)
	if err == nil || !strings.Contains(err.Error(), "cannot determine instance type") {
		t.Fatalf("expected 'cannot determine instance type' error, got: %v", err)
	}
}

// --- sanitizeHostname ---

func TestSanitizeHostname(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"normal", "my-node-123", "my-node-123"},
		{"empty", "", "lambda-node"},
		{"special chars", "my_node.foo/bar", "my-node-foo-bar"},
		{"leading trailing dashes", "--my-node--", "my-node"},
		{"all special", "___", "lambda-node"},
		{"uppercase", "MY-NODE", "my-node"},
		{"long name", strings.Repeat("a", 100), strings.Repeat("a", 63)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeHostname(tt.in)
			if got != tt.want {
				t.Fatalf("sanitizeHostname(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- sanitizeTagKey ---

func TestSanitizeTagKey(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"normal", "my-tag", "my-tag"},
		{"karpenter style", "karpenter.sh/nodeclaim", "karpenter-sh-nodeclaim"},
		{"dots and underscores", "foo_bar.baz", "foo-bar-baz"},
		{"starts with number", "123abc", "k-123abc"},
		{"empty after sanitize", "", ""},
		{"uppercase", "MY_TAG", "my-tag"},
		{"long key", strings.Repeat("a", 100), strings.Repeat("a", 55)},
		{"preserves colon", "key:value", "key:value"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeTagKey(tt.in)
			if got != tt.want {
				t.Fatalf("sanitizeTagKey(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// --- instanceTypeFromNodeClaim ---

func TestInstanceTypeFromNodeClaim(t *testing.T) {
	nc := &v1.NodeClaim{
		Spec: v1.NodeClaimSpec{
			Requirements: []v1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpIn, Values: []string{"gpu_1x_gh200"}},
			},
		},
	}
	got := instanceTypeFromNodeClaim(nc)
	if got != "gpu_1x_gh200" {
		t.Fatalf("expected gpu_1x_gh200, got %q", got)
	}
}

func TestInstanceTypeFromNodeClaimEmpty(t *testing.T) {
	nc := &v1.NodeClaim{}
	got := instanceTypeFromNodeClaim(nc)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestInstanceTypeFromNodeClaimMultipleValues(t *testing.T) {
	// Multiple values means Karpenter hasn't narrowed to one — should return "".
	nc := &v1.NodeClaim{
		Spec: v1.NodeClaimSpec{
			Requirements: []v1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpIn, Values: []string{"gpu_1x_gh200", "gpu_1x_a100"}},
			},
		},
	}
	got := instanceTypeFromNodeClaim(nc)
	if got != "" {
		t.Fatalf("expected empty for multiple values, got %q", got)
	}
}

// --- buildLaunchRequest from NodeClaim requirements ---

func TestBuildLaunchRequestFromNodeClaimRequirements(t *testing.T) {
	p := &Provider{clusterName: "test"}
	class := &v1alpha1.LambdaNodeClass{
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"key"},
		},
	}
	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test-nc"},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "LambdaNodeClass", Name: "test"},
			Requirements: []v1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpIn, Values: []string{"gpu_1x_gh200"}},
			},
		},
	}
	req, err := p.buildLaunchRequest(nc, class)
	if err != nil {
		t.Fatalf("buildLaunchRequest: %v", err)
	}
	if req.InstanceTypeName != "gpu_1x_gh200" {
		t.Fatalf("expected gpu_1x_gh200, got %s", req.InstanceTypeName)
	}
}

func TestBuildLaunchRequestFallbackToNodeClass(t *testing.T) {
	p := &Provider{clusterName: "test"}
	class := &v1alpha1.LambdaNodeClass{
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_a100",
			SSHKeyNames:  []string{"key"},
		},
	}
	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test-nc"},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "LambdaNodeClass", Name: "test"},
		},
	}
	req, err := p.buildLaunchRequest(nc, class)
	if err != nil {
		t.Fatalf("buildLaunchRequest: %v", err)
	}
	if req.InstanceTypeName != "gpu_1x_a100" {
		t.Fatalf("expected fallback to gpu_1x_a100, got %s", req.InstanceTypeName)
	}
}

func TestBuildLaunchRequestNoInstanceTypeAnywhere(t *testing.T) {
	p := &Provider{}
	class := &v1alpha1.LambdaNodeClass{
		Spec: v1alpha1.LambdaNodeClassSpec{Region: "us-east-3"},
	}
	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test"},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "LambdaNodeClass", Name: "test"},
		},
	}
	_, err := p.buildLaunchRequest(nc, class)
	if err == nil {
		t.Fatal("expected error when no instance type available")
	}
	if !strings.Contains(err.Error(), "cannot determine instance type") {
		t.Fatalf("expected 'cannot determine instance type' error, got: %v", err)
	}
}

func TestBuildLaunchRequestNodeClaimOverridesNodeClass(t *testing.T) {
	p := &Provider{clusterName: "test"}
	class := &v1alpha1.LambdaNodeClass{
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_a100",
			SSHKeyNames:  []string{"key"},
		},
	}
	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test-nc"},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "LambdaNodeClass", Name: "test"},
			Requirements: []v1.NodeSelectorRequirementWithMinValues{
				{Key: corev1.LabelInstanceTypeStable, Operator: corev1.NodeSelectorOpIn, Values: []string{"gpu_1x_gh200"}},
			},
		},
	}
	req, err := p.buildLaunchRequest(nc, class)
	if err != nil {
		t.Fatalf("buildLaunchRequest: %v", err)
	}
	// NodeClaim requirement should take priority over NodeClass spec.
	if req.InstanceTypeName != "gpu_1x_gh200" {
		t.Fatalf("expected NodeClaim type gpu_1x_gh200 to override NodeClass gpu_1x_a100, got %s", req.InstanceTypeName)
	}
}

func TestBuildLaunchRequestUserDataTemplateRendered(t *testing.T) {
	p := &Provider{clusterName: "my-cluster"}
	class := &v1alpha1.LambdaNodeClass{
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"key"},
			UserData:     "region={{.Region}} cluster={{.ClusterName}}",
		},
	}
	nc := &v1.NodeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: "test-nc"},
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{Group: v1alpha1.Group, Kind: "LambdaNodeClass", Name: "test"},
		},
	}
	req, err := p.buildLaunchRequest(nc, class)
	if err != nil {
		t.Fatalf("buildLaunchRequest: %v", err)
	}
	if req.UserData != "region=us-east-3 cluster=my-cluster" {
		t.Fatalf("expected rendered userData, got: %s", req.UserData)
	}
}

// --- GetInstanceTypes filtering ---

func TestGetInstanceTypesFilteredByNodeClass(t *testing.T) {
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
			SSHKeyNames:  []string{"key"},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class).Build()

	cache := testInstanceTypeCache(map[string]lambdaclient.InstanceTypesItem{
		"gpu_1x_gh200": {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs}},
		"gpu_1x_a100":  {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_a100", Specs: lambdaclient.InstanceTypeSpec{VCPUs: 30, MemoryGiB: 200, StorageGiB: 512, GPUs: 1}}},
	})

	p := New(client, nil, nil, cache, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nodePool := &v1.NodePool{
		Spec: v1.NodePoolSpec{
			Template: v1.NodeClaimTemplate{
				Spec: v1.NodeClaimTemplateSpec{
					NodeClassRef: &v1.NodeClassReference{
						Group: v1alpha1.Group,
						Kind:  "LambdaNodeClass",
						Name:  "lambda-gh200",
					},
				},
			},
		},
	}

	its, err := p.GetInstanceTypes(context.Background(), nodePool)
	if err != nil {
		t.Fatalf("GetInstanceTypes: %v", err)
	}
	if len(its) != 1 {
		t.Fatalf("expected 1 filtered instance type, got %d", len(its))
	}
	if its[0].Name != "gpu_1x_gh200" {
		t.Fatalf("expected gpu_1x_gh200, got %s", its[0].Name)
	}
}

func TestGetInstanceTypesFilteredBySelector(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	gv := schema.GroupVersion{Group: apis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, gv)
	scheme.AddKnownTypes(gv, &v1.NodePool{}, &v1.NodePoolList{}, &v1.NodeClaim{}, &v1.NodeClaimList{})

	class := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "lambda-selector"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:               "us-east-3",
			SSHKeyNames:          []string{"key"},
			InstanceTypeSelector: []string{"gpu_1x_gh200", "gpu_1x_h100"},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class).Build()

	cache := testInstanceTypeCache(map[string]lambdaclient.InstanceTypesItem{
		"gpu_1x_gh200": {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs}},
		"gpu_1x_a100":  {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_a100", Specs: lambdaclient.InstanceTypeSpec{VCPUs: 30, MemoryGiB: 200, StorageGiB: 512, GPUs: 1}}},
		"gpu_1x_h100":  {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_h100", Specs: lambdaclient.InstanceTypeSpec{VCPUs: 26, MemoryGiB: 200, StorageGiB: 512, GPUs: 1}}},
	})

	p := New(client, nil, nil, cache, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nodePool := &v1.NodePool{
		Spec: v1.NodePoolSpec{
			Template: v1.NodeClaimTemplate{
				Spec: v1.NodeClaimTemplateSpec{
					NodeClassRef: &v1.NodeClassReference{
						Group: v1alpha1.Group,
						Kind:  "LambdaNodeClass",
						Name:  "lambda-selector",
					},
				},
			},
		},
	}

	its, err := p.GetInstanceTypes(context.Background(), nodePool)
	if err != nil {
		t.Fatalf("GetInstanceTypes: %v", err)
	}
	if len(its) != 2 {
		t.Fatalf("expected 2 instance types from selector, got %d", len(its))
	}
	names := map[string]bool{}
	for _, it := range its {
		names[it.Name] = true
	}
	if !names["gpu_1x_gh200"] || !names["gpu_1x_h100"] {
		t.Fatalf("expected gh200 and h100, got %v", names)
	}
	if names["gpu_1x_a100"] {
		t.Fatal("a100 should have been filtered out by selector")
	}
}

func TestGetInstanceTypesSelectorNoMatches(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	gv := schema.GroupVersion{Group: apis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, gv)
	scheme.AddKnownTypes(gv, &v1.NodePool{}, &v1.NodePoolList{}, &v1.NodeClaim{}, &v1.NodeClaimList{})

	class := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "lambda-selector"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:               "us-east-3",
			SSHKeyNames:          []string{"key"},
			InstanceTypeSelector: []string{"gpu_1x_nonexistent"},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class).Build()

	cache := testInstanceTypeCache(map[string]lambdaclient.InstanceTypesItem{
		"gpu_1x_gh200": {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs}},
	})

	p := New(client, nil, nil, cache, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nodePool := &v1.NodePool{
		Spec: v1.NodePoolSpec{
			Template: v1.NodeClaimTemplate{
				Spec: v1.NodeClaimTemplateSpec{
					NodeClassRef: &v1.NodeClassReference{
						Group: v1alpha1.Group,
						Kind:  "LambdaNodeClass",
						Name:  "lambda-selector",
					},
				},
			},
		},
	}

	its, err := p.GetInstanceTypes(context.Background(), nodePool)
	if err != nil {
		t.Fatalf("GetInstanceTypes: %v", err)
	}
	if len(its) != 0 {
		t.Fatalf("expected 0 instance types for non-matching selector, got %d", len(its))
	}
}

func TestGetInstanceTypesEmptySelectorReturnsAll(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	gv := schema.GroupVersion{Group: apis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, gv)
	scheme.AddKnownTypes(gv, &v1.NodePool{}, &v1.NodePoolList{}, &v1.NodeClaim{}, &v1.NodeClaimList{})

	// NodeClass with empty selector and no pinned type — should return all.
	class := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "lambda-all"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"key"},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class).Build()

	cache := testInstanceTypeCache(map[string]lambdaclient.InstanceTypesItem{
		"gpu_1x_gh200": {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs}},
		"gpu_1x_a100":  {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_a100", Specs: lambdaclient.InstanceTypeSpec{VCPUs: 30, MemoryGiB: 200, StorageGiB: 512, GPUs: 1}}},
	})

	p := New(client, nil, nil, cache, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nodePool := &v1.NodePool{
		Spec: v1.NodePoolSpec{
			Template: v1.NodeClaimTemplate{
				Spec: v1.NodeClaimTemplateSpec{
					NodeClassRef: &v1.NodeClassReference{
						Group: v1alpha1.Group,
						Kind:  "LambdaNodeClass",
						Name:  "lambda-all",
					},
				},
			},
		},
	}

	its, err := p.GetInstanceTypes(context.Background(), nodePool)
	if err != nil {
		t.Fatalf("GetInstanceTypes: %v", err)
	}
	if len(its) != 2 {
		t.Fatalf("expected 2 instance types (empty selector = all), got %d", len(its))
	}
}

func TestGetInstanceTypesUnfiltered(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	gv := schema.GroupVersion{Group: apis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, gv)
	scheme.AddKnownTypes(gv, &v1.NodePool{}, &v1.NodePoolList{}, &v1.NodeClaim{}, &v1.NodeClaimList{})

	// NodeClass with no instanceType — should return all types.
	class := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "lambda-generic"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"key"},
		},
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(class).Build()

	cache := testInstanceTypeCache(map[string]lambdaclient.InstanceTypesItem{
		"gpu_1x_gh200": {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs}},
		"gpu_1x_a100":  {InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_a100", Specs: lambdaclient.InstanceTypeSpec{VCPUs: 30, MemoryGiB: 200, StorageGiB: 512, GPUs: 1}}},
	})

	p := New(client, nil, nil, cache, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nodePool := &v1.NodePool{
		Spec: v1.NodePoolSpec{
			Template: v1.NodeClaimTemplate{
				Spec: v1.NodeClaimTemplateSpec{
					NodeClassRef: &v1.NodeClassReference{
						Group: v1alpha1.Group,
						Kind:  "LambdaNodeClass",
						Name:  "lambda-generic",
					},
				},
			},
		},
	}

	its, err := p.GetInstanceTypes(context.Background(), nodePool)
	if err != nil {
		t.Fatalf("GetInstanceTypes: %v", err)
	}
	if len(its) != 2 {
		t.Fatalf("expected 2 unfiltered instance types, got %d", len(its))
	}
}

// --- IsDrifted error path ---

func TestProviderIsDriftedNodeClassNotFound(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	gv := schema.GroupVersion{Group: apis.Group, Version: "v1"}
	metav1.AddToGroupVersion(scheme, gv)
	scheme.AddKnownTypes(gv, &v1.NodePool{}, &v1.NodePoolList{}, &v1.NodeClaim{}, &v1.NodeClaimList{})

	// No nodeclass in the fake client.
	client := fake.NewClientBuilder().WithScheme(scheme).Build()
	fakeAPI := &fakeLambda{}
	p := New(client, fakeAPI, fakeAPI, nil, NewUnavailableOfferings(5*time.Minute), "test", testLog)

	nc := &v1.NodeClaim{
		Spec: v1.NodeClaimSpec{
			NodeClassRef: &v1.NodeClassReference{
				Group: v1alpha1.Group,
				Kind:  "LambdaNodeClass",
				Name:  "nonexistent",
			},
		},
	}
	_, err := p.IsDrifted(context.Background(), nc)
	if err == nil {
		t.Fatal("expected error for missing nodeclass")
	}
}

func TestCreateCapacityErrorMarksOfferingUnavailable(t *testing.T) {
	class, nodePool, scheme := testSchemeAndClass(t)
	client := fake.NewClientBuilder().WithScheme(&scheme).WithObjects(class, nodePool).Build()
	fakeAPI := &fakeLambda{
		launchErr: fmt.Errorf("lambda api POST /api/v1/instance-operations/launch failed: 503: insufficient capacity"),
	}
	uo := NewUnavailableOfferings(5 * time.Minute)
	p := New(client, fakeAPI, fakeAPI, nil, uo, "test", testLog)

	// Create should fail with capacity error.
	_, err := p.Create(context.Background(), testNodeClaim())
	if !cloudprovider.IsInsufficientCapacityError(err) {
		t.Fatalf("expected InsufficientCapacityError, got %v", err)
	}

	// The offering should now be marked unavailable.
	if !uo.IsUnavailable("gpu_1x_gh200", "us-east-3") {
		t.Fatal("expected offering to be marked unavailable after capacity error")
	}

	// GetInstanceTypes should reflect the unavailability.
	cache := testInstanceTypeCache(map[string]lambdaclient.InstanceTypesItem{
		"gpu_1x_gh200": {
			InstanceType: lambdaclient.InstanceTypeRef{Name: "gpu_1x_gh200", Specs: gh200Specs},
			Regions:      []lambdaclient.Region{{Name: "us-east-3"}},
		},
	})
	p.cache = cache

	its, err := p.GetInstanceTypes(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetInstanceTypes: %v", err)
	}
	if len(its) != 1 {
		t.Fatalf("expected 1 instance type, got %d", len(its))
	}
	if len(its[0].Offerings) != 1 {
		t.Fatalf("expected 1 offering, got %d", len(its[0].Offerings))
	}
	if its[0].Offerings[0].Available {
		t.Fatal("expected offering to be unavailable after capacity error")
	}
}
