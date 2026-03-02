package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
	"sigs.k8s.io/yaml"
)

// InstanceCmd is the parent command for instance management.
type InstanceCmd struct {
	List      InstanceListCmd      `cmd:"" help:"List instances."`
	Get       InstanceGetCmd       `cmd:"" help:"Get instance details."`
	Launch    InstanceLaunchCmd    `cmd:"" help:"Launch a new instance."`
	Terminate InstanceTerminateCmd `cmd:"" help:"Terminate an instance."`
}

type InstanceListCmd struct {
	APIFlags
	Limit  int    `name:"limit" default:"0" help:"Limit output to N instances (0 = all)."`
	Region string `name:"region" help:"Filter by region name."`
	Status string `name:"status" help:"Filter by status (e.g. active, booting, unhealthy)."`
	Type   string `name:"type" help:"Filter by instance type name."`
	Tag    string `name:"tag" help:"Filter by tag (key or key=value)."`
}

func (c *InstanceListCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	items, err := client.ListInstances(ctx)
	fatalIf(err)

	// Apply filters.
	filtered := items[:0]
	for _, inst := range items {
		if c.Region != "" && !strings.EqualFold(inst.Region.Name, c.Region) {
			continue
		}
		if c.Status != "" && !strings.EqualFold(inst.Status, c.Status) {
			continue
		}
		if c.Type != "" && !strings.EqualFold(inst.Type.Name, c.Type) {
			continue
		}
		if c.Tag != "" && !matchTag(inst.Tags, c.Tag) {
			continue
		}
		filtered = append(filtered, inst)
	}
	items = filtered

	if c.Limit > 0 && c.Limit < len(items) {
		items = items[:c.Limit]
	}

	var rows [][]string
	for _, inst := range items {
		rows = append(rows, []string{
			inst.ID, inst.Name, inst.Status, inst.Type.Name,
			inst.Region.Name, inst.IP, formatTags(inst.Tags),
		})
	}
	printListTable(
		[]string{"ID", "NAME", "STATUS", "TYPE", "REGION", "IP", "TAGS"},
		rows,
	)
	return nil
}

// matchTag checks whether any tag matches the filter. Supports "key" (tag exists)
// and "key=value" (exact value match).
func matchTag(tags []lambdaclient.TagEntry, filter string) bool {
	key, value, hasValue := strings.Cut(filter, "=")
	for _, t := range tags {
		if !strings.EqualFold(t.Key, key) {
			continue
		}
		if !hasValue || t.Value == value {
			return true
		}
	}
	return false
}

// shortDuration formats a duration as a human-friendly age string (e.g. "3d", "5h", "12m").
func shortDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return "<1m"
	}
}

// formatTags formats tag entries as a compact "k=v,k=v" string.
func formatTags(tags []lambdaclient.TagEntry) string {
	if len(tags) == 0 {
		return ""
	}
	parts := make([]string, len(tags))
	for i, t := range tags {
		parts[i] = t.Key + "=" + t.Value
	}
	return strings.Join(parts, ",")
}

type InstanceGetCmd struct {
	APIFlags
	ID string `arg:"" help:"Instance ID."`
}

func (c *InstanceGetCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	inst, err := client.GetInstance(ctx, c.ID)
	fatalIf(err)

	var fields [][]string
	field := func(label, value string) {
		if value != "" {
			fields = append(fields, []string{label, value})
		}
	}

	field("Type", inst.Type.Name+" — "+inst.Status)
	field("ID", inst.ID)
	field("Name", inst.Name)
	field("Hostname", inst.Hostname)
	field("Region", inst.Region.Name+" ("+inst.Region.Description+")")
	field("IP", inst.IP)
	field("Private IP", inst.PrivateIP)
	field("Description", inst.Type.Description)
	field("GPU", inst.Type.GPUDesc)
	field("Price", fmt.Sprintf("$%.2f/hr", float64(inst.Type.PriceCents)/100.0))
	specs := inst.Type.Specs
	field("Specs", fmt.Sprintf("%d vCPU, %d GiB RAM, %d GiB disk, %d GPU",
		specs.VCPUs, specs.MemoryGiB, specs.StorageGiB, specs.GPUs))
	if !inst.CreatedAt.IsZero() {
		field("Created", inst.CreatedAt.Format(time.RFC3339)+" ("+shortDuration(time.Since(inst.CreatedAt))+")")
	}
	if len(inst.SSHKeyNames) > 0 {
		field("SSH Keys", strings.Join(inst.SSHKeyNames, ", "))
	}
	if len(inst.FileSystemNames) > 0 {
		field("Filesystems", strings.Join(inst.FileSystemNames, ", "))
	}
	if len(inst.Tags) > 0 {
		field("Tags", formatTags(inst.Tags))
	}

	printDetailTable(fields)
	return nil
}

// LaunchConfig is the YAML config file format for launch.
type LaunchConfig struct {
	Name            string            `json:"name" yaml:"name"`
	Hostname        string            `json:"hostname" yaml:"hostname"`
	Region          string            `json:"region" yaml:"region"`
	InstanceType    string            `json:"instanceType" yaml:"instanceType"`
	UserData        string            `json:"userData" yaml:"userData"`
	UserDataFile    string            `json:"userDataFile" yaml:"userDataFile"`
	ImageID         string            `json:"imageID" yaml:"imageID"`
	ImageFamily     string            `json:"imageFamily" yaml:"imageFamily"`
	SSHKeyNames     []string          `json:"sshKeyNames" yaml:"sshKeyNames"`
	FirewallRuleIDs []string          `json:"firewallRulesetIDs" yaml:"firewallRulesetIDs"`
	Tags            map[string]string `json:"tags" yaml:"tags"`
}

type InstanceLaunchCmd struct {
	APIFlags
	Config        string        `name:"config" help:"Path to YAML config."`
	Confirm       bool          `name:"confirm" help:"Skip interactive confirmation."`
	Name          string        `name:"name" help:"Instance name."`
	Hostname      string        `name:"hostname" help:"Hostname."`
	Region        string        `name:"region" help:"Region name."`
	InstanceType  string        `name:"instance-type" help:"Instance type name."`
	UserData      string        `name:"user-data" help:"cloud-init user-data content."`
	UserDataFile  string        `name:"user-data-file" help:"Path to cloud-init user-data."`
	ImageID       string        `name:"image-id" help:"Image ID."`
	ImageFamily   string        `name:"image-family" help:"Image family."`
	SSHKeys       []string      `name:"ssh-key" help:"SSH key name (repeatable)."`
	FirewallIDs   []string      `name:"firewall-id" help:"Firewall ruleset ID (repeatable)."`
	Tags          []string      `name:"tag" help:"Tag in key=value form (repeatable)."`
	Retry         time.Duration `name:"retry" short:"r" default:"0" help:"Retry on capacity errors for this duration (e.g. 30m)."`
	RetryInterval time.Duration `name:"retry-interval" default:"30s" help:"Time between retry attempts."`
}

func (c *InstanceLaunchCmd) Run() error {
	cfg := LaunchConfig{
		Tags: map[string]string{},
	}
	if c.Config != "" {
		data, err := os.ReadFile(c.Config)
		fatalIf(err)
		fatalIf(yaml.Unmarshal(data, &cfg))
		if cfg.Tags == nil {
			cfg.Tags = map[string]string{}
		}
	}

	applyStringOverride(&cfg.Name, c.Name)
	applyStringOverride(&cfg.Hostname, c.Hostname)
	applyStringOverride(&cfg.Region, c.Region)
	applyStringOverride(&cfg.InstanceType, c.InstanceType)
	applyStringOverride(&cfg.UserData, c.UserData)
	applyStringOverride(&cfg.UserDataFile, c.UserDataFile)
	applyStringOverride(&cfg.ImageID, c.ImageID)
	applyStringOverride(&cfg.ImageFamily, c.ImageFamily)
	if len(c.SSHKeys) > 0 {
		cfg.SSHKeyNames = append([]string(nil), c.SSHKeys...)
	}
	if len(c.FirewallIDs) > 0 {
		cfg.FirewallRuleIDs = append([]string(nil), c.FirewallIDs...)
	}
	for _, kv := range c.Tags {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			fatalf("invalid tag %q, expected key=value", kv)
		}
		cfg.Tags[k] = v
	}

	if cfg.Region == "" {
		fatalf("region is required")
	}
	if cfg.InstanceType == "" {
		fatalf("instance-type is required")
	}
	if len(cfg.SSHKeyNames) == 0 {
		fatalf("at least one --ssh-key is required")
	}
	if cfg.ImageID != "" && cfg.ImageFamily != "" {
		fatalf("only one of --image-id or --image-family may be set")
	}

	if cfg.UserDataFile != "" {
		data, err := os.ReadFile(cfg.UserDataFile)
		fatalIf(err)
		cfg.UserData = string(data)
	}
	if cfg.Hostname == "" && cfg.Name != "" {
		cfg.Hostname = cfg.Name
	}

	if !c.Confirm {
		label := cfg.InstanceType + " in " + cfg.Region
		if cfg.Name != "" {
			label = cfg.Name + " (" + label + ")"
		}
		confirmAction("launch " + label)
	}

	client := c.mustClient()
	req := lambdaclient.LaunchRequest{
		Name:             cfg.Name,
		Hostname:         cfg.Hostname,
		RegionName:       cfg.Region,
		InstanceTypeName: cfg.InstanceType,
		UserData:         cfg.UserData,
		SSHKeyNames:      cfg.SSHKeyNames,
	}
	if cfg.ImageID != "" || cfg.ImageFamily != "" {
		req.Image = &lambdaclient.ImageSpec{ID: cfg.ImageID, Family: cfg.ImageFamily}
	}
	if len(cfg.FirewallRuleIDs) > 0 {
		for _, id := range cfg.FirewallRuleIDs {
			req.FirewallRulesets = append(req.FirewallRulesets, lambdaclient.FirewallRulesetEntry{ID: id})
		}
	}
	for k, v := range cfg.Tags {
		req.Tags = append(req.Tags, lambdaclient.TagEntry{Key: k, Value: v})
	}

	ctx := context.Background()
	ids, err := client.LaunchInstance(ctx, req)
	if err == nil {
		for _, id := range ids {
			fmt.Println(id)
		}
		return nil
	}

	// No retry requested — fail immediately.
	if c.Retry <= 0 || !lambdaclient.IsCapacityError(err) {
		fatalIf(err)
	}

	// Retry loop for capacity errors.
	deadline := time.Now().Add(c.Retry)
	attempt := 1
	fmt.Fprintf(os.Stderr, "%s %s — retrying for %s (every %s)\n",
		styleChanged.Render("no capacity"),
		styleDim.Render(cfg.InstanceType+" in "+cfg.Region),
		c.Retry, c.RetryInterval)

	for time.Now().Before(deadline) {
		time.Sleep(c.RetryInterval)
		attempt++
		remaining := time.Until(deadline).Round(time.Second)

		ids, err = client.LaunchInstance(ctx, req)
		if err == nil {
			fmt.Fprintf(os.Stderr, "%s launched on attempt %d\n",
				styleAdded.Render("ok"),
				attempt)
			for _, id := range ids {
				fmt.Println(id)
			}
			return nil
		}

		if !lambdaclient.IsCapacityError(err) {
			fatalIf(err)
		}

		fmt.Fprintf(os.Stderr, "%s attempt %d, %s remaining\n",
			styleChanged.Render("no capacity"),
			attempt, remaining)
	}

	fatalf("gave up after %d attempts over %s: %v", attempt, c.Retry, err)
	return nil
}

type InstanceTerminateCmd struct {
	APIFlags
	ID      string `arg:"" help:"Instance ID."`
	Confirm bool   `name:"confirm" help:"Skip interactive confirmation."`
}

func (c *InstanceTerminateCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()

	inst, err := client.GetInstance(ctx, c.ID)
	fatalIf(err)

	if !c.Confirm {
		summary := fmt.Sprintf("terminate %s (%s, %s, %s, ip=%s)",
			inst.ID, inst.Name, inst.Type.Name, inst.Region.Name, inst.IP)
		confirmAction(summary)
	}

	fatalIf(client.TerminateInstance(ctx, c.ID))
	fmt.Printf("terminated %s\n", c.ID)
	return nil
}
