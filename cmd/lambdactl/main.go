package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/evecallicoat/lambda-karpenter/internal/lambdaclient"
	"github.com/evecallicoat/lambda-karpenter/internal/ratelimit"
	"sigs.k8s.io/yaml"
)

const defaultBaseURL = "https://cloud.lambda.ai"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	switch cmd {
	case "list-instances":
		listInstances(os.Args[2:])
	case "get-instance":
		getInstance(os.Args[2:])
	case "list-instance-types":
		listInstanceTypes(os.Args[2:])
	case "list-images":
		listImages(os.Args[2:])
	case "launch":
		launchInstance(os.Args[2:])
	case "terminate":
		terminateInstances(os.Args[2:])
	case "get-image":
		getImage(os.Args[2:])
	case "k8s":
		handleK8s(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Usage: lambdactl <command> [flags]")
	fmt.Fprintln(os.Stderr, "Commands:")
	fmt.Fprintln(os.Stderr, "  list-instances [--limit N]")
	fmt.Fprintln(os.Stderr, "  get-instance --id <instance-id>")
	fmt.Fprintln(os.Stderr, "  list-instance-types")
	fmt.Fprintln(os.Stderr, "  list-images")
	fmt.Fprintln(os.Stderr, "  get-image --id <image-id> [--region us-east-3]")
	fmt.Fprintln(os.Stderr, "  launch [--config file.yaml] [--confirm] [flags]")
	fmt.Fprintln(os.Stderr, "  terminate --id <instance-id> [--confirm]")
	fmt.Fprintln(os.Stderr, "  k8s <command> [flags]")
}

func listInstances(args []string) {
	fs := flag.NewFlagSet("list-instances", flag.ExitOnError)
	limit := fs.Int("limit", 0, "Limit output to N instances (0 = all)")
	base, token := addCommonFlags(fs)
	tokenFile := fs.String("token-file", "", "Path to token file")
	_ = fs.Parse(args)

	if *token == "" && *tokenFile == "" {
		if _, err := os.Stat("lambda-eve-karpenter.key"); err == nil {
			*tokenFile = "lambda-eve-karpenter.key"
		}
	}
	if *token == "" && *tokenFile != "" {
		data, err := os.ReadFile(*tokenFile)
		fatalIf(err)
		*token = strings.TrimSpace(string(data))
	}

	client := mustClientWith(*base, *token)
	ctx := context.Background()
	items, err := client.ListInstances(ctx)
	fatalIf(err)
	if *limit > 0 && *limit < len(items) {
		items = items[:*limit]
	}
	for _, inst := range items {
		fmt.Printf("%s\t%s\t%s\t%s\n", inst.ID, inst.Status, inst.Type.Name, inst.Region.Name)
	}
}

func getInstance(args []string) {
	fs := flag.NewFlagSet("get-instance", flag.ExitOnError)
	id := fs.String("id", "", "instance id")
	base, token := addCommonFlags(fs)
	tokenFile := fs.String("token-file", "", "Path to token file")
	_ = fs.Parse(args)
	if *id == "" {
		fatalf("--id is required")
	}
	if *token == "" && *tokenFile == "" {
		if _, err := os.Stat("lambda-eve-karpenter.key"); err == nil {
			*tokenFile = "lambda-eve-karpenter.key"
		}
	}
	if *token == "" && *tokenFile != "" {
		data, err := os.ReadFile(*tokenFile)
		fatalIf(err)
		*token = strings.TrimSpace(string(data))
	}

	client := mustClientWith(*base, *token)
	ctx := context.Background()
	inst, err := client.GetInstance(ctx, *id)
	fatalIf(err)
	fmt.Printf("id=%s status=%s type=%s region=%s ip=%s private_ip=%s\n", inst.ID, inst.Status, inst.Type.Name, inst.Region.Name, inst.IP, inst.PrivateIP)
}

func listInstanceTypes(args []string) {
	client := mustClient(args)
	ctx := context.Background()
	items, err := client.ListInstanceTypes(ctx)
	fatalIf(err)
	names := make([]string, 0, len(items))
	for name := range items {
		names = append(names, name)
	}
	sort.Strings(names)
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
		fmt.Printf("%s\t%d vcpu\t%d GiB\t%d gpu\t$%.2f\tregions=%d [%s]\n",
			name,
			item.InstanceType.Specs.VCPUs,
			item.InstanceType.Specs.MemoryGiB,
			item.InstanceType.Specs.GPUs,
			float64(item.InstanceType.PriceCents)/100.0,
			len(item.Regions),
			regions,
		)
	}
}

func listImages(args []string) {
	client := mustClient(args)
	ctx := context.Background()
	items, err := client.ListImages(ctx)
	fatalIf(err)
	for _, img := range items {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\n", img.ID, img.Family, img.Name, img.Region.Name, img.Arch)
	}
}

func getImage(args []string) {
	fs := flag.NewFlagSet("get-image", flag.ExitOnError)
	id := fs.String("id", "", "Image ID")
	region := fs.String("region", "", "Region name (optional)")
	family := fs.String("family", "", "Image family (optional)")
	name := fs.String("name", "", "Image name (optional)")
	arch := fs.String("arch", "", "Architecture filter (optional)")
	latest := fs.Bool("latest", false, "Return only the latest matching image")
	base, token := addCommonFlags(fs)
	tokenFile := fs.String("token-file", "", "Path to token file")
	_ = fs.Parse(args)

	if *id == "" && *family == "" && *name == "" {
		fatalf("one of --id, --family, or --name is required")
	}

	if *token == "" && *tokenFile == "" {
		if _, err := os.Stat("lambda-eve-karpenter.key"); err == nil {
			*tokenFile = "lambda-eve-karpenter.key"
		}
	}
	if *token == "" && *tokenFile != "" {
		data, err := os.ReadFile(*tokenFile)
		fatalIf(err)
		*token = strings.TrimSpace(string(data))
	}

	client := mustClientWith(*base, *token)
	ctx := context.Background()
	items, err := client.ListImages(ctx)
	fatalIf(err)

	var matches []lambdaclient.Image
	for _, img := range items {
		if *id != "" && img.ID != *id {
			continue
		}
		if *family != "" && img.Family != *family {
			continue
		}
		if *name != "" && img.Name != *name {
			continue
		}
		if *region != "" && img.Region.Name != *region {
			continue
		}
		if *arch != "" && img.Arch != *arch {
			continue
		}
		matches = append(matches, img)
	}

	if *latest && len(matches) > 0 {
		latestImg := matches[0]
		for _, img := range matches[1:] {
			if img.UpdatedTime.After(latestImg.UpdatedTime) {
				latestImg = img
			}
		}
		matches = []lambdaclient.Image{latestImg}
	}

	for _, img := range matches {
		fmt.Printf("%s\t%s\t%s\t%s\t%s\t%s\n", img.ID, img.Family, img.Name, img.Region.Name, img.Arch, img.UpdatedTime.Format(time.RFC3339))
	}
}

type stringSlice []string

func (s *stringSlice) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSlice) Set(val string) error {
	*s = append(*s, val)
	return nil
}

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

func launchInstance(args []string) {
	fs := flag.NewFlagSet("launch", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to YAML config")
	confirm := fs.Bool("confirm", false, "Confirm launch")
	name := fs.String("name", "", "Instance name")
	hostname := fs.String("hostname", "", "Hostname")
	region := fs.String("region", "", "Region name")
	instanceType := fs.String("instance-type", "", "Instance type name")
	userData := fs.String("user-data", "", "cloud-init user-data content")
	userDataFile := fs.String("user-data-file", "", "Path to cloud-init user-data")
	imageID := fs.String("image-id", "", "Image ID")
	imageFamily := fs.String("image-family", "", "Image family")
	var sshKeys stringSlice
	fs.Var(&sshKeys, "ssh-key", "SSH key name (repeatable)")
	var firewallIDs stringSlice
	fs.Var(&firewallIDs, "firewall-id", "Firewall ruleset ID (repeatable)")
	var tags stringSlice
	fs.Var(&tags, "tag", "Tag in key=value form (repeatable)")
	base, token := addCommonFlags(fs)
	tokenFile := fs.String("token-file", "", "Path to token file")
	_ = fs.Parse(args)

	if !*confirm {
		fatalf("launch requires --confirm")
	}

	cfg := LaunchConfig{
		Tags: map[string]string{},
	}
	if *configPath != "" {
		data, err := os.ReadFile(*configPath)
		fatalIf(err)
		fatalIf(yaml.Unmarshal(data, &cfg))
		if cfg.Tags == nil {
			cfg.Tags = map[string]string{}
		}
	}

	applyStringOverride(&cfg.Name, *name)
	applyStringOverride(&cfg.Hostname, *hostname)
	applyStringOverride(&cfg.Region, *region)
	applyStringOverride(&cfg.InstanceType, *instanceType)
	applyStringOverride(&cfg.UserData, *userData)
	applyStringOverride(&cfg.UserDataFile, *userDataFile)
	applyStringOverride(&cfg.ImageID, *imageID)
	applyStringOverride(&cfg.ImageFamily, *imageFamily)
	if len(sshKeys) > 0 {
		cfg.SSHKeyNames = append([]string(nil), sshKeys...)
	}
	if len(firewallIDs) > 0 {
		cfg.FirewallRuleIDs = append([]string(nil), firewallIDs...)
	}
	for _, kv := range tags {
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

	if *token == "" && *tokenFile == "" {
		if _, err := os.Stat("lambda-eve-karpenter.key"); err == nil {
			*tokenFile = "lambda-eve-karpenter.key"
		}
	}
	if *token == "" && *tokenFile != "" {
		data, err := os.ReadFile(*tokenFile)
		fatalIf(err)
		*token = strings.TrimSpace(string(data))
	}

	client := mustClientWith(*base, *token)
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
}

func terminateInstances(args []string) {
	fs := flag.NewFlagSet("terminate", flag.ExitOnError)
	id := fs.String("id", "", "Instance ID")
	confirm := fs.Bool("confirm", false, "Confirm termination")
	_ = fs.Parse(args)

	if !*confirm {
		fatalf("terminate requires --confirm")
	}
	if *id == "" {
		fatalf("--id is required")
	}

	fatalf("terminate is disabled by design in this build (requested safety guard)")
}

func mustClient(args []string) *lambdaclient.Client {
	fs := flag.NewFlagSet("common", flag.ExitOnError)
	base, token := addCommonFlags(fs)
	tokenFile := fs.String("token-file", "", "Path to token file")
	_ = fs.Parse(args)
	if *token == "" && *tokenFile == "" {
		if _, err := os.Stat("lambda-eve-karpenter.key"); err == nil {
			*tokenFile = "lambda-eve-karpenter.key"
		}
	}
	if *token == "" && *tokenFile != "" {
		data, err := os.ReadFile(*tokenFile)
		fatalIf(err)
		*token = strings.TrimSpace(string(data))
	}
	return mustClientWith(*base, *token)
}

func addCommonFlags(fs *flag.FlagSet) (*string, *string) {
	base := fs.String("base-url", getenvOr("LAMBDA_API_BASE_URL", defaultBaseURL), "Lambda API base URL")
	token := fs.String("token", getenvOr("LAMBDA_API_TOKEN", ""), "Lambda API token")
	return base, token
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

func getenvOr(key, def string) string {
	val := os.Getenv(key)
	if val == "" {
		return def
	}
	return val
}

func applyStringOverride(dst *string, val string) {
	if val != "" {
		*dst = val
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
