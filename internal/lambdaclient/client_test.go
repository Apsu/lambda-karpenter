package lambdaclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evecallicoat/lambda-karpenter/internal/ratelimit"
)

func newTestClient(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	limiter := ratelimit.New(1000, 0)
	client, err := New(srv.URL, "test-token", limiter)
	if err != nil {
		t.Fatalf("New client: %v", err)
	}
	return client
}

func assertAuth(t *testing.T, r *http.Request) {
	t.Helper()
	if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
		t.Fatalf("expected Authorization header, got %q", got)
	}
}

func TestListInstances(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/instances" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		assertAuth(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":                "i-1",
					"status":            "active",
					"ssh_key_names":     []string{"Eve"},
					"file_system_names": []string{},
					"instance_type": map[string]any{
						"name":                 "gpu_1x_gh200",
						"description":          "GH200",
						"gpu_description":      "GH200",
						"price_cents_per_hour": 1000,
						"specs": map[string]any{
							"vcpus":       96,
							"memory_gib":  480,
							"storage_gib": 0,
							"gpus":        1,
						},
					},
					"region": map[string]any{"name": "us-east-3"},
					"actions": map[string]any{
						"migrate":     map[string]any{"available": false},
						"rebuild":     map[string]any{"available": true},
						"restart":     map[string]any{"available": true},
						"cold_reboot": map[string]any{"available": true},
						"terminate":   map[string]any{"available": true},
					},
				},
			},
		})
	})
	client := newTestClient(t, handler)
	items, err := client.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("ListInstances: %v", err)
	}
	if len(items) != 1 || items[0].ID != "i-1" {
		t.Fatalf("unexpected instances: %#v", items)
	}
}

func TestGetInstance(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/instances/i-2" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		assertAuth(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"id":                "i-2",
				"status":            "booting",
				"ssh_key_names":     []string{"Eve"},
				"file_system_names": []string{},
				"instance_type": map[string]any{
					"name":                 "gpu_1x_gh200",
					"description":          "GH200",
					"gpu_description":      "GH200",
					"price_cents_per_hour": 1000,
					"specs": map[string]any{
						"vcpus":       96,
						"memory_gib":  480,
						"storage_gib": 0,
						"gpus":        1,
					},
				},
				"region": map[string]any{"name": "us-east-3"},
				"actions": map[string]any{
					"migrate":     map[string]any{"available": false},
					"rebuild":     map[string]any{"available": true},
					"restart":     map[string]any{"available": true},
					"cold_reboot": map[string]any{"available": true},
					"terminate":   map[string]any{"available": true},
				},
			},
		})
	})
	client := newTestClient(t, handler)
	item, err := client.GetInstance(context.Background(), "i-2")
	if err != nil {
		t.Fatalf("GetInstance: %v", err)
	}
	if item.ID != "i-2" || item.Status != "booting" {
		t.Fatalf("unexpected instance: %#v", item)
	}
}

func TestLaunchInstance(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/instance-operations/launch" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		assertAuth(t, r)
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if body["region_name"] != "us-east-3" || body["instance_type_name"] != "gpu_1x_gh200" {
			t.Fatalf("unexpected body: %#v", body)
		}
		if _, ok := body["ssh_key_names"]; !ok {
			t.Fatalf("expected ssh_key_names in body")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"instance_ids": []string{"i-3"},
			},
		})
	})
	client := newTestClient(t, handler)
	ids, err := client.LaunchInstance(context.Background(), LaunchRequest{
		RegionName:       "us-east-3",
		InstanceTypeName: "gpu_1x_gh200",
		SSHKeyNames:      []string{"Eve"},
		Hostname:         "gh200-pool-abc",
	})
	if err != nil {
		t.Fatalf("LaunchInstance: %v", err)
	}
	if len(ids) != 1 || ids[0] != "i-3" {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}

func TestTerminateInstance(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/instance-operations/terminate" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		assertAuth(t, r)
		var body struct {
			InstanceIDs []string `json:"instance_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if len(body.InstanceIDs) != 1 || body.InstanceIDs[0] != "i-4" {
			t.Fatalf("unexpected body: %#v", body)
		}
		w.WriteHeader(http.StatusOK)
	})
	client := newTestClient(t, handler)
	if err := client.TerminateInstance(context.Background(), "i-4"); err != nil {
		t.Fatalf("TerminateInstance: %v", err)
	}
}

func TestListInstanceTypes(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/instance-types" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		assertAuth(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": map[string]any{
				"gpu_1x_gh200": map[string]any{
					"instance_type": map[string]any{
						"name":            "gpu_1x_gh200",
						"description":     "GH200",
						"gpu_description": "GH200",
						"specs": map[string]any{
							"vcpus":       96,
							"memory_gib":  480,
							"storage_gib": 0,
							"gpus":        1,
						},
						"price_cents_per_hour": 1000,
					},
					"regions_with_capacity_available": []map[string]any{
						{"name": "us-east-3"},
					},
				},
			},
		})
	})
	client := newTestClient(t, handler)
	items, err := client.ListInstanceTypes(context.Background())
	if err != nil {
		t.Fatalf("ListInstanceTypes: %v", err)
	}
	if _, ok := items["gpu_1x_gh200"]; !ok {
		t.Fatalf("expected instance type in response")
	}
}

func TestListImages(t *testing.T) {
	now := time.Now().UTC()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/images" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		assertAuth(t, r)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{
				{
					"id":           "img-1",
					"family":       "lambda-stack-24-04",
					"name":         "Lambda Stack 24.04",
					"architecture": "arm64",
					"region":       map[string]any{"name": "us-east-3"},
					"created_time": now.Format(time.RFC3339),
					"updated_time": now.Add(1 * time.Hour).Format(time.RFC3339),
				},
			},
		})
	})
	client := newTestClient(t, handler)
	images, err := client.ListImages(context.Background())
	if err != nil {
		t.Fatalf("ListImages: %v", err)
	}
	if len(images) != 1 || !strings.EqualFold(images[0].Arch, "arm64") {
		t.Fatalf("unexpected images: %#v", images)
	}
}

// --- Error path tests ---

func TestAPIError404(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": {"code": "not_found", "message": "instance not found"}}`))
	})
	client := newTestClient(t, handler)
	_, err := client.GetInstance(context.Background(), "i-missing")
	if err == nil {
		t.Fatal("expected error for 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("expected 404 in error, got: %v", err)
	}
}

func TestAPIError500Retries(t *testing.T) {
	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": "internal"}`))
	})
	client := newTestClient(t, handler)
	_, err := client.ListInstances(context.Background())
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if got := attempts.Load(); got < 2 {
		t.Fatalf("expected multiple retry attempts, got %d", got)
	}
}

func TestAPIError429Retries(t *testing.T) {
	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error": "rate_limited"}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
	})
	client := newTestClient(t, handler)
	items, err := client.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if items == nil {
		t.Fatal("expected non-nil result")
	}
	if got := attempts.Load(); got < 3 {
		t.Fatalf("expected at least 3 attempts, got %d", got)
	}
}

func TestContextCancellation(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow handler — context should cancel before response
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	})
	client := newTestClient(t, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.ListInstances(ctx)
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
}

func TestMalformedJSONNotRetried(t *testing.T) {
	var attempts atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{not valid json`))
	})
	client := newTestClient(t, handler)
	_, err := client.ListInstances(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Fatalf("expected decode error, got: %v", err)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("expected exactly 1 attempt (no retry for JSON errors), got %d", got)
	}
}

func TestIsCapacityError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"lambda api POST /api/v1/instance-operations/launch failed: 503: no capacity", true},
		{"no available instances in region", true},
		{"lambda api POST /api/v1/instance-operations/launch failed: 503: service unavailable", true},
		{"lambda api GET /api/v1/instances failed: 404: not found", false},
		{"connection refused", false},
	}
	for _, tc := range tests {
		t.Run(tc.msg, func(t *testing.T) {
			got := IsCapacityError(fmt.Errorf("%s", tc.msg))
			if got != tc.want {
				t.Fatalf("IsCapacityError(%q) = %v, want %v", tc.msg, got, tc.want)
			}
		})
	}
}
