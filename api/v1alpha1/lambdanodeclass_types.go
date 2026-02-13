package v1alpha1

import (
	"github.com/awslabs/operatorpkg/status"
	"k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// LambdaNodeClass is the Schema for Lambda Cloud node classes.
// Cluster-scoped. CRD is hand-authored in charts/lambda-karpenter/crds/.
type LambdaNodeClass struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LambdaNodeClassSpec   `json:"spec"`
	Status LambdaNodeClassStatus `json:"status,omitempty"`
}

// LambdaNodeClassList contains a list of LambdaNodeClass.
type LambdaNodeClassList struct {
	v1.TypeMeta `json:",inline"`
	v1.ListMeta `json:"metadata,omitempty"`
	Items       []LambdaNodeClass `json:"items"`
}

type LambdaNodeClassSpec struct {
	Region               string            `json:"region"`
	InstanceType         string            `json:"instanceType"`
	InstanceTypeSelector []string          `json:"instanceTypeSelector,omitempty"`
	Image                *LambdaImage      `json:"image,omitempty"`
	SSHKeyNames          []string          `json:"sshKeyNames,omitempty"`
	PublicIP             *bool             `json:"publicIP,omitempty"`
	Pool                 string            `json:"pool,omitempty"`
	FirewallRulesetIDs   []string          `json:"firewallRulesetIDs,omitempty"`
	FileSystemNames      []string          `json:"fileSystemNames,omitempty"`
	FileSystemMounts     []FileSystemMount `json:"fileSystemMounts,omitempty"`
	Tags                 map[string]string `json:"tags,omitempty"`
	UserData             string            `json:"userData,omitempty"`
	UserDataFrom         []UserDataSource  `json:"userDataFrom,omitempty"`
}

// FileSystemMount specifies a Lambda Cloud filesystem mount.
type FileSystemMount struct {
	MountPoint   string `json:"mountPoint"`
	FileSystemID string `json:"fileSystemID"`
}

// UserDataSource is a source of userData content.
// Exactly one of Inline or ConfigMapRef must be set.
type UserDataSource struct {
	Inline       string           `json:"inline,omitempty"`
	ConfigMapRef *ConfigMapKeyRef `json:"configMapRef,omitempty"`
}

// ConfigMapKeyRef references a key in a ConfigMap.
// Namespace is required because LambdaNodeClass is cluster-scoped.
type ConfigMapKeyRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
}

type LambdaImage struct {
	ID     string `json:"id,omitempty"`
	Family string `json:"family,omitempty"`
}

type LambdaNodeClassStatus struct {
	ResolvedImageID      string             `json:"resolvedImageID,omitempty"`
	ResolvedImageFamily  string             `json:"resolvedImageFamily,omitempty"`
	ResolvedUserData     string             `json:"resolvedUserData,omitempty"`
	ResolvedUserDataHash string             `json:"resolvedUserDataHash,omitempty"`
	LastValidatedAt      *v1.Time           `json:"lastValidatedAt,omitempty"`
	Conditions           []status.Condition `json:"conditions,omitempty"`
}

func init() {
	SchemeBuilder.Register(&LambdaNodeClass{}, &LambdaNodeClassList{})
}

var (
	_ runtime.Object = (*LambdaNodeClass)(nil)
	_ runtime.Object = (*LambdaNodeClassList)(nil)
)

func (in *LambdaNodeClass) DeepCopyInto(out *LambdaNodeClass) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	out.Spec = *in.Spec.DeepCopy()
	out.Status = *in.Status.DeepCopy()
}

func (in *LambdaNodeClass) DeepCopy() *LambdaNodeClass {
	if in == nil {
		return nil
	}
	out := new(LambdaNodeClass)
	in.DeepCopyInto(out)
	return out
}

func (in *LambdaNodeClass) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *LambdaNodeClassList) DeepCopyInto(out *LambdaNodeClassList) {
	*out = *in
	out.TypeMeta = in.TypeMeta
	in.ListMeta.DeepCopyInto(&out.ListMeta)
	if in.Items != nil {
		out.Items = make([]LambdaNodeClass, len(in.Items))
		for i := range in.Items {
			in.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
}

func (in *LambdaNodeClassList) DeepCopy() *LambdaNodeClassList {
	if in == nil {
		return nil
	}
	out := new(LambdaNodeClassList)
	in.DeepCopyInto(out)
	return out
}

func (in *LambdaNodeClassList) DeepCopyObject() runtime.Object {
	if c := in.DeepCopy(); c != nil {
		return c
	}
	return nil
}

func (in *LambdaNodeClassSpec) DeepCopy() *LambdaNodeClassSpec {
	if in == nil {
		return nil
	}
	out := new(LambdaNodeClassSpec)
	*out = *in
	if in.InstanceTypeSelector != nil {
		out.InstanceTypeSelector = append([]string(nil), in.InstanceTypeSelector...)
	}
	if in.SSHKeyNames != nil {
		out.SSHKeyNames = append([]string(nil), in.SSHKeyNames...)
	}
	if in.FirewallRulesetIDs != nil {
		out.FirewallRulesetIDs = append([]string(nil), in.FirewallRulesetIDs...)
	}
	if in.FileSystemNames != nil {
		out.FileSystemNames = append([]string(nil), in.FileSystemNames...)
	}
	if in.FileSystemMounts != nil {
		out.FileSystemMounts = make([]FileSystemMount, len(in.FileSystemMounts))
		copy(out.FileSystemMounts, in.FileSystemMounts)
	}
	if in.Tags != nil {
		out.Tags = make(map[string]string, len(in.Tags))
		for k, v := range in.Tags {
			out.Tags[k] = v
		}
	}
	if in.Image != nil {
		out.Image = &LambdaImage{
			ID:     in.Image.ID,
			Family: in.Image.Family,
		}
	}
	if in.UserDataFrom != nil {
		out.UserDataFrom = make([]UserDataSource, len(in.UserDataFrom))
		for i, src := range in.UserDataFrom {
			out.UserDataFrom[i] = UserDataSource{Inline: src.Inline}
			if src.ConfigMapRef != nil {
				ref := *src.ConfigMapRef
				out.UserDataFrom[i].ConfigMapRef = &ref
			}
		}
	}
	return out
}

func (in *LambdaNodeClassStatus) DeepCopy() *LambdaNodeClassStatus {
	if in == nil {
		return nil
	}
	out := new(LambdaNodeClassStatus)
	*out = *in
	if in.LastValidatedAt != nil {
		t := *in.LastValidatedAt
		out.LastValidatedAt = &t
	}
	if in.Conditions != nil {
		out.Conditions = append([]status.Condition(nil), in.Conditions...)
	}
	return out
}

func (in *LambdaNodeClass) StatusConditions() status.ConditionSet {
	return status.NewReadyConditions(status.ConditionReady).For(in)
}

func (in *LambdaNodeClass) GetConditions() []status.Condition {
	return in.Status.Conditions
}

func (in *LambdaNodeClass) SetConditions(conditions []status.Condition) {
	in.Status.Conditions = conditions
}
