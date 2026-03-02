package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/alecthomas/kong"
	"github.com/joho/godotenv"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
	"github.com/lambdal/lambda-karpenter/internal/ratelimit"
	"golang.org/x/term"
)

// CLI is the top-level command tree.
type CLI struct {
	Dashboard    DashboardCmd    `cmd:"" default:"1" hidden:"" help:"Launch interactive TUI dashboard."`
	Instance     InstanceCmd     `cmd:"" help:"Instance management."`
	InstanceType InstanceTypeCmd `cmd:"" name:"instance-type" help:"Instance type queries."`
	Image        ImageCmd        `cmd:"" help:"Image management."`
	SSHKey       SSHKeyCmd       `cmd:"" name:"ssh-key" help:"SSH key management."`
	Filesystem   FilesystemCmd   `cmd:"" help:"Filesystem management."`
	Firewall     FirewallCmd     `cmd:"" help:"Firewall management."`
	K8s          K8sCmd          `cmd:"" name:"k8s" help:"Kubernetes cluster management."`
}

// DashboardCmd launches the TUI dashboard when no subcommand is given.
type DashboardCmd struct{}

func (c *DashboardCmd) Run() error {
	return runDashboard()
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
