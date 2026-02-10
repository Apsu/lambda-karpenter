package controller

import (
	"context"
	"fmt"

	"github.com/awslabs/operatorpkg/status"
	"github.com/evecallicoat/lambda-karpenter/api/v1alpha1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// LambdaNodeClassReconciler validates LambdaNodeClass resources.
type LambdaNodeClassReconciler struct {
	client.Client
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
	if err := r.Status().Update(ctx, &nc); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("reconciled LambdaNodeClass", "name", nc.Name, "ready", cond.IsTrue(status.ConditionReady))
	return ctrl.Result{}, nil
}

func (r *LambdaNodeClassReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.LambdaNodeClass{}).
		Complete(r)
}

func validateNodeClass(nc *v1alpha1.LambdaNodeClass) error {
	if nc.Spec.Region == "" {
		return fmt.Errorf("spec.region is required")
	}
	if nc.Spec.InstanceType == "" {
		return fmt.Errorf("spec.instanceType is required")
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
	return nil
}
