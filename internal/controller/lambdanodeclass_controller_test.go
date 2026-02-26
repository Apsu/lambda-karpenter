package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/awslabs/operatorpkg/status"
	"github.com/lambdal/lambda-karpenter/api/v1alpha1"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	_ = corev1.AddToScheme(s)
	return s
}

func reconcileNC(t *testing.T, nc *v1alpha1.LambdaNodeClass) *v1alpha1.LambdaNodeClass {
	return reconcileNCWithResolver(t, nc, nil)
}

func reconcileNCWithResolver(t *testing.T, nc *v1alpha1.LambdaNodeClass, resolver ImageResolver, extraObjs ...runtime.Object) *v1alpha1.LambdaNodeClass {
	t.Helper()
	scheme := testScheme()
	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nc).WithStatusSubresource(nc)
	for _, obj := range extraObjs {
		builder = builder.WithRuntimeObjects(obj)
	}
	client := builder.Build()
	r := &LambdaNodeClassReconciler{Client: client, ImageResolver: resolver}

	_, err := r.Reconcile(context.Background(), reconcile.Request{
		NamespacedName: types.NamespacedName{Name: nc.Name},
	})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	var result v1alpha1.LambdaNodeClass
	if err := client.Get(context.Background(), types.NamespacedName{Name: nc.Name}, &result); err != nil {
		t.Fatalf("Get after reconcile: %v", err)
	}
	return &result
}

func findCondition(nc *v1alpha1.LambdaNodeClass, condType string) *status.Condition {
	for i := range nc.Status.Conditions {
		if nc.Status.Conditions[i].Type == condType {
			return &nc.Status.Conditions[i]
		}
	}
	return nil
}

// fakeImageResolver implements ImageResolver for testing.
type fakeImageResolver struct {
	images []lambdaclient.Image
	err    error
}

func (f *fakeImageResolver) ListImages(ctx context.Context) ([]lambdaclient.Image, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.images, nil
}

func TestReconcileValidNodeClass(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "valid"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"my-key"},
		},
	}
	result := reconcileNC(t, nc)

	cond := findCondition(result, string(status.ConditionReady))
	if cond == nil {
		t.Fatal("expected Ready condition")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Ready=True, got %s: %s", cond.Status, cond.Message)
	}
	if result.Status.LastValidatedAt == nil {
		t.Fatal("expected LastValidatedAt to be set")
	}
}

func TestReconcileMissingRegion(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "no-region"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"my-key"},
		},
	}
	result := reconcileNC(t, nc)

	cond := findCondition(result, string(status.ConditionReady))
	if cond == nil {
		t.Fatal("expected Ready condition")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %s", cond.Status)
	}
	if cond.Reason != "ValidationFailed" {
		t.Fatalf("expected reason ValidationFailed, got %s", cond.Reason)
	}
}

func TestReconcileMissingInstanceType(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "no-it"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"my-key"},
		},
	}
	result := reconcileNC(t, nc)

	cond := findCondition(result, string(status.ConditionReady))
	if cond == nil {
		t.Fatal("expected Ready condition")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Ready=True (instanceType is optional), got %s: %s", cond.Status, cond.Message)
	}
}

func TestReconcileMissingSSHKeys(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "no-ssh"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
		},
	}
	result := reconcileNC(t, nc)

	cond := findCondition(result, string(status.ConditionReady))
	if cond == nil {
		t.Fatal("expected Ready condition")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %s", cond.Status)
	}
}

func TestReconcileImageBothIDAndFamily(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "bad-image"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"my-key"},
			Image:        &v1alpha1.LambdaImage{ID: "img-123", Family: "ubuntu"},
		},
	}
	result := reconcileNC(t, nc)

	cond := findCondition(result, string(status.ConditionReady))
	if cond == nil {
		t.Fatal("expected Ready condition")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False, got %s", cond.Status)
	}
}

func TestReconcileInstanceTypeSelectorAccepted(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "its-accepted"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:               "us-east-3",
			SSHKeyNames:          []string{"my-key"},
			InstanceTypeSelector: []string{"gpu_1x_gh200", "gpu_1x_a10"},
		},
	}
	result := reconcileNC(t, nc)

	cond := findCondition(result, string(status.ConditionReady))
	if cond == nil {
		t.Fatal("expected Ready condition")
	}
	if cond.Status != metav1.ConditionTrue {
		t.Fatalf("expected Ready=True (instanceTypeSelector is now supported), got %s: %s", cond.Status, cond.Message)
	}
}

func TestReconcileImageResolutionID(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "img-id"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"my-key"},
			Image:        &v1alpha1.LambdaImage{ID: "img-123"},
		},
	}
	result := reconcileNC(t, nc)

	if result.Status.ResolvedImageID != "img-123" {
		t.Fatalf("expected ResolvedImageID=img-123, got %s", result.Status.ResolvedImageID)
	}
	if result.Status.ResolvedImageFamily != "" {
		t.Fatalf("expected empty ResolvedImageFamily, got %s", result.Status.ResolvedImageFamily)
	}
}

func TestReconcileImageResolutionFamily(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "img-family"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"my-key"},
			Image:        &v1alpha1.LambdaImage{Family: "lambda-stack-24-04"},
		},
	}
	// Without resolver, falls through to pass-through behavior.
	result := reconcileNC(t, nc)

	if result.Status.ResolvedImageFamily != "lambda-stack-24-04" {
		t.Fatalf("expected ResolvedImageFamily=lambda-stack-24-04, got %s", result.Status.ResolvedImageFamily)
	}
	if result.Status.ResolvedImageID != "" {
		t.Fatalf("expected empty ResolvedImageID (no resolver), got %s", result.Status.ResolvedImageID)
	}
}

func TestReconcileImageResolutionFamilyWithResolver(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "img-family-resolved"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"my-key"},
			Image:        &v1alpha1.LambdaImage{Family: "lambda-stack-24-04"},
		},
	}

	resolver := &fakeImageResolver{
		images: []lambdaclient.Image{
			{ID: "img-old", Family: "lambda-stack-24-04", Region: lambdaclient.Region{Name: "us-east-3"}, UpdatedTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
			{ID: "img-latest", Family: "lambda-stack-24-04", Region: lambdaclient.Region{Name: "us-east-3"}, UpdatedTime: time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)},
			{ID: "img-wrong-region", Family: "lambda-stack-24-04", Region: lambdaclient.Region{Name: "us-west-1"}, UpdatedTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
			{ID: "img-wrong-family", Family: "ubuntu-22-04", Region: lambdaclient.Region{Name: "us-east-3"}, UpdatedTime: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		},
	}

	result := reconcileNCWithResolver(t, nc, resolver)

	if result.Status.ResolvedImageID != "img-latest" {
		t.Fatalf("expected ResolvedImageID=img-latest, got %s", result.Status.ResolvedImageID)
	}
	if result.Status.ResolvedImageFamily != "lambda-stack-24-04" {
		t.Fatalf("expected ResolvedImageFamily=lambda-stack-24-04, got %s", result.Status.ResolvedImageFamily)
	}
}

func TestReconcileImageResolutionFamilyNoMatch(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "img-nomatch"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"my-key"},
			Image:        &v1alpha1.LambdaImage{Family: "nonexistent-family"},
		},
	}

	resolver := &fakeImageResolver{
		images: []lambdaclient.Image{
			{ID: "img-1", Family: "ubuntu-22-04", Region: lambdaclient.Region{Name: "us-east-3"}, UpdatedTime: time.Now()},
		},
	}

	result := reconcileNCWithResolver(t, nc, resolver)

	if result.Status.ResolvedImageID != "" {
		t.Fatalf("expected empty ResolvedImageID for no match, got %s", result.Status.ResolvedImageID)
	}
	if result.Status.ResolvedImageFamily != "nonexistent-family" {
		t.Fatalf("expected ResolvedImageFamily=nonexistent-family, got %s", result.Status.ResolvedImageFamily)
	}
}

func TestReconcileNoImageClearsStatus(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "no-image"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:       "us-east-3",
			InstanceType: "gpu_1x_gh200",
			SSHKeyNames:  []string{"my-key"},
		},
		Status: v1alpha1.LambdaNodeClassStatus{
			ResolvedImageID:     "old-id",
			ResolvedImageFamily: "old-family",
		},
	}
	result := reconcileNC(t, nc)

	if result.Status.ResolvedImageID != "" {
		t.Fatalf("expected empty ResolvedImageID, got %s", result.Status.ResolvedImageID)
	}
	if result.Status.ResolvedImageFamily != "" {
		t.Fatalf("expected empty ResolvedImageFamily, got %s", result.Status.ResolvedImageFamily)
	}
}

// --- Phase 4: UserData from ConfigMap tests ---

func TestReconcileUserDataMutuallyExclusive(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "both-userdata"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"my-key"},
			UserData:    "#cloud-config",
			UserDataFrom: []v1alpha1.UserDataSource{
				{Inline: "extra-data"},
			},
		},
	}
	result := reconcileNC(t, nc)

	cond := findCondition(result, string(status.ConditionReady))
	if cond == nil {
		t.Fatal("expected Ready condition")
	}
	if cond.Status != metav1.ConditionFalse {
		t.Fatalf("expected Ready=False for mutually exclusive, got %s", cond.Status)
	}
}

func TestReconcileUserDataFromInline(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "ud-inline"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"my-key"},
			UserDataFrom: []v1alpha1.UserDataSource{
				{Inline: "part-1"},
				{Inline: "part-2"},
			},
		},
	}
	result := reconcileNC(t, nc)

	expected := "part-1\npart-2"
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(expected)))

	if result.Status.ResolvedUserData != expected {
		t.Fatalf("expected resolved userData %q, got %q", expected, result.Status.ResolvedUserData)
	}
	if result.Status.ResolvedUserDataHash != expectedHash {
		t.Fatalf("expected hash %q, got %q", expectedHash, result.Status.ResolvedUserDataHash)
	}
}

func TestReconcileUserDataFromConfigMap(t *testing.T) {
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: "my-userdata", Namespace: "karpenter"},
		Data: map[string]string{
			"join.sh": "#!/bin/bash\necho join",
		},
	}

	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "ud-cm"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"my-key"},
			UserDataFrom: []v1alpha1.UserDataSource{
				{Inline: "#cloud-config"},
				{ConfigMapRef: &v1alpha1.ConfigMapKeyRef{Name: "my-userdata", Namespace: "karpenter", Key: "join.sh"}},
			},
		},
	}
	result := reconcileNCWithResolver(t, nc, nil, cm)

	expected := "#cloud-config\n#!/bin/bash\necho join"
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte(expected)))

	if result.Status.ResolvedUserData != expected {
		t.Fatalf("expected resolved userData %q, got %q", expected, result.Status.ResolvedUserData)
	}
	if result.Status.ResolvedUserDataHash != expectedHash {
		t.Fatalf("expected hash %q, got %q", expectedHash, result.Status.ResolvedUserDataHash)
	}
}

func TestReconcileInlineUserDataHash(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "ud-inline-hash"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"my-key"},
			UserData:    "#!/bin/bash\necho hello",
		},
	}
	result := reconcileNC(t, nc)

	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte("#!/bin/bash\necho hello")))
	if result.Status.ResolvedUserDataHash != expectedHash {
		t.Fatalf("expected hash %q, got %q", expectedHash, result.Status.ResolvedUserDataHash)
	}
	// ResolvedUserData should remain empty for inline — the content is in spec.
	if result.Status.ResolvedUserData != "" {
		t.Fatalf("expected empty ResolvedUserData for inline userData, got %q", result.Status.ResolvedUserData)
	}
}

func TestReconcileInlineUserDataHashChangesOnUpdate(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "ud-inline-change"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"my-key"},
			UserData:    "#!/bin/bash\necho v1",
		},
	}
	result1 := reconcileNC(t, nc)
	hash1 := result1.Status.ResolvedUserDataHash

	nc.Spec.UserData = "#!/bin/bash\necho v2"
	result2 := reconcileNC(t, nc)
	hash2 := result2.Status.ResolvedUserDataHash

	if hash1 == hash2 {
		t.Fatal("expected different hashes for different userData content")
	}
	expectedHash2 := fmt.Sprintf("%x", sha256.Sum256([]byte("#!/bin/bash\necho v2")))
	if hash2 != expectedHash2 {
		t.Fatalf("expected hash %q, got %q", expectedHash2, hash2)
	}
}

func TestReconcileUserDataFromClearsWhenEmpty(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "ud-clear"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:      "us-east-3",
			SSHKeyNames: []string{"my-key"},
		},
		Status: v1alpha1.LambdaNodeClassStatus{
			ResolvedUserData:     "old-data",
			ResolvedUserDataHash: "old-hash",
		},
	}
	result := reconcileNC(t, nc)

	if result.Status.ResolvedUserData != "" {
		t.Fatalf("expected empty ResolvedUserData, got %q", result.Status.ResolvedUserData)
	}
	if result.Status.ResolvedUserDataHash != "" {
		t.Fatalf("expected empty ResolvedUserDataHash, got %q", result.Status.ResolvedUserDataHash)
	}
}
