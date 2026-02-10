package lambdaclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/evecallicoat/lambda-karpenter/internal/ratelimit"
)

const (
	defaultTimeout = 30 * time.Second
	maxAttempts    = 5
)

// Client is a thin Lambda Cloud API client.
type Client struct {
	baseURL *url.URL
	http    *http.Client
	token   string
	limiter *ratelimit.Limiter
}

func New(baseURL, token string, limiter *ratelimit.Limiter) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid base url: %w", err)
	}
	return &Client{
		baseURL: parsed,
		http: &http.Client{
			Timeout: defaultTimeout,
		},
		token:   token,
		limiter: limiter,
	}, nil
}

// Instance represents a Lambda instance (partial fields for MVP).
type Instance struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Status    string          `json:"status"`
	IP        string          `json:"ip"`
	PrivateIP string          `json:"private_ip"`
	Hostname  string          `json:"hostname"`
	SSHKeyNames      []string               `json:"ssh_key_names"`
	FileSystemNames  []string               `json:"file_system_names"`
	FileSystemMounts []FilesystemMountEntry `json:"file_system_mounts,omitempty"`
	Tags      []TagEntry      `json:"tags"`
	Actions   InstanceActionAvailability `json:"actions"`
	Region    Region          `json:"region"`
	Type      InstanceTypeRef `json:"instance_type"`
	CreatedAt time.Time       `json:"created_time"`
}

// InstanceTypeRef represents Lambda instance type detail.
type InstanceTypeRef struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	GPUDesc     string           `json:"gpu_description"`
	PriceCents  int              `json:"price_cents_per_hour"`
	Specs       InstanceTypeSpec `json:"specs"`
}

type InstanceTypeSpec struct {
	VCPUs      int `json:"vcpus"`
	MemoryGiB  int `json:"memory_gib"`
	StorageGiB int `json:"storage_gib"`
	GPUs       int `json:"gpus"`
}

type InstanceTypesItem struct {
	InstanceType InstanceTypeRef `json:"instance_type"`
	Regions      []Region        `json:"regions_with_capacity_available"`
}

type Region struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// Image represents a Lambda image (partial fields for MVP).
type Image struct {
	ID          string    `json:"id"`
	Family      string    `json:"family"`
	Name        string    `json:"name"`
	Region      Region    `json:"region"`
	Arch        string    `json:"architecture"`
	CreatedTime time.Time `json:"created_time"`
	UpdatedTime time.Time `json:"updated_time"`
}

type TagEntry struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type FirewallRulesetEntry struct {
	ID string `json:"id"`
}

type FilesystemMountEntry struct {
	MountPoint   string `json:"mount_point"`
	FileSystemID string `json:"file_system_id"`
}

type InstanceActionAvailability struct {
	Migrate    InstanceActionAvailabilityDetails `json:"migrate"`
	Rebuild    InstanceActionAvailabilityDetails `json:"rebuild"`
	Restart    InstanceActionAvailabilityDetails `json:"restart"`
	ColdReboot InstanceActionAvailabilityDetails `json:"cold_reboot"`
	Terminate  InstanceActionAvailabilityDetails `json:"terminate"`
}

type InstanceActionAvailabilityDetails struct {
	Available         bool   `json:"available"`
	ReasonCode        string `json:"reason_code,omitempty"`
	ReasonDescription string `json:"reason_description,omitempty"`
}

type ImageSpec struct {
	ID     string `json:"id,omitempty"`
	Family string `json:"family,omitempty"`
}

// LaunchRequest is the MVP subset of the Lambda launch request.
type LaunchRequest struct {
	Name             string                 `json:"name,omitempty"`
	Hostname         string                 `json:"hostname,omitempty"`
	RegionName       string                 `json:"region_name"`
	InstanceTypeName string                 `json:"instance_type_name"`
	UserData         string                 `json:"user_data,omitempty"`
	FileSystemNames  []string               `json:"file_system_names,omitempty"`
	FileSystemMounts []FilesystemMountEntry `json:"file_system_mounts,omitempty"`
	Tags             []TagEntry             `json:"tags,omitempty"`
	Image            *ImageSpec             `json:"image,omitempty"`
	SSHKeyNames      []string               `json:"ssh_key_names,omitempty"`
	FirewallRulesets []FirewallRulesetEntry `json:"firewall_rulesets,omitempty"`
}

func (c *Client) ListInstances(ctx context.Context) ([]Instance, error) {
	var resp struct {
		Data []Instance `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/instances", nil, &resp, false); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (c *Client) GetInstance(ctx context.Context, id string) (*Instance, error) {
	var resp struct {
		Data Instance `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/instances/"+id, nil, &resp, false); err != nil {
		return nil, err
	}
	return &resp.Data, nil
}

func (c *Client) LaunchInstance(ctx context.Context, req LaunchRequest) ([]string, error) {
	var resp struct {
		Data struct {
			InstanceIDs []string `json:"instance_ids"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/v1/instance-operations/launch", req, &resp, true); err != nil {
		return nil, err
	}
	return resp.Data.InstanceIDs, nil
}

func (c *Client) TerminateInstance(ctx context.Context, id string) error {
	req := struct {
		InstanceIDs []string `json:"instance_ids"`
	}{
		InstanceIDs: []string{id},
	}
	return c.do(ctx, http.MethodPost, "/api/v1/instance-operations/terminate", req, nil, false)
}

func (c *Client) ListInstanceTypes(ctx context.Context) (map[string]InstanceTypesItem, error) {
	var resp struct {
		Data map[string]InstanceTypesItem `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/instance-types", nil, &resp, false); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (c *Client) ListImages(ctx context.Context) ([]Image, error) {
	var resp struct {
		Data []Image `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/v1/images", nil, &resp, false); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any, isLaunch bool) error {
	reqURL := c.baseURL.ResolveReference(&url.URL{Path: path})
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if isLaunch {
			if err := c.limiter.WaitLaunch(ctx); err != nil {
				return err
			}
		}
		if err := c.limiter.Wait(ctx); err != nil {
			return err
		}

		var reader io.Reader
		if payload != nil {
			reader = bytes.NewBuffer(payload)
		}

		req, err := http.NewRequestWithContext(ctx, method, reqURL.String(), reader)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := c.http.Do(req)
		if err != nil {
			if !shouldRetry(attempt) {
				return err
			}
			if err := sleepBackoff(ctx, attempt); err != nil {
				return err
			}
			continue
		}

		if resp.StatusCode >= 400 {
			data, _ := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if isRetryableStatus(resp.StatusCode) && shouldRetry(attempt) {
				if err := sleepBackoff(ctx, attempt); err != nil {
					return err
				}
				continue
			}
			return fmt.Errorf("lambda api %s %s failed: %d: %s", method, path, resp.StatusCode, string(data))
		}

		if out == nil {
			_ = resp.Body.Close()
			return nil
		}
		decoder := json.NewDecoder(resp.Body)
		err = decoder.Decode(out)
		_ = resp.Body.Close()
		if err != nil {
			if !shouldRetry(attempt) {
				return err
			}
			if err := sleepBackoff(ctx, attempt); err != nil {
				return err
			}
			continue
		}
		return nil
	}

	return fmt.Errorf("lambda api %s %s failed after %d attempts", method, path, maxAttempts)
}

func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

func shouldRetry(attempt int) bool {
	return attempt+1 < maxAttempts
}

func sleepBackoff(ctx context.Context, attempt int) error {
	base := 500 * time.Millisecond
	backoff := base * time.Duration(1<<attempt)
	if backoff > 10*time.Second {
		backoff = 10 * time.Second
	}
	timer := time.NewTimer(backoff)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
