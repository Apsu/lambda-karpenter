package main

import (
	"os"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/evecallicoat/lambda-karpenter/api/v1alpha1"
	"github.com/evecallicoat/lambda-karpenter/internal/config"
	"github.com/evecallicoat/lambda-karpenter/internal/controller"
	"github.com/evecallicoat/lambda-karpenter/internal/lambdaclient"
	"github.com/evecallicoat/lambda-karpenter/internal/provider"
	"github.com/evecallicoat/lambda-karpenter/internal/ratelimit"
	"sigs.k8s.io/karpenter/pkg/cloudprovider/overlay"
	"sigs.k8s.io/karpenter/pkg/controllers"
	"sigs.k8s.io/karpenter/pkg/controllers/state"
	"sigs.k8s.io/karpenter/pkg/operator"
)

func main() {
	devMode := strings.EqualFold(os.Getenv("LOG_DEV_MODE"), "true")
	log.SetLogger(zap.New(zap.UseDevMode(devMode)))

	cfg, err := config.Load()
	if err != nil {
		log.Log.Error(err, "invalid configuration")
		os.Exit(1)
	}

	log.Log.Info("starting lambda-karpenter",
		"clusterName", cfg.ClusterName,
		"baseURL", cfg.BaseURL,
		"rps", cfg.RPS,
		"launchMinInterval", cfg.LaunchMinInterval,
		"instanceTypeCacheTTL", cfg.InstanceTypeCacheTTL,
	)

	ctx, op := operator.NewOperator()

	if err := v1alpha1.AddToScheme(op.GetScheme()); err != nil {
		log.Log.Error(err, "add scheme")
		os.Exit(1)
	}

	limiter := ratelimit.New(cfg.RPS, cfg.LaunchMinInterval)
	lambdaAPI, err := lambdaclient.New(cfg.BaseURL, cfg.APIToken, limiter)
	if err != nil {
		log.Log.Error(err, "create lambda client")
		os.Exit(1)
	}

	cache := lambdaclient.NewInstanceTypeCache(lambdaAPI, cfg.InstanceTypeCacheTTL)
	listCache := lambdaclient.NewInstanceListCache(lambdaAPI, 5*time.Second)
	unavailableOfferings := provider.NewUnavailableOfferings(cfg.UnavailableOfferingsTTL)
	cloudProvider := provider.New(op.GetClient(), lambdaAPI, listCache, cache, unavailableOfferings, cfg.ClusterName, log.Log)
	overlayProvider := overlay.Decorate(cloudProvider, op.GetClient(), op.InstanceTypeStore)
	clusterState := state.NewCluster(op.Clock, op.GetClient(), overlayProvider)

	if err := (&controller.LambdaNodeClassReconciler{Client: op.GetClient()}).SetupWithManager(op.Manager); err != nil {
		log.Log.Error(err, "unable to create LambdaNodeClass controller")
		os.Exit(1)
	}

	op.WithControllers(ctx, controllers.NewControllers(
		ctx,
		op.Manager,
		op.Clock,
		op.GetClient(),
		op.EventRecorder,
		overlayProvider,
		cloudProvider,
		clusterState,
		op.InstanceTypeStore,
	)...).Start(ctx)
}
