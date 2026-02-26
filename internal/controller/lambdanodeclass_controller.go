package controller

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/awslabs/operatorpkg/status"
	"github.com/lambdal/lambda-karpenter/api/v1alpha1"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ImageResolver resolves image families to concrete image IDs.
type ImageResolver interface {
	ListImages(ctx context.Context) ([]lambdaclient.Image, error)
}

// LambdaNodeClassReconciler validates LambdaNodeClass resources.
type LambdaNodeClassReconciler struct {
	client.Client
	ImageResolver ImageResolver
}

func (r *LambdaNodeClassReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var nc v1alpha1.LambdaNodeClass
	if err := r.Get(ctx, req.NamespacedName, &nc); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	cond := status.NewReadyConditions(status.ConditionReady).For(&nc)
	if err := validateNodeClass(&nc); err != nil {
		cond.SetFalse(status.ConditionReady, "ValidationFailed", err.Error())
	} else {
		cond.SetTrue(status.ConditionReady)
	}

	// Resolve image: if family is set and we have an ImageResolver, resolve to concrete ID.
	if err := r.resolveImage(ctx, &nc); err != nil {
		logger.Error(err, "image resolution failed", "name", nc.Name)
	}

	// Resolve userDataFrom: fetch ConfigMap contents, concatenate, compute hash.
	if err := r.resolveUserData(ctx, &nc); err != nil {
		logger.Error(err, "userData resolution failed", "name", nc.Name)
		cond.SetFalse(status.ConditionReady, "UserDataResolutionFailed", err.Error())
	}

	now := metav1.NewTime(time.Now())
	nc.Status.LastValidatedAt = &now

	if err := r.Status().Update(ctx, &nc); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("reconciled LambdaNodeClass", "name", nc.Name, "ready", cond.IsTrue(status.ConditionReady))
	return ctrl.Result{}, nil
}

func (r *LambdaNodeClassReconciler) resolveImage(ctx context.Context, nc *v1alpha1.LambdaNodeClass) error {
	if nc.Spec.Image == nil {
		nc.Status.ResolvedImageID = ""
		nc.Status.ResolvedImageFamily = ""
		return nil
	}

	if nc.Spec.Image.ID != "" {
		nc.Status.ResolvedImageID = nc.Spec.Image.ID
		nc.Status.ResolvedImageFamily = ""
		return nil
	}

	if nc.Spec.Image.Family != "" && r.ImageResolver != nil {
		images, err := r.ImageResolver.ListImages(ctx)
		if err != nil {
			nc.Status.ResolvedImageID = ""
			nc.Status.ResolvedImageFamily = nc.Spec.Image.Family
			return err
		}

		imageID := resolveLatestImageByFamily(images, nc.Spec.Image.Family, nc.Spec.Region)
		if imageID != "" {
			nc.Status.ResolvedImageID = imageID
		} else {
			nc.Status.ResolvedImageID = ""
		}
		nc.Status.ResolvedImageFamily = nc.Spec.Image.Family
		return nil
	}

	nc.Status.ResolvedImageID = ""
	nc.Status.ResolvedImageFamily = nc.Spec.Image.Family
	return nil
}

func (r *LambdaNodeClassReconciler) resolveUserData(ctx context.Context, nc *v1alpha1.LambdaNodeClass) error {
	if len(nc.Spec.UserDataFrom) == 0 {
		nc.Status.ResolvedUserData = ""
		// Compute hash for inline spec.userData so drift detection works
		// for both the inline and userDataFrom paths.
		if nc.Spec.UserData != "" {
			nc.Status.ResolvedUserDataHash = fmt.Sprintf("%x", sha256.Sum256([]byte(nc.Spec.UserData)))
		} else {
			nc.Status.ResolvedUserDataHash = ""
		}
		return nil
	}

	var parts []string
	for i, src := range nc.Spec.UserDataFrom {
		if src.Inline != "" {
			parts = append(parts, src.Inline)
		} else if src.ConfigMapRef != nil {
			ref := src.ConfigMapRef
			var cm corev1.ConfigMap
			if err := r.Client.Get(ctx, types.NamespacedName{Name: ref.Name, Namespace: ref.Namespace}, &cm); err != nil {
				return fmt.Errorf("userDataFrom[%d]: configMapRef %s/%s: %w", i, ref.Namespace, ref.Name, err)
			}
			val, ok := cm.Data[ref.Key]
			if !ok {
				return fmt.Errorf("userDataFrom[%d]: key %q not found in configmap %s/%s", i, ref.Key, ref.Namespace, ref.Name)
			}
			parts = append(parts, val)
		}
	}

	resolved := strings.Join(parts, "\n")
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(resolved)))

	nc.Status.ResolvedUserData = resolved
	nc.Status.ResolvedUserDataHash = hash
	return nil
}

// resolveLatestImageByFamily finds the latest image matching the given family and region.
func resolveLatestImageByFamily(images []lambdaclient.Image, family, region string) string {
	var bestID string
	var bestTime time.Time

	for _, img := range images {
		if img.Family != family {
			continue
		}
		if region != "" && img.Region.Name != "" && img.Region.Name != region {
			continue
		}
		if img.UpdatedTime.After(bestTime) || (img.UpdatedTime.Equal(bestTime) && img.ID > bestID) {
			bestTime = img.UpdatedTime
			bestID = img.ID
		}
	}
	return bestID
}

func (r *LambdaNodeClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.LambdaNodeClass{}).
		Watches(&corev1.ConfigMap{}, handler.EnqueueRequestsFromMapFunc(r.configMapToNodeClass)).
		Complete(r)
}

// configMapToNodeClass maps a ConfigMap change to the LambdaNodeClass resources
// that reference it via spec.userDataFrom[].configMapRef. This ensures the
// controller re-resolves userData (and recomputes the hash) when a referenced
// ConfigMap is updated.
func (r *LambdaNodeClassReconciler) configMapToNodeClass(ctx context.Context, obj client.Object) []ctrl.Request {
	logger := log.FromContext(ctx)
	var nodeClasses v1alpha1.LambdaNodeClassList
	if err := r.List(ctx, &nodeClasses); err != nil {
		logger.Error(err, "failed to list LambdaNodeClasses for ConfigMap watch")
		return nil
	}

	var requests []ctrl.Request
	for _, nc := range nodeClasses.Items {
		for _, src := range nc.Spec.UserDataFrom {
			if src.ConfigMapRef != nil && src.ConfigMapRef.Name == obj.GetName() && src.ConfigMapRef.Namespace == obj.GetNamespace() {
				requests = append(requests, ctrl.Request{NamespacedName: types.NamespacedName{Name: nc.Name}})
				break
			}
		}
	}
	return requests
}

func validateNodeClass(nc *v1alpha1.LambdaNodeClass) error {
	if nc.Spec.Region == "" {
		return fmt.Errorf("spec.region is required")
	}
	if len(nc.Spec.SSHKeyNames) == 0 {
		return fmt.Errorf("spec.sshKeyNames must include at least one entry")
	}
	if nc.Spec.Image != nil {
		hasID := nc.Spec.Image.ID != ""
		hasFamily := nc.Spec.Image.Family != ""
		if hasID == hasFamily {
			return fmt.Errorf("spec.image must set exactly one of id or family")
		}
	}
	if nc.Spec.UserData != "" && len(nc.Spec.UserDataFrom) > 0 {
		return fmt.Errorf("spec.userData and spec.userDataFrom are mutually exclusive")
	}
	return nil
}
