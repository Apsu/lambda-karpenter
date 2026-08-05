package main

import (
	"path/filepath"
	"testing"

	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

func typesWith(name string, regions ...string) map[string]lambdaclient.InstanceTypesItem {
	item := lambdaclient.InstanceTypesItem{}
	for _, r := range regions {
		item.Regions = append(item.Regions, lambdaclient.Region{Name: r})
	}
	return map[string]lambdaclient.InstanceTypesItem{name: item}
}

func TestCapacityCacheRecordAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seen.json")
	c := &capacityCache{path: path, seen: map[string]map[string]bool{}}

	// First sighting of GH200 in us-east-3 is new.
	if !c.record(typesWith("gpu_1x_gh200", "us-east-3")) {
		t.Fatal("expected first record to report a change")
	}
	// Recording the same pair again is not new.
	if c.record(typesWith("gpu_1x_gh200", "us-east-3")) {
		t.Fatal("expected duplicate record to report no change")
	}
	// A new region for the same type is new.
	if !c.record(typesWith("gpu_1x_gh200", "us-east-1")) {
		t.Fatal("expected new region to report a change")
	}

	if err := c.save(); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Reload from disk and confirm both regions survived the round trip.
	reloaded := loadCapacityCacheFrom(path)
	for _, r := range []string{"us-east-3", "us-east-1"} {
		if !reloaded.seen["gpu_1x_gh200"][r] {
			t.Fatalf("expected %s persisted for gh200, got %#v", r, reloaded.seen)
		}
	}
	// A region never seen must not be reported as seen.
	if reloaded.seen["gpu_1x_gh200"]["australia-east-1"] {
		t.Fatal("never-seen region must not be present")
	}
}
