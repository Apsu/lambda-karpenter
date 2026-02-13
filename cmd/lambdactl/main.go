package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/alecthomas/kong"
	"github.com/evecallicoat/lambda-karpenter/internal/lambdaclient"
	"github.com/evecallicoat/lambda-karpenter/internal/ratelimit"
	"github.com/joho/godotenv"
	"golang.org/x/term"
	"sigs.k8s.io/yaml"
)

// CLI is the top-level command tree.
type CLI struct {
	ListInstances     ListInstancesCmd     `cmd:"" name:"list-instances" help:"List Lambda instances."`
	GetInstance       GetInstanceCmd       `cmd:"" name:"get-instance" help:"Get instance details."`
	ListInstanceTypes ListInstanceTypesCmd `cmd:"" name:"list-instance-types" help:"List available instance types."`
	ListImages        ListImagesCmd        `cmd:"" name:"list-images" help:"List available images."`
	GetImage          GetImageCmd          `cmd:"" name:"get-image" help:"Get image details."`
	Launch            LaunchCmd            `cmd:"" help:"Launch a new instance."`
	Terminate         TerminateCmd         `cmd:"" help:"Terminate an instance."`
	K8s               K8sCmd               `cmd:"" name:"k8s" help:"Kubernetes cluster management."`
}

// APIFlags are shared flags for Lambda API commands.
type APIFlags struct {
	BaseURL   string `name:"base-url" env:"LAMBDA_API_BASE_URL" default:"https://cloud.lambda.ai" help:"Lambda API base URL."`
	Token     string `name:"token" env:"LAMBDA_API_TOKEN" help:"Lambda API token."`
	TokenFile string `name:"token-file" help:"Path to token file."`
}

func (f *APIFlags) resolveToken() {
	if f.Token == "" && f.TokenFile == "" {
		if _, err := os.Stat("lambda-api.key"); err == nil {
			f.TokenFile = "lambda-api.key"
		}
	}
	if f.Token == "" && f.TokenFile != "" {
		data, err := os.ReadFile(f.TokenFile)
		fatalIf(err)
		f.Token = strings.TrimSpace(string(data))
	}
}

func (f *APIFlags) mustClient() *lambdaclient.Client {
	f.resolveToken()
	return mustClientWith(f.BaseURL, f.Token)
}

// --- API commands ---

type ListInstancesCmd struct {
	APIFlags
	Limit int `name:"limit" default:"0" help:"Limit output to N instances (0 = all)."`
}

func (c *ListInstancesCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	items, err := client.ListInstances(ctx)
	fatalIf(err)
	if c.Limit > 0 && c.Limit < len(items) {
		items = items[:c.Limit]
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tSTATUS\tTYPE\tREGION\tIP\tAGE\tTAGS")
	for _, inst := range items {
		age := ""
		if !inst.CreatedAt.IsZero() {
			age = shortDuration(time.Since(inst.CreatedAt))
		}
		tags := formatTags(inst.Tags)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			inst.ID, inst.Name, inst.Status, inst.Type.Name, inst.Region.Name, inst.IP, age, tags)
	}
	w.Flush()
	return nil
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

type GetInstanceCmd struct {
	APIFlags
	ID string `name:"id" required:"" help:"Instance ID."`
}

func (c *GetInstanceCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	inst, err := client.GetInstance(ctx, c.ID)
	fatalIf(err)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	field := func(label, value string) {
		if value != "" {
			fmt.Fprintf(w, "  %s:\t%s\n", label, value)
		}
	}

	fmt.Fprintf(w, "%s\t%s\n", inst.Type.Name, inst.Status)
	field("ID", inst.ID)
	field("Name", inst.Name)
	field("Hostname", inst.Hostname)
	field("Region", inst.Region.Name+" ("+inst.Region.Description+")")
	field("IP", inst.IP)
	field("Private IP", inst.PrivateIP)
	field("Type", inst.Type.Description)
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
	w.Flush()
	return nil
}

type ListInstanceTypesCmd struct {
	APIFlags
}

func (c *ListInstanceTypesCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	items, err := client.ListInstanceTypes(ctx)
	fatalIf(err)
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tGPU\tVCPU\tRAM\tDISK\tGPUS\tPRICE\tREGIONS")
	for _, name := range names {
		item := items[name]
		regionNames := make([]string, 0, len(item.Regions))
		for _, region := range item.Regions {
			if region.Name != "" {
				regionNames = append(regionNames, region.Name)
			}
		}
		regions := "-"
		if len(regionNames) > 0 {
			sort.Strings(regionNames)
			regions = strings.Join(regionNames, ",")
		}
		fmt.Fprintf(w, "%s\t%s\t%d\t%d GiB\t%d GiB\t%d\t$%.2f/hr\t%s\n",
			name,
			item.InstanceType.GPUDesc,
			item.InstanceType.Specs.VCPUs,
			item.InstanceType.Specs.MemoryGiB,
			item.InstanceType.Specs.StorageGiB,
			item.InstanceType.Specs.GPUs,
			float64(item.InstanceType.PriceCents)/100.0,
			regions,
		)
	}
	w.Flush()
	return nil
}

type ListImagesCmd struct {
	APIFlags
}

func (c *ListImagesCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	items, err := client.ListImages(ctx)
	fatalIf(err)
	for _, img := range items {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", img.ID, img.Family, img.Name, img.Region.Name, img.Arch)
	}
	return nil
}

type GetImageCmd struct {
	APIFlags
	ID     string `name:"id" help:"Image ID."`
	Region string `name:"region" help:"Region name."`
	Family string `name:"family" help:"Image family."`
	Name   string `name:"name" help:"Image name."`
	Arch   string `name:"arch" help:"Architecture filter."`
	Latest bool   `name:"latest" help:"Return only the latest matching image."`
}

func (c *GetImageCmd) Run() error {
	if c.ID == "" && c.Family == "" && c.Name == "" {
		fatalf("one of --id, --family, or --name is required")
	}

	client := c.mustClient()
	ctx := context.Background()
	items, err := client.ListImages(ctx)
	fatalIf(err)

	var matches []lambdaclient.Image
	for _, img := range items {
		if c.ID != "" && img.ID != c.ID {
			continue
		}
		if c.Family != "" && img.Family != c.Family {
			continue
		}
		if c.Name != "" && img.Name != c.Name {
			continue
		}
		if c.Region != "" && img.Region.Name != c.Region {
			continue
		}
		if c.Arch != "" && img.Arch != c.Arch {
			continue
		}
		matches = append(matches, img)
	}

	if c.Latest && len(matches) > 0 {
		latestImg := matches[0]
		for _, img := range matches[1:] {
			if img.UpdatedTime.After(latestImg.UpdatedTime) {
				latestImg = img
			}
		}
		matches = []lambdaclient.Image{latestImg}
	}

	for _, img := range matches {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n",
			img.ID, img.Family, img.Name, img.Region.Name, img.Arch,
			img.UpdatedTime.Format(time.RFC3339))
	}
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

type LaunchCmd struct {
	APIFlags
	Config       string   `name:"config" help:"Path to YAML config."`
	Confirm      bool     `name:"confirm" help:"Skip interactive confirmation."`
	Name         string   `name:"name" help:"Instance name."`
	Hostname     string   `name:"hostname" help:"Hostname."`
	Region       string   `name:"region" help:"Region name."`
	InstanceType string   `name:"instance-type" help:"Instance type name."`
	UserData     string   `name:"user-data" help:"cloud-init user-data content."`
	UserDataFile string   `name:"user-data-file" help:"Path to cloud-init user-data."`
	ImageID      string   `name:"image-id" help:"Image ID."`
	ImageFamily  string   `name:"image-family" help:"Image family."`
	SSHKeys      []string `name:"ssh-key" help:"SSH key name (repeatable)."`
	FirewallIDs  []string `name:"firewall-id" help:"Firewall ruleset ID (repeatable)."`
	Tags         []string `name:"tag" help:"Tag in key=value form (repeatable)."`
}

func (c *LaunchCmd) Run() error {
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
	fatalIf(err)
	for _, id := range ids {
		fmt.Println(id)
	}
	return nil
}

type TerminateCmd struct {
	APIFlags
	ID      string `name:"id" required:"" help:"Instance ID."`
	Confirm bool   `name:"confirm" help:"Skip interactive confirmation."`
}

func (c *TerminateCmd) Run() error {
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

// --- Helpers ---

func main() {
	// Load .env (project defaults) then .env.local (personal overrides).
	// Existing env vars are never overwritten. Missing files are silently skipped.
	_ = godotenv.Load(".env", ".env.local")

	var cli CLI
	ctx := kong.Parse(&cli,
		kong.Name("lambdactl"),
		kong.Description("Lambda Cloud CLI for instance and cluster management."),
		kong.UsageOnError(),
	)
	fatalIf(ctx.Run())
}

func mustClientWith(baseURL, token string) *lambdaclient.Client {
	if token == "" {
		fatalf("token is required (set LAMBDA_API_TOKEN or --token)")
	}
	limiter := ratelimit.New(1, 5*time.Second)
	client, err := lambdaclient.New(baseURL, token, limiter)
	fatalIf(err)
	return client
}

func applyStringOverride(dst *string, val string) {
	if val != "" {
		*dst = val
	}
}

func confirmAction(action string) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		fatalf("%s: refusing without --confirm (stdin is not a terminal)", action)
	}
	fmt.Fprintf(os.Stderr, "%s — proceed? [y/N] ", action)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		fatalf("aborted")
	}
	resp := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if resp != "y" && resp != "yes" {
		fatalf("aborted")
	}
}

func fatalIf(err error) {
	if err == nil {
		return
	}
	fatalf("%v", err)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
