package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

// capacityCache remembers which instance type → region pairs have ever shown
// available capacity. The Lambda API only reports *current* capacity (each
// type's regions_with_capacity_available), with no field for where a type is
// merely offered. Scarce GPUs (e.g. GH200 in us-east-3) flicker in and out of
// capacity, so a single snapshot cannot tell "offered here but momentarily
// empty" apart from "never offered here".
//
// This cache accumulates the union of regions each type has been seen with
// capacity, across both the running session and prior runs (persisted to a
// dotfile). The launch form uses it to annotate type×region pairs honestly:
// available now, offered-but-empty, or never-seen.
type capacityCache struct {
	path string
	seen map[string]map[string]bool // instance type name -> set of region names
}

// capacityCachePath returns the on-disk location of the cache. The data is
// account/catalog-global rather than project-specific, so it lives under the
// user's home directory.
func capacityCachePath() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".lambdactl", "seen-capacity.json")
	}
	return ".lambdactl-seen-capacity.json"
}

// loadCapacityCache reads the cache from its default home-dir location.
func loadCapacityCache() *capacityCache {
	return loadCapacityCacheFrom(capacityCachePath())
}

// loadCapacityCacheFrom reads the cache from path, returning an empty (but
// usable) cache if the file is missing or unreadable.
func loadCapacityCacheFrom(path string) *capacityCache {
	c := &capacityCache{path: path, seen: make(map[string]map[string]bool)}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var stored struct {
		Types map[string][]string `json:"types"`
	}
	if json.Unmarshal(data, &stored) != nil {
		return c
	}
	for name, regions := range stored.Types {
		set := make(map[string]bool, len(regions))
		for _, r := range regions {
			if r != "" {
				set[r] = true
			}
		}
		if len(set) > 0 {
			c.seen[name] = set
		}
	}
	return c
}

// record merges the regions that currently have capacity into the cache.
// Returns true if any new type→region pair was added.
func (c *capacityCache) record(items map[string]lambdaclient.InstanceTypesItem) bool {
	changed := false
	for name, item := range items {
		for _, r := range item.Regions {
			if r.Name == "" {
				continue
			}
			if c.seen[name] == nil {
				c.seen[name] = make(map[string]bool)
			}
			if !c.seen[name][r.Name] {
				c.seen[name][r.Name] = true
				changed = true
			}
		}
	}
	return changed
}

// marshal serializes the cache to stable JSON. Call it on the main goroutine;
// the returned bytes can then be written from a background command without
// racing concurrent mutations of the map.
func (c *capacityCache) marshal() ([]byte, error) {
	stored := struct {
		Types map[string][]string `json:"types"`
	}{Types: make(map[string][]string, len(c.seen))}
	for name, set := range c.seen {
		regions := make([]string, 0, len(set))
		for r := range set {
			regions = append(regions, r)
		}
		sort.Strings(regions)
		stored.Types[name] = regions
	}
	return json.MarshalIndent(stored, "", "  ")
}

// save writes the cache to disk synchronously. Used by non-TUI commands.
func (c *capacityCache) save() error {
	data, err := c.marshal()
	if err != nil {
		return err
	}
	return writeCapacityCache(c.path, data)
}

// persistCmd marshals the cache now (on the caller's goroutine) and returns a
// command that writes it in the background. Returns nil on marshal failure.
func (c *capacityCache) persistCmd() tea.Cmd {
	data, err := c.marshal()
	if err != nil {
		return nil
	}
	path := c.path
	return func() tea.Msg {
		_ = writeCapacityCache(path, data)
		return nil
	}
}

// writeCapacityCache atomically writes data to path, creating the directory.
func writeCapacityCache(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
