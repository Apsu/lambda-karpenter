package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/awslabs/operatorpkg/serrors"
	"github.com/awslabs/operatorpkg/status"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/evecallicoat/lambda-karpenter/api/v1alpha1"
	"github.com/evecallicoat/lambda-karpenter/internal/lambdaclient"
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
)

type LambdaAPI interface {
	ListInstances(ctx context.Context) ([]lambdaclient.Instance, error)
	GetInstance(ctx context.Context, id string) (*lambdaclient.Instance, error)
	LaunchInstance(ctx context.Context, req lambdaclient.LaunchRequest) ([]string, error)
	TerminateInstance(ctx context.Context, id string) error
}

var _ cloudprovider.CloudProvider = (*Provider)(nil)

// Provider implements the Karpenter CloudProvider interface for Lambda Cloud.
type Provider struct {
	kubeClient  client.Client
	lambda      LambdaAPI
	cache       *lambdaclient.InstanceTypeCache
	clusterName string
}

func New(kubeClient client.Client, lambda LambdaAPI, cache *lambdaclient.InstanceTypeCache, clusterName string) *Provider {
	return &Provider{
		kubeClient:  kubeClient,
		lambda:      lambda,
		cache:       cache,
		clusterName: clusterName,
	}
}

func (p *Provider) Name() string {
	return providerName
}

func (p *Provider) GetSupportedNodeClasses() []status.Object {
	return []status.Object{&v1alpha1.LambdaNodeClass{}}
}

func (p *Provider) RepairPolicies() []cloudprovider.RepairPolicy {
	return nil
}

func (p *Provider) IsDrifted(context.Context, *v1.NodeClaim) (cloudprovider.DriftReason, error) {
	return "", nil
}

func (p *Provider) Create(ctx context.Context, nodeClaim *v1.NodeClaim) (*v1.NodeClaim, error) {
	nc := nodeClaim.DeepCopy()

	class, err := p.resolveNodeClass(ctx, nc)
	if err != nil {
		return nil, err
	}
	if len(class.Spec.SSHKeyNames) == 0 {
		return nil, serrors.Wrap(fmt.Errorf("sshKeyNames must include at least one entry"), "nodeclass", class.Name)
	}

	existing, err := p.findByNodeClaimTag(ctx, nc.Name)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return p.nodeClaimFromInstance(nc, existing), nil
	}

	if err := p.enforceNodePoolLimit(ctx, nc); err != nil {
		return nil, err
	}

	launchReq, err := p.buildLaunchRequest(nc, class)
	if err != nil {
		return nil, err
	}

	ids, err := p.lambda.LaunchInstance(ctx, launchReq)
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("lambda launch returned no instance ids")
	}

	inst, err := p.lambda.GetInstance(ctx, ids[0])
	if err != nil {
		return nil, err
	}

	return p.nodeClaimFromInstance(nc, inst), nil
}

func (p *Provider) enforceNodePoolLimit(ctx context.Context, nc *v1.NodeClaim) error {
	npName := ""
	if nc.Labels != nil {
		if val, ok := nc.Labels[v1.NodePoolLabelKey]; ok {
			npName = val
		}
	}
	if npName == "" {
		return nil
	}

	var np v1.NodePool
	if err := p.kubeClient.Get(ctx, types.NamespacedName{Name: npName}, &np); err != nil {
		return err
	}
	if np.Spec.Limits == nil {
		return nil
	}
	limitQty, ok := np.Spec.Limits[corev1.ResourceName("nodes")]
	if !ok {
		return nil
	}
	limit := limitQty.Value()
	if limit <= 0 {
		return nil
	}

	instances, err := p.lambda.ListInstances(ctx)
	if err != nil {
		return err
	}
	active := 0
	for _, inst := range instances {
		if isTerminalInstance(&inst) {
			continue
		}
		tags := tagsToMap(inst.Tags)
		if tags[tagCluster] != p.clusterName {
			continue
		}
		if tags[tagNodePool] != npName {
			continue
		}
		active++
	}

	if int64(active) >= limit {
		return cloudprovider.NewInsufficientCapacityError(fmt.Errorf("nodepool %s limit %d reached", npName, limit))
	}
	return nil
}

func (p *Provider) Delete(ctx context.Context, nodeClaim *v1.NodeClaim) error {
	inst, err := p.resolveInstanceForNodeClaim(ctx, nodeClaim)
	if err != nil {
		return err
	}
	if inst == nil || isTerminalInstance(inst) {
		return cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("nodeclaim not found"))
	}
	if inst.Status == "terminating" {
		return nil
	}
	if err := p.lambda.TerminateInstance(ctx, inst.ID); err != nil {
		return err
	}
	return nil
}

func (p *Provider) Get(ctx context.Context, providerID string) (*v1.NodeClaim, error) {
	key := parseProviderID(providerID)
	if key == "" {
		return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("invalid provider id"))
	}
	inst, err := p.lambda.GetInstance(ctx, key)
	if err != nil {
		// Fallback to list in case providerID is hostname or name.
		inst, err = p.findByInstanceKey(ctx, key)
		if err != nil {
			return nil, cloudprovider.NewNodeClaimNotFoundError(err)
		}
		if inst == nil {
			return nil, cloudprovider.NewNodeClaimNotFoundError(fmt.Errorf("instance %q not found", key))
		}
	}

	return p.nodeClaimFromInstance(nil, inst), nil
}

func (p *Provider) List(ctx context.Context) ([]*v1.NodeClaim, error) {
	instances, err := p.lambda.ListInstances(ctx)
	if err != nil {
		return nil, err
	}

	var out []*v1.NodeClaim
	for _, inst := range instances {
		tags := tagsToMap(inst.Tags)
		if tags[tagCluster] != p.clusterName {
			continue
		}
		nc := p.nodeClaimFromInstance(nil, &inst)
		out = append(out, nc)
	}
	return out, nil
}

func (p *Provider) GetInstanceTypes(ctx context.Context, _ *v1.NodePool) ([]*cloudprovider.InstanceType, error) {
	items, err := p.cache.Get(ctx)
	if err != nil {
		return nil, err
	}
	instanceTypes := make([]*cloudprovider.InstanceType, 0, len(items))
	for name, item := range items {
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

func (p *Provider) buildLaunchRequest(nodeClaim *v1.NodeClaim, class *v1alpha1.LambdaNodeClass) (lambdaclient.LaunchRequest, error) {
	if class.Spec.Region == "" {
		return lambdaclient.LaunchRequest{}, fmt.Errorf("nodeclass region is required")
	}
	if class.Spec.InstanceType == "" {
		return lambdaclient.LaunchRequest{}, fmt.Errorf("nodeclass instanceType is required")
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

	req := lambdaclient.LaunchRequest{
		Name:             nodeClaim.Name,
		Hostname:         sanitizeHostname(nodeClaim.Name),
		RegionName:       class.Spec.Region,
		InstanceTypeName: class.Spec.InstanceType,
		UserData:         class.Spec.UserData,
		SSHKeyNames:      class.Spec.SSHKeyNames,
		Tags:             launchTags,
	}

	if class.Spec.Image != nil {
		req.Image = &lambdaclient.ImageSpec{ID: class.Spec.Image.ID, Family: class.Spec.Image.Family}
	}
	if len(class.Spec.FirewallRulesetIDs) > 0 {
		for _, id := range class.Spec.FirewallRulesetIDs {
			req.FirewallRulesets = append(req.FirewallRulesets, lambdaclient.FirewallRulesetEntry{ID: id})
		}
	}
	return req, nil
}

func (p *Provider) findByNodeClaimTag(ctx context.Context, name string) (*lambdaclient.Instance, error) {
	instances, err := p.lambda.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
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
			inst, err = p.findByInstanceKey(ctx, key)
			if err != nil {
				return nil, err
			}
			if inst != nil {
				return inst, nil
			}
		}
	}
	return p.findByNodeClaimTag(ctx, nodeClaim.Name)
}

func (p *Provider) findByInstanceKey(ctx context.Context, key string) (*lambdaclient.Instance, error) {
	instances, err := p.lambda.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.ID == key || inst.Hostname == key || inst.Name == key {
			return &inst, nil
		}
	}
	return nil, nil
}

func isTerminalInstance(inst *lambdaclient.Instance) bool {
	switch inst.Status {
	case "terminated", "preempted":
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
	// Rehydrate core labels from instance tags when seed is nil.
	if seed == nil {
		tags := tagsToMap(inst.Tags)
		if np := tags[tagNodePool]; np != "" {
			labels[v1.NodePoolLabelKey] = np
		}
		if ncName := tags[tagNodeClass]; ncName != "" {
			labels[v1.NodeClassLabelKey(schema.GroupKind{Group: v1alpha1.Group, Kind: "LambdaNodeClass"})] = ncName
		}
		labels[v1.CapacityTypeLabelKey] = v1.CapacityTypeOnDemand
	}
	labels[corev1.LabelInstanceTypeStable] = inst.Type.Name
	if inst.Region.Name != "" {
		labels[corev1.LabelTopologyRegion] = inst.Region.Name
		labels[corev1.LabelTopologyZone] = inst.Region.Name
	}

	nc.Labels = labels
	nc.Status.ProviderID = providerIDForInstance(inst)

	return &nc
}

func providerIDForInstance(inst *lambdaclient.Instance) string {
	return providerIDPrefix + inst.ID
}

func (p *Provider) instanceTypeFromItem(name string, item lambdaclient.InstanceTypesItem) *cloudprovider.InstanceType {
	requirements := scheduling.NewRequirements(
		scheduling.NewRequirement(corev1.LabelInstanceTypeStable, corev1.NodeSelectorOpIn, name),
	)

	offerings := cloudprovider.Offerings{}
	regions := item.Regions
	if len(regions) == 0 {
		regions = []lambdaclient.Region{{Name: "unknown"}}
	}
	for _, region := range regions {
		reqs := scheduling.NewRequirements(
			scheduling.NewRequirement(v1.CapacityTypeLabelKey, corev1.NodeSelectorOpIn, v1.CapacityTypeOnDemand),
			scheduling.NewRequirement(corev1.LabelTopologyZone, corev1.NodeSelectorOpIn, region.Name),
			scheduling.NewRequirement(corev1.LabelTopologyRegion, corev1.NodeSelectorOpIn, region.Name),
		)
		offerings = append(offerings, &cloudprovider.Offering{
			Requirements: reqs,
			Available:    region.Name != "unknown",
			Price:        float64(item.InstanceType.PriceCents) / 100.0,
		})
	}

	capacity := corev1.ResourceList{}
	capacity[corev1.ResourceCPU] = *resource.NewQuantity(int64(item.InstanceType.Specs.VCPUs), resource.DecimalSI)
	capacity[corev1.ResourceMemory] = *resource.NewQuantity(int64(item.InstanceType.Specs.MemoryGiB)<<30, resource.BinarySI)
	if item.InstanceType.Specs.StorageGiB > 0 {
		capacity[corev1.ResourceEphemeralStorage] = *resource.NewQuantity(int64(item.InstanceType.Specs.StorageGiB)<<30, resource.BinarySI)
	}
	if item.InstanceType.Specs.GPUs > 0 {
		capacity[corev1.ResourceName("nvidia.com/gpu")] = *resource.NewQuantity(int64(item.InstanceType.Specs.GPUs), resource.DecimalSI)
	}
	// Set a conservative default max pods to satisfy scheduling requirements.
	capacity[corev1.ResourcePods] = *resource.NewQuantity(110, resource.DecimalSI)

	return &cloudprovider.InstanceType{
		Name:         name,
		Requirements: requirements,
		Offerings:    offerings,
		Capacity:     capacity,
		Overhead:     &cloudprovider.InstanceTypeOverhead{},
	}
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
