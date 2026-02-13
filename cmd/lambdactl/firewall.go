package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/evecallicoat/lambda-karpenter/internal/lambdaclient"
)

// FirewallCmd is the parent command for firewall management.
type FirewallCmd struct {
	List         FirewallListCmd         `cmd:"" help:"List firewall rulesets."`
	Get          FirewallGetCmd          `cmd:"" help:"Get firewall ruleset details."`
	Create       FirewallCreateCmd       `cmd:"" help:"Create a firewall ruleset."`
	Update       FirewallUpdateCmd       `cmd:"" help:"Update a firewall ruleset."`
	Delete       FirewallDeleteCmd       `cmd:"" help:"Delete a firewall ruleset."`
	Global       FirewallGlobalCmd       `cmd:"" help:"Get global firewall rules."`
	GlobalUpdate FirewallGlobalUpdateCmd `cmd:"" name:"global-update" help:"Update global firewall rules."`
}

type FirewallListCmd struct {
	APIFlags
}

func (c *FirewallListCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	rulesets, err := client.ListFirewallRulesets(ctx)
	fatalIf(err)

	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tREGION\tRULES\tINSTANCES")
	for _, rs := range rulesets {
		fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\n",
			rs.ID, rs.Name, rs.Region.Name, len(rs.Rules), len(rs.InstanceIDs))
	}
	w.Flush()
	return nil
}

type FirewallGetCmd struct {
	APIFlags
	ID string `name:"id" required:"" help:"Firewall ruleset ID."`
}

func (c *FirewallGetCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	rs, err := client.GetFirewallRuleset(ctx, c.ID)
	fatalIf(err)

	fmt.Printf("ID:        %s\n", rs.ID)
	fmt.Printf("Name:      %s\n", rs.Name)
	fmt.Printf("Region:    %s\n", rs.Region.Name)
	fmt.Printf("Created:   %s\n", rs.Created)
	if len(rs.InstanceIDs) > 0 {
		fmt.Printf("Instances: %s\n", strings.Join(rs.InstanceIDs, ", "))
	}
	fmt.Println()
	printRules(rs.Rules)
	return nil
}

type FirewallCreateCmd struct {
	APIFlags
	Name   string   `name:"name" required:"" help:"Ruleset name."`
	Region string   `name:"region" required:"" help:"Region."`
	Rules  []string `name:"rule" help:"Rule in proto:port-range:source:description form (repeatable)."`
}

func (c *FirewallCreateCmd) Run() error {
	rules := parseRuleFlags(c.Rules)
	client := c.mustClient()
	ctx := context.Background()
	rs, err := client.CreateFirewallRuleset(ctx, c.Name, c.Region, rules)
	fatalIf(err)
	fmt.Printf("created ruleset %s (%s) with %d rules\n", rs.ID, rs.Name, len(rs.Rules))
	return nil
}

type FirewallUpdateCmd struct {
	APIFlags
	ID    string   `name:"id" required:"" help:"Firewall ruleset ID."`
	Name  string   `name:"name" help:"New ruleset name."`
	Rules []string `name:"rule" help:"Rule in proto:port-range:source:description form (repeatable). Replaces all rules."`
}

func (c *FirewallUpdateCmd) Run() error {
	var namePtr *string
	if c.Name != "" {
		namePtr = &c.Name
	}
	var rules []lambdaclient.FirewallRule
	if len(c.Rules) > 0 {
		rules = parseRuleFlags(c.Rules)
	}

	client := c.mustClient()
	ctx := context.Background()
	rs, err := client.UpdateFirewallRuleset(ctx, c.ID, namePtr, rules)
	fatalIf(err)
	fmt.Printf("updated ruleset %s (%s) — %d rules\n", rs.ID, rs.Name, len(rs.Rules))
	return nil
}

type FirewallDeleteCmd struct {
	APIFlags
	ID      string `name:"id" required:"" help:"Firewall ruleset ID."`
	Confirm bool   `name:"confirm" help:"Skip interactive confirmation."`
}

func (c *FirewallDeleteCmd) Run() error {
	if !c.Confirm {
		confirmAction("delete firewall ruleset " + c.ID)
	}
	client := c.mustClient()
	ctx := context.Background()
	fatalIf(client.DeleteFirewallRuleset(ctx, c.ID))
	fmt.Printf("deleted firewall ruleset %s\n", c.ID)
	return nil
}

type FirewallGlobalCmd struct {
	APIFlags
}

func (c *FirewallGlobalCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	rs, err := client.GetGlobalFirewallRuleset(ctx)
	fatalIf(err)
	fmt.Printf("ID:   %s\n", rs.ID)
	fmt.Printf("Name: %s\n", rs.Name)
	fmt.Println()
	printRules(rs.Rules)
	return nil
}

type FirewallGlobalUpdateCmd struct {
	APIFlags
	Rules []string `name:"rule" required:"" help:"Rule in proto:port-range:source:description form (repeatable). Replaces all global rules."`
}

func (c *FirewallGlobalUpdateCmd) Run() error {
	rules := parseRuleFlags(c.Rules)
	client := c.mustClient()
	ctx := context.Background()
	rs, err := client.UpdateGlobalFirewallRuleset(ctx, rules)
	fatalIf(err)
	fmt.Printf("updated global firewall rules — %d rules\n", len(rs.Rules))
	return nil
}

// --- helpers ---

// printRules prints a table of firewall rules.
func printRules(rules []lambdaclient.FirewallRule) {
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "PROTOCOL\tPORTS\tSOURCE\tDESCRIPTION")
	for _, r := range rules {
		ports := "-"
		if len(r.PortRange) == 2 {
			if r.PortRange[0] == r.PortRange[1] {
				ports = strconv.Itoa(r.PortRange[0])
			} else {
				ports = fmt.Sprintf("%d-%d", r.PortRange[0], r.PortRange[1])
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", r.Protocol, ports, r.SourceNetwork, r.Description)
	}
	w.Flush()
}

// parseRuleFlags parses rule flags in "proto:ports:source:description" format.
// Examples:
//
//	tcp:22:0.0.0.0/0:SSH
//	tcp:6443:0.0.0.0/0:K8s API
//	icmp::0.0.0.0/0:Ping
//	all::10.0.0.0/8:Internal
func parseRuleFlags(flags []string) []lambdaclient.FirewallRule {
	var rules []lambdaclient.FirewallRule
	for _, f := range flags {
		parts := strings.SplitN(f, ":", 4)
		if len(parts) < 3 {
			fatalf("invalid rule %q: expected proto:ports:source[:description]", f)
		}
		rule := lambdaclient.FirewallRule{
			Protocol:      parts[0],
			SourceNetwork: parts[2],
		}
		if len(parts) == 4 {
			rule.Description = parts[3]
		}
		if parts[1] != "" {
			portParts := strings.SplitN(parts[1], "-", 2)
			low, err := strconv.Atoi(portParts[0])
			if err != nil {
				fatalf("invalid port in rule %q: %v", f, err)
			}
			high := low
			if len(portParts) == 2 {
				high, err = strconv.Atoi(portParts[1])
				if err != nil {
					fatalf("invalid port range in rule %q: %v", f, err)
				}
			}
			rule.PortRange = []int{low, high}
		}
		rules = append(rules, rule)
	}
	return rules
}
