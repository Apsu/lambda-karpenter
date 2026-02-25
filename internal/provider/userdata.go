package provider

import (
	"bytes"
	"strings"
	"text/template"
)

// userDataContext holds the values available to Go templates in userData.
type userDataContext struct {
	Region        string
	ClusterName   string
	NodeClaimName string
	ImageFamily   string
	ImageID       string

	// EKS hybrid fields (populated when SSM activator is configured).
	SSMActivationCode string
	SSMActivationID   string
	GatewayIP         string
}

// renderUserData renders Go template actions in userData. If the string
// contains no template actions, it is returned unchanged (fast path).
// Security: text/template is appropriate here — NodeClass is cluster-scoped
// (admin-only), and there is no injection risk from pod specs.
func renderUserData(raw string, ctx userDataContext) (string, error) {
	if !strings.Contains(raw, "{{") {
		return raw, nil
	}
	tmpl, err := template.New("userData").Option("missingkey=error").Parse(raw)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ctx); err != nil {
		return "", err
	}
	return buf.String(), nil
}
