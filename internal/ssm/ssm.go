package ssm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

// Activation holds the code and ID returned by SSM CreateActivation.
type Activation struct {
	ActivationCode string
	ActivationID   string
}

// Client wraps the AWS SSM API for hybrid node activation management.
type Client struct {
	ssm *ssm.Client
}

// New creates an SSM client for the given AWS region. It uses the default
// credential chain, which automatically picks up IRSA credentials when
// running in an EKS pod with an annotated service account.
func New(ctx context.Context, region string) (*Client, error) {
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
	if err != nil {
		return nil, fmt.Errorf("loading AWS config: %w", err)
	}
	return &Client{ssm: ssm.NewFromConfig(cfg)}, nil
}

// CreateActivation creates a single-use SSM activation that expires in 1 hour.
// The iamRole is the IAM role name or ARN that the hybrid node will assume.
// If a full ARN is provided, the role name is extracted automatically since
// the SSM API requires a role name, not an ARN.
func (c *Client) CreateActivation(ctx context.Context, iamRole string) (*Activation, error) {
	// SSM CreateActivation expects a role name, not a full ARN.
	// Extract from "arn:aws:iam::123456789012:role/RoleName" → "RoleName"
	roleName := iamRole
	if strings.HasPrefix(iamRole, "arn:") {
		parts := strings.SplitN(iamRole, ":role/", 2)
		if len(parts) == 2 {
			roleName = parts[1]
		}
	}
	expiration := time.Now().Add(1 * time.Hour)
	out, err := c.ssm.CreateActivation(ctx, &ssm.CreateActivationInput{
		IamRole:             aws.String(roleName),
		RegistrationLimit:   aws.Int32(1),
		ExpirationDate:      aws.Time(expiration),
		DefaultInstanceName: aws.String("karpenter-hybrid-node"),
	})
	if err != nil {
		return nil, fmt.Errorf("ssm CreateActivation: %w", err)
	}
	return &Activation{
		ActivationCode: aws.ToString(out.ActivationCode),
		ActivationID:   aws.ToString(out.ActivationId),
	}, nil
}

// DeleteActivation deletes an SSM activation. It is best-effort: "not found"
// errors (expired or already deleted activations) are silently ignored.
func (c *Client) DeleteActivation(ctx context.Context, activationID string) error {
	_, err := c.ssm.DeleteActivation(ctx, &ssm.DeleteActivationInput{
		ActivationId: aws.String(activationID),
	})
	if err != nil {
		// Ignore "not found" — the activation may have expired or been cleaned up.
		var invalidActivation *types.InvalidActivation
		var invalidActivationID *types.InvalidActivationId
		if errors.As(err, &invalidActivation) || errors.As(err, &invalidActivationID) {
			return nil
		}
		return fmt.Errorf("ssm DeleteActivation %s: %w", activationID, err)
	}
	return nil
}
