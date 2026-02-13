package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/awslabs/operatorpkg/serrors"
	"github.com/awslabs/operatorpkg/status"
	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/lambdal/lambda-karpenter/api/v1alpha1"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
	v1 "sigs.k8s.io/karpenter/pkg/apis/v1"
	"sigs.k8s.io/karpenter/pkg/cloudprovider"
	"sigs.k8s.io/karpenter/pkg/scheduling"
)

const (
	providerName     = "lambda"
	providerIDPrefix = "lambda://"
	tagNodeClaim     = "karpenter-sh-nodeclaim"
	tagNodePool      = "karpenter-sh-nodepool"
	tagNodeClass     = "karpenter-lambda-cloud-lambdanodeclass"
	tagCluster       = "karpenter-sh-cluster"
	tagImageID       = "karpenter-lambda-cloud-image-id"
	tagUserDataHash  = "karpenter-lambda-cloud-userdata-hash"
)

type LambdaAPI interface {
	ListInstances(ctx context.Context) ([]lambdaclient.Instance, error)
	GetInstance(ctx context.Context, id string) (*lambdaclient.Instance, error)
	LaunchInstance(ctx context.Context, req lambdaclient.LaunchRequest) ([]string, error)
	TerminateInstance(ctx context.Context, id string) error
}

// InstanceLister abstracts cached instance listing. Implemented by
// *lambdaclient.InstanceListCache and directly by LambdaAPI for testing.
type InstanceLister interface {
	List(ctx context.Context) ([]lambdaclient.Instance, error)
}

var _ cloudprovider.CloudProvider = (*Provider)(nil)

// Provider implements the Karpenter CloudProvider interface for Lambda Cloud.
type Provider struct {
	kubeClient           client.Client
	lambda               LambdaAPI
	listCache            InstanceLister
	cache                *lambdaclient.InstanceTypeCache
	unavailableOfferings *UnavailableOfferings
	clusterName          string
	log                  logr.Logger
}

func New(kubeClient client.Client, lambda LambdaAPI, listCache InstanceLister, cache *lambdaclient.InstanceTypeCache, unavailableOfferings *UnavailableOfferings, clusterName string, log logr.Logger) *Provider {
	return &Provider{
		kubeClient:           kubeClient,
		lambda:               lambda,
		listCache:            listCache,
		cache:                cache,
		unavailableOfferings: unavailableOfferings,
		clusterName:          clusterName,
		log:                  log.WithName("lambda-provider"),
	}
}

func (p *Provider) Name() string {
	return providerName
}

func (p *Provider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&v1alpha1.LambdaNodeClass{}}
}

func (p *Provider) RepairPolicies() []cloudprovider.RepairPolicy {
	return []cloudprovider.RepairPolicy{
		{
			ConditionType:   "Ready",
			ConditionStatus: corev1.ConditionFalse,
		},
	}
}

func (p *Provider) IsDrifted(ctx context.Context, nodeClaim *v1.NodeClaim) (cloudprovider.DriftReason, error) {
	class, err := p.resolveNodeClass(ctx, nodeClaim)
	if err != nil {
		return "", err
	}

	// Check region drift
	if regionLabel, ok := nodeClaim.Labels[corev1.LabelTopologyRegion]; ok {
		if class.Spec.Region != "" && regionLabel != class.Spec.Region {
			return "RegionDrifted", nil
		}
	}

	// Check instance type drift
	if itLabel, ok := nodeClaim.Labels[corev1.LabelInstanceTypeStable]; ok {
		if class.Spec.InstanceType != "" && itLabel != class.Spec.InstanceType {
			return "InstanceTypeDrifted", nil
		}
	}

	// Check image drift: compare the image ID stored at launch time against
	// the current resolved image ID (from status, set by controller).
	// Falls back to spec if status is not yet populated.
	if class.Spec.Image != nil && nodeClaim.Status.ImageID != "" {
		wantImageID := class.Status.ResolvedImageID
		if wantImageID == "" {
			// Fallback: controller hasn't resolved yet, use spec directly.
			wantImageID = class.Spec.Image.ID
			if wantImageID == "" {
				wantImageID = class.Spec.Image.Family
			}
		}
		if wantImageID != "" && nodeClaim.Status.ImageID != wantImageID {
			return "ImageDrifted", nil
		}
	}

	// Check userData drift: compare the hash stored at launch time (in instance tag)
	// against the current resolved hash in the NodeClass status.
	if class.Status.ResolvedUserDataHash != "" {
		if inst, err := p.resolveInstanceForNodeClaim(ctx, nodeClaim); err == nil && inst != nil {
			tags := tagsToMap(inst.Tags)
			if launchHash := tags[tagUserDataHash]; launchHash != "" && launchHash != class.Status.ResolvedUserDataHash {
				return "UserDataDrifted", nil
			}
		}
	}

	return "", nil
}

func (p *Provider) Create(ctx context.Context, nodeClaim *v1.NodeClaim) (*v1.NodeClaim, error) {
	nc := nodeClaim.DeepCopy()
	log := p.log.WithValues("nodeclaim", nc.Name)

	class, err := p.resolveNodeClass(ctx, nc)
	if err != nil {
		return nil, err
	}
	if len(class.Spec.SSHKeyNames) == 0 {
		return nil, serrors.Wrap(fmt.Errorf("sshKeyNames must include at least one entry"), "nodeclass", class.Name)
	}

	existing, err := p.findByNodeClaimTag(ctx, nc.Name, isNonViableInstance)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		log.V(1).Info("found existing instance by tag", "instanceID", existing.ID)
		return p.nodeClaimFromInstance(nc, existing), nil
	}

	launchReq, err := p.buildLaunchRequest(nc, class)
	if err != nil {
		return nil, err
	}

	log.Info("launching instance", "region", launchReq.RegionName, "instanceType", launchReq.InstanceTypeName)
	ids, err := p.lambda.LaunchInstance(ctx, launchReq)
	if err != nil {
		instanceCreateTotal.WithLabelValues("error").Inc()
		if lambdaclient.IsCapacityError(err) {
			p.unavailableOfferings.MarkUnavailable(launchReq.InstanceTypeName, launchReq.RegionName)
			log.Info("marked offering unavailable", "instanceType", launchReq.InstanceTypeName, "region", launchReq.RegionName)
			return nil, cloudprovider.NewInsufficientCapacityError(err)
		}
		return nil, err
	}
	if len(ids) == 0 {
		instanceCreateTotal.WithLabelValues("error").Inc()
		return nil, fmt.Errorf("lambda launch returned no instance ids")
	}
	instanceCreateTotal.WithLabelValues("success").Inc()

	log.Info("instance launched", "instanceID", ids[0])
	inst, err := p.lambda.GetInstance(ctx, ids[0])
	if err != nil {
		return nil, err
	}

	return p.nodeClaimFromInstance(nc, inst), nil
}

func (p *Provider) Delete(ctx context.Context, nodeClaim *v1.NodeClaim) error {
	log := p.log.WithValues("nodeclaim", nodeClaim.Name)
	inst, err := p.resolveInstanceForNodeClaim(ctx, nodeClaim)
	if err != nil {
		return err
	}
	if inst == nil {
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance not found"))
	}

	switch inst.Status {
	case "terminated", "preempted":
		// Instance is truly gone.
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance %s is %s", inst.ID, inst.Status))
	case "terminating":
		// Termination in progress; tell Karpenter to check back later.
		return nil
	default:
		// Active, booting, unhealthy, etc. — proceed with termination.
		log.Info("terminating instance", "instanceID", inst.ID, "status", inst.Status)
		if err := p.lambda.TerminateInstance(ctx, inst.ID); err != nil {
			instanceDeleteTotal.WithLabelValues("error").Inc()
			return err
		}
		instanceDeleteTotal.WithLabelValues("success").Inc()
		return nil
	}
}

func (p *Provider) Get(ctx context.Context, providerID string) (*v1.NodeClaim, error) {
	key := parseProviderID(providerID)
	if key == "" {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("invalid provider id"))
	}
	inst, getErr := p.lambda.GetInstance(ctx, key)
	if getErr != nil {
		// Fallback to list in case providerID is hostname or name.
		var listErr error
		inst, listErr = p.findByInstanceKey(ctx, key, isGoneInstance)
		if listErr != nil {
			// Both Get and List failed — likely a transient API issue.
			// Return raw error so Karpenter retries instead of prematurely finalizing.
			return nil, fmt.Errorf("get instance %q: %w; list fallback: %v", key, getErr, listErr)
		}
		if inst == nil {
			return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance %q not found", key))
		}
	}

	return p.nodeClaimFromInstance(nil, inst), nil
}

func (p *Provider) List(ctx context.Context) ([]*v1.NodeClaim, error) {
	instances, err := p.listCache.List(ctx)
	if err != nil {
		return nil, err
	}

	var out []*v1.NodeClaim
	for _, inst := range instances {
		if isNonViableInstance(&inst) {
			continue
		}
		tags := tagsToMap(inst.Tags)
		if tags[tagCluster] != p.clusterName {
			continue
		}
		nc := p.nodeClaimFromInstance(nil, &inst)
		out = append(out, nc)
	}
	return out, nil
}

func (p *Provider) GetInstanceTypes(ctx context.Context, nodePool *v1.NodePool) ([]*cloudprovider.InstanceType, error) {
	items, err := p.cache.Get(ctx)
	if err != nil {
		return nil, err
	}

	// If the NodePool's NodeClass pins a specific instance type or uses a selector,
	// filter the returned types accordingly.
	var pinnedType string
	var selectorSet map[string]bool
	if nodePool != nil && nodePool.Spec.Template.Spec.NodeClassRef != nil {
		ref := nodePool.Spec.Template.Spec.NodeClassRef
		if ref.Group == v1alpha1.Group && ref.Kind == "LambdaNodeClass" {
			var class v1alpha1.LambdaNodeClass
			if err := p.kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name}, &class); err == nil {
				pinnedType = class.Spec.InstanceType
				if len(class.Spec.InstanceTypeSelector) > 0 {
					selectorSet = make(map[string]bool, len(class.Spec.InstanceTypeSelector))
					for _, name := range class.Spec.InstanceTypeSelector {
						selectorSet[name] = true
					}
				}
			}
		}
	}

	instanceTypes := make([]*cloudprovider.InstanceType, 0, len(items))
	for name, item := range items {
		if pinnedType != "" && name != pinnedType {
			continue
		}
		if selectorSet != nil && !selectorSet[name] {
			continue
		}
		instanceTypes = append(instanceTypes, p.instanceTypeFromItem(name, item))
	}
	return instanceTypes, nil
}

func (p *Provider) resolveNodeClass(ctx context.Context, nodeClaim *v1.NodeClaim) (*v1alpha1.LambdaNodeClass, error) {
	if nodeClaim.Spec.NodeClassRef == nil {
		return nil, fmt.Errorf("missing nodeClassRef")
	}
	ref := nodeClaim.Spec.NodeClassRef
	if ref.Group != v1alpha1.Group || ref.Kind != "LambdaNodeClass" {
		return nil, serrors.Wrap(fmt.Errorf("unsupported nodeclass"), "group", ref.Group, "kind", ref.Kind)
	}

	var class v1alpha1.LambdaNodeClass
	if err := p.kubeClient.Get(ctx, types.NamespacedName{Name: ref.Name}, &class); err != nil {
		return nil, err
	}
	return &class, nil
}

// instanceTypeFromNodeClaim extracts the instance type that Karpenter selected
// from the NodeClaim's requirements. Returns "" if not found.
func instanceTypeFromNodeClaim(nc *v1.NodeClaim) string {
	for _, req := range nc.Spec.Requirements {
		if req.Key == corev1.LabelInstanceTypeStable &&
			req.Operator == corev1.NodeSelectorOpIn &&
			len(req.Values) == 1 {
			return req.Values[0]
		}
	}
	return ""
}

func (p *Provider) buildLaunchRequest(nodeClaim *v1.NodeClaim, class *v1alpha1.LambdaNodeClass) (lambdaclient.LaunchRequest, error) {
	if class.Spec.Region == "" {
		return lambdaclient.LaunchRequest{}, fmt.Errorf("nodeclass region is required")
	}

	// Resolve instance type: prefer NodeClaim requirements (Karpenter's selection),
	// fall back to NodeClass spec (backward compat with pinned NodeClass).
	instanceType := instanceTypeFromNodeClaim(nodeClaim)
	if instanceType == "" {
		instanceType = class.Spec.InstanceType
	}
	if instanceType == "" {
		return lambdaclient.LaunchRequest{}, fmt.Errorf("cannot determine instance type: neither nodeclaim requirements nor nodeclass spec provide one")
	}

	tags := map[string]string{}
	if class.Spec.Tags != nil {
		for k, v := range class.Spec.Tags {
			tags[k] = v
		}
	}
	if nodeClaim != nil {
		if nodeClaim.Name != "" {
			tags[tagNodeClaim] = nodeClaim.Name
		}
		if nodeClaim.Labels != nil {
			if np, ok := nodeClaim.Labels[v1.NodePoolLabelKey]; ok {
				tags[tagNodePool] = np
			}
			if nc, ok := nodeClaim.Labels[v1.NodeClassLabelKey(nodeClaim.Spec.NodeClassRef.GroupKind())]; ok {
				tags[tagNodeClass] = nc
			}
		}
	}
	if p.clusterName != "" {
		tags[tagCluster] = p.clusterName
	}

	launchTags := make([]lambdaclient.TagEntry, 0, len(tags))
	for k, v := range tags {
		key := sanitizeTagKey(k)
		if key == "" {
			continue
		}
		launchTags = append(launchTags, lambdaclient.TagEntry{Key: key, Value: v})
	}

	// Determine userData source: prefer resolved content from userDataFrom, fall back to inline.
	var rawUserData string
	var userDataHash string
	if class.Status.ResolvedUserData != "" {
		rawUserData = class.Status.ResolvedUserData
		userDataHash = class.Status.ResolvedUserDataHash
	} else {
		rawUserData = class.Spec.UserData
	}

	// Render userData templates with launch-time context.
	udCtx := userDataContext{
		Region:        class.Spec.Region,
		ClusterName:   p.clusterName,
		NodeClaimName: nodeClaim.Name,
	}
	if class.Spec.Image != nil {
		udCtx.ImageFamily = class.Spec.Image.Family
		udCtx.ImageID = class.Spec.Image.ID
	}
	renderedUserData, err := renderUserData(rawUserData, udCtx)
	if err != nil {
		return lambdaclient.LaunchRequest{}, fmt.Errorf("rendering userData template: %w", err)
	}

	req := lambdaclient.LaunchRequest{
		Name:             nodeClaim.Name,
		Hostname:         sanitizeHostname(nodeClaim.Name),
		RegionName:       class.Spec.Region,
		InstanceTypeName: instanceType,
		UserData:         renderedUserData,
		SSHKeyNames:      class.Spec.SSHKeyNames,
		Tags:             launchTags,
	}

	if class.Spec.Image != nil {
		// Prefer resolved image ID from status (set by controller's image resolver).
		imageSpec := lambdaclient.ImageSpec{ID: class.Spec.Image.ID, Family: class.Spec.Image.Family}
		if class.Status.ResolvedImageID != "" {
			imageSpec.ID = class.Status.ResolvedImageID
		}
		req.Image = &imageSpec
		// Tag with image identifier so nodeClaimFromInstance can set Status.ImageID,
		// enabling Karpenter drift detection on image changes.
		imageTag := imageSpec.ID
		if imageTag == "" {
			imageTag = imageSpec.Family
		}
		if imageTag != "" {
			req.Tags = append(req.Tags, lambdaclient.TagEntry{Key: tagImageID, Value: imageTag})
		}
	}
	if len(class.Spec.FileSystemNames) > 0 {
		req.FileSystemNames = append([]string(nil), class.Spec.FileSystemNames...)
	}
	if len(class.Spec.FileSystemMounts) > 0 {
		for _, m := range class.Spec.FileSystemMounts {
			req.FileSystemMounts = append(req.FileSystemMounts, lambdaclient.FilesystemMountEntry{
				MountPoint:   m.MountPoint,
				FileSystemID: m.FileSystemID,
			})
		}
	}
	if len(class.Spec.FirewallRulesetIDs) > 0 {
		for _, id := range class.Spec.FirewallRulesetIDs {
			req.FirewallRulesets = append(req.FirewallRulesets, lambdaclient.FirewallRulesetEntry{ID: id})
		}
	}
	if class.Spec.PublicIP != nil {
		req.PublicIP = class.Spec.PublicIP
	}
	if class.Spec.Pool != "" {
		req.Pool = class.Spec.Pool
	}
	// Tag with userData hash for drift detection (userDataFrom path).
	if userDataHash != "" {
		req.Tags = append(req.Tags, lambdaclient.TagEntry{Key: tagUserDataHash, Value: userDataHash})
	}
	return req, nil
}

// findByNodeClaimTag looks up an instance by its karpenter-sh-nodeclaim tag.
// The skip function controls which instances to exclude from results.
func (p *Provider) findByNodeClaimTag(ctx context.Context, name string, skip func(*lambdaclient.Instance) bool) (*lambdaclient.Instance, error) {
	instances, err := p.listCache.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if skip(&inst) {
			continue
		}
		tags := tagsToMap(inst.Tags)
		if tags[tagCluster] != p.clusterName {
			continue
		}
		if tags[tagNodeClaim] == name {
			return &inst, nil
		}
	}
	return nil, nil
}

func (p *Provider) resolveInstanceForNodeClaim(ctx context.Context, nodeClaim *v1.NodeClaim) (*lambdaclient.Instance, error) {
	if nodeClaim.Status.ProviderID != "" {
		key := parseProviderID(nodeClaim.Status.ProviderID)
		if key != "" {
			inst, err := p.lambda.GetInstance(ctx, key)
			if err == nil {
				return inst, nil
			}
			// Fallback to list in case Get returns a transient error or providerID is hostname.
			inst, err = p.findByInstanceKey(ctx, key, isGoneInstance)
			if err != nil {
				return nil, err
			}
			if inst != nil {
				return inst, nil
			}
		}
	}
	return p.findByNodeClaimTag(ctx, nodeClaim.Name, isGoneInstance)
}

func (p *Provider) findByInstanceKey(ctx context.Context, key string, skip func(*lambdaclient.Instance) bool) (*lambdaclient.Instance, error) {
	instances, err := p.listCache.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if skip(&inst) {
			continue
		}
		if inst.ID == key || inst.Hostname == key || inst.Name == key {
			return &inst, nil
		}
	}
	return nil, nil
}

// isGoneInstance returns true for instances that are irrecoverably gone and
// will never serve as a node again.
func isGoneInstance(inst *lambdaclient.Instance) bool {
	switch inst.Status {
	case "terminated", "preempted":
		return true
	default:
		return false
	}
}

// isNonViableInstance returns true for instances that should not be considered
// when checking for existing viable instances (e.g., during Create idempotency).
// Includes gone instances plus those that are in the process of dying.
func isNonViableInstance(inst *lambdaclient.Instance) bool {
	switch inst.Status {
	case "terminated", "preempted", "unhealthy", "terminating":
		return true
	default:
		return false
	}
}

func (p *Provider) nodeClaimFromInstance(seed *v1.NodeClaim, inst *lambdaclient.Instance) *v1.NodeClaim {
	var nc v1.NodeClaim
	if seed != nil {
		nc = *seed.DeepCopy()
	}

	labels := map[string]string{}
	if nc.Labels != nil {
		for k, v := range nc.Labels {
			labels[k] = v
		}
	}
	// Rehydrate core labels from instance tags when seed is nil (List/Get path).
	if seed == nil {
		tags := tagsToMap(inst.Tags)
		if np := tags[tagNodePool]; np != "" {
			labels[v1.NodePoolLabelKey] = np
		}
		if ncName := tags[tagNodeClass]; ncName != "" {
			labels[v1.NodeClassLabelKey(schema.GroupKind{Group: v1alpha1.Group, Kind: "LambdaNodeClass"})] = ncName
		}
	}
	// Lambda only has on-demand capacity.
	labels[v1.CapacityTypeLabelKey] = v1.CapacityTypeOnDemand
	labels[corev1.LabelInstanceTypeStable] = inst.Type.Name
	if inst.Region.Name != "" {
		labels[corev1.LabelTopologyRegion] = inst.Region.Name
		// Synthetic single-zone: Lambda regions don't have availability zones.
		labels[corev1.LabelTopologyZone] = inst.Region.Name + "a"
	}

	nc.Labels = labels
	nc.Status.ProviderID = providerIDForInstance(inst)

	// Populate capacity so Karpenter's scheduler knows this pending NodeClaim
	// can accept pods. Without this, the scheduler sees zero available resources
	// and creates duplicate NodeClaims for the same pod.
	capacity := capacityFromSpecs(inst.Type.Name, inst.Type.Specs)
	nc.Status.Capacity = capacity
	nc.Status.Allocatable = capacity

	// Set ImageID from the tag we applied at launch time. This enables
	// Karpenter drift detection when the nodeclass image changes.
	tags := tagsToMap(inst.Tags)
	if imageID := tags[tagImageID]; imageID != "" {
		nc.Status.ImageID = imageID
	}

	return &nc
}

func providerIDForInstance(inst *lambdaclient.Instance) string {
	return providerIDPrefix + inst.ID
}

func (p *Provider) instanceTypeFromItem(name string, item lambdaclient.InstanceTypesItem) *cloudprovider.InstanceType {
	// Determine architecture: GH200 is arm64, everything else is amd64.
	arch := "amd64"
	if strings.Contains(strings.ToLower(name), "gh200") {
		arch = "arm64"
	}

	requirements := scheduling.NewRequirements(
		scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, name),
		scheduling.NewRequirement(corev1.LabelOSStable, corev1.NodeSelectorOpIn, "linux"),
		scheduling.NewRequirement(corev1.LabelArchStable, corev1.NodeSelectorOpIn, arch),
	)

	offerings := cloudprovider.Offerings{}
	regions := item.Regions
	if len(regions) == 0 {
		regions = []lambdaclient.Region{{Name: "unknown"}}
	}
	for _, region := range regions {
		// Synthetic single-zone: Lambda regions don't have availability zones.
		zone := region.Name + "a"
		reqs := scheduling.NewRequirements(
			scheduling.NewRequirement(v1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, v1.CapacityTypeOnDemand),
			scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, zone),
			scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, region.Name),
		)
		offerings = append(offerings, &cloudprovider.Offering{
			Requirements: reqs,
			Available:    region.Name != "unknown" && !p.unavailableOfferings.IsUnavailable(name, region.Name),
			Price:        float64(item.InstanceType.PriceCents) / 100.0,
		})
	}

	capacity := capacityFromSpecs(name, item.InstanceType.Specs)

	return &cloudprovider.InstanceType{
		Name:         name,
		Requirements: requirements,
		Offerings:    offerings,
		Capacity:     capacity,
		Overhead:     &cloudprovider.InstanceTypeOverhead{},
	}
}

// capacityFromSpecs builds a ResourceList from instance type specs. Used both
// for advertising instance type capacity and for populating NodeClaim status
// so the scheduler can account for pending NodeClaims.
func capacityFromSpecs(name string, specs lambdaclient.InstanceTypeSpec) corev1.ResourceList {
	capacity := corev1.ResourceList{}
	capacity[corev1.ResourceCPU] = *resource.NewQuantity(int64(specs.VCPUs), resource.DecimalSI)
	capacity[corev1.ResourceMemory] = *resource.NewQuantity(int64(specs.MemoryGiB)<<30, resource.BinarySI)
	if specs.StorageGiB > 0 {
		capacity[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(int64(specs.StorageGiB)<<30, resource.BinarySI)
	}
	if specs.GPUs > 0 {
		capacity[corev1.ResourceName("nvidia.com/gpu")] = *resource.NewQuantity(int64(specs.GPUs), resource.DecimalSI)
	}
	// Conservative default max pods to satisfy scheduling requirements.
	capacity[corev1.ResourcePods] = *resource.NewQuantity(110, resource.DecimalSI)
	return capacity
}

func tagsToMap(tags []lambdaclient.TagEntry) map[string]string {
	out := map[string]string{}
	for _, tag := range tags {
		out[tag.Key] = tag.Value
	}
	return out
}

func parseProviderID(providerID string) string {
	return strings.TrimPrefix(providerID, providerIDPrefix)
}

func sanitizeHostname(name string) string {
	if name == "" {
		return "lambda-node"
	}
	clean := strings.ToLower(name)
	clean = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-':
			return r
		default:
			return '-'
		}
	}, clean)
	clean = strings.Trim(clean, "-")
	if clean == "" {
		return "lambda-node"
	}
	if len(clean) > 63 {
		clean = clean[:63]
	}
	return clean
}

func sanitizeTagKey(key string) string {
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "/", "-")
	key = strings.ReplaceAll(key, "_", "-")
	key = strings.ReplaceAll(key, ".", "-")
	key = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == ':':
			return r
		default:
			return '-'
		}
	}, key)
	key = strings.Trim(key, "-")
	if key == "" {
		return ""
	}
	if key[0] < 'a' || key[0] > 'z' {
		key = "k-" + key
	}
	if len(key) > 55 {
		key = key[:55]
	}
	return key
}
