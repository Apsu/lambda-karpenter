package provider

import (
	"strings"
	"testing"
)

func TestRenderUserDataWithVars(t *testing.T) {
	raw := `node-label:
  - node.kubernetes.io/instance-type={{.InstanceType}}
  - topology.kubernetes.io/region={{.Region}}
  - cluster={{.ClusterName}}`

	ctx := userDataContext{
		InstanceType: "gpu_1x_gh200",
		Region:       "us-east-3",
		ClusterName:  "my-cluster",
	}

	got, err := renderUserData(raw, ctx)
	if err != nil {
		t.Fatalf("renderUserData: %v", err)
	}

	if !strings.Contains(got, "instance-type=gpu_1x_gh200") {
		t.Fatalf("expected InstanceType rendered, got:\n%s", got)
	}
	if !strings.Contains(got, "region=us-east-3") {
		t.Fatalf("expected Region rendered, got:\n%s", got)
	}
	if !strings.Contains(got, "cluster=my-cluster") {
		t.Fatalf("expected ClusterName rendered, got:\n%s", got)
	}
}

func TestRenderUserDataNoTemplateActions(t *testing.T) {
	raw := "#cloud-config\npackages: [vim]\n"
	got, err := renderUserData(raw, userDataContext{})
	if err != nil {
		t.Fatalf("renderUserData: %v", err)
	}
	if got != raw {
		t.Fatalf("expected unchanged string, got:\n%s", got)
	}
}

func TestRenderUserDataInvalidTemplate(t *testing.T) {
	raw := "bad template {{ .Foo"
	_, err := renderUserData(raw, userDataContext{})
	if err == nil {
		t.Fatal("expected error for invalid template")
	}
}

func TestRenderUserDataShellVarsPreserved(t *testing.T) {
	raw := `INSTANCE_ID="${INSTANCE_ID}"
type={{.InstanceType}}`

	ctx := userDataContext{InstanceType: "gpu_1x_gh200"}
	got, err := renderUserData(raw, ctx)
	if err != nil {
		t.Fatalf("renderUserData: %v", err)
	}
	if !strings.Contains(got, `"${INSTANCE_ID}"`) {
		t.Fatalf("expected shell vars preserved, got:\n%s", got)
	}
	if !strings.Contains(got, "type=gpu_1x_gh200") {
		t.Fatalf("expected InstanceType rendered, got:\n%s", got)
	}
}

func TestRenderUserDataAllFields(t *testing.T) {
	raw := "{{.InstanceType}} {{.Region}} {{.ClusterName}} {{.NodeClaimName}} {{.ImageFamily}} {{.ImageID}}"
	ctx := userDataContext{
		InstanceType:  "gpu_1x_gh200",
		Region:        "us-east-3",
		ClusterName:   "test-cluster",
		NodeClaimName: "nc-abc",
		ImageFamily:   "lambda-stack-24-04",
		ImageID:       "img-123",
	}
	got, err := renderUserData(raw, ctx)
	if err != nil {
		t.Fatalf("renderUserData: %v", err)
	}
	expected := "gpu_1x_gh200 us-east-3 test-cluster nc-abc lambda-stack-24-04 img-123"
	if got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}
