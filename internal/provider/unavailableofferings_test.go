package provider

import (
	"testing"
	"time"
)

func TestUnavailableOfferingsMarkAndCheck(t *testing.T) {
	u := NewUnavailableOfferings(5 * time.Minute)

	if u.IsUnavailable("gpu_1x_gh200", "us-east-3") {
		t.Fatal("should not be unavailable before marking")
	}

	u.MarkUnavailable("gpu_1x_gh200", "us-east-3")

	if !u.IsUnavailable("gpu_1x_gh200", "us-east-3") {
		t.Fatal("should be unavailable after marking")
	}
}

func TestUnavailableOfferingsExpiry(t *testing.T) {
	u := NewUnavailableOfferings(10 * time.Millisecond)

	u.MarkUnavailable("gpu_1x_gh200", "us-east-3")
	if !u.IsUnavailable("gpu_1x_gh200", "us-east-3") {
		t.Fatal("should be unavailable immediately after marking")
	}

	time.Sleep(20 * time.Millisecond)

	if u.IsUnavailable("gpu_1x_gh200", "us-east-3") {
		t.Fatal("should be available after TTL expires")
	}
}

func TestUnavailableOfferingsDifferentKeys(t *testing.T) {
	u := NewUnavailableOfferings(5 * time.Minute)

	u.MarkUnavailable("gpu_1x_gh200", "us-east-3")

	if !u.IsUnavailable("gpu_1x_gh200", "us-east-3") {
		t.Fatal("marked combo should be unavailable")
	}
	if u.IsUnavailable("gpu_1x_a100", "us-east-3") {
		t.Fatal("different instance type should not be affected")
	}
	if u.IsUnavailable("gpu_1x_gh200", "us-west-1") {
		t.Fatal("different region should not be affected")
	}
}

func TestUnavailableOfferingsRemarkExtendsTTL(t *testing.T) {
	u := NewUnavailableOfferings(50 * time.Millisecond)

	u.MarkUnavailable("gpu_1x_gh200", "us-east-3")
	time.Sleep(30 * time.Millisecond)

	// Re-mark before expiry — should extend the TTL.
	u.MarkUnavailable("gpu_1x_gh200", "us-east-3")
	time.Sleep(30 * time.Millisecond)

	// 60ms total, but re-marked at 30ms with 50ms TTL → should still be unavailable.
	if !u.IsUnavailable("gpu_1x_gh200", "us-east-3") {
		t.Fatal("should still be unavailable after re-mark")
	}
}
