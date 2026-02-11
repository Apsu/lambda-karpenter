package controller

import (
	"context"
	"testing"

	"github.com/awslabs/operatorpkg/status"
	"github.com/evecallicoat/lambda-karpenter/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = v1alpha1.AddToScheme(s)
	return s
}

func reconcileNC(t *testing.T, nc *v1alpha1.LambdaNodeClass) *v1alpha1.LambdaNodeClass {
	t.Helper()
	scheme := testScheme()
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(nc).WithStatusSubresource(nc).Build()
	r := &LambdaNodeClassReconciler{Client: client}

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

func TestReconcileInstanceTypeSelectorUnsupported(t *testing.T) {
	nc := &v1alpha1.LambdaNodeClass{
		ObjectMeta: metav1.ObjectMeta{Name: "its-unsupported"},
		Spec: v1alpha1.LambdaNodeClassSpec{
			Region:               "us-east-3",
			InstanceType:         "gpu_1x_gh200",
			SSHKeyNames:          []string{"my-key"},
			InstanceTypeSelector: []string{"gpu_1x_gh200", "gpu_1x_a10"},
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
	result := reconcileNC(t, nc)

	if result.Status.ResolvedImageFamily != "lambda-stack-24-04" {
		t.Fatalf("expected ResolvedImageFamily=lambda-stack-24-04, got %s", result.Status.ResolvedImageFamily)
	}
	if result.Status.ResolvedImageID != "" {
		t.Fatalf("expected empty ResolvedImageID, got %s", result.Status.ResolvedImageID)
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
