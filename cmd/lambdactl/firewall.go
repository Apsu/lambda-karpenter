package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
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
	Region string `name:"region" help:"Filter by region name."`
}

func (c *FirewallListCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	rulesets, err := client.ListFirewallRulesets(ctx)
	fatalIf(err)

	var rows [][]string
	for _, rs := range rulesets {
		if c.Region != "" && !strings.EqualFold(rs.Region.Name, c.Region) {
			continue
		}
		rows = append(rows, []string{
			rs.ID, rs.Name, rs.Region.Name,
			strconv.Itoa(len(rs.Rules)), strconv.Itoa(len(rs.InstanceIDs)),
		})
	}
	printListTable([]string{"ID", "NAME", "REGION", "RULES", "INSTANCES"}, rows)
	return nil
}

type FirewallGetCmd struct {
	APIFlags
	ID string `arg:"" help:"Firewall ruleset ID."`
}

func (c *FirewallGetCmd) Run() error {
	client := c.mustClient()
	ctx := context.Background()
	rs, err := client.GetFirewallRuleset(ctx, c.ID)
	fatalIf(err)

	fields := [][]string{
		{"ID", rs.ID},
		{"Name", rs.Name},
		{"Region", rs.Region.Name},
		{"Created", rs.Created},
	}
	if len(rs.InstanceIDs) > 0 {
		fields = append(fields, []string{"Instances", strings.Join(rs.InstanceIDs, ", ")})
	}
	printDetailTable(fields)
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
	ID    string   `arg:"" help:"Firewall ruleset ID."`
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
	ID      string `arg:"" help:"Firewall ruleset ID."`
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
	printDetailTable([][]string{
		{"ID", rs.ID},
		{"Name", rs.Name},
	})
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
	var rows [][]string
	for _, r := range rules {
		ports := "-"
		if len(r.PortRange) == 2 {
			if r.PortRange[0] == r.PortRange[1] {
				ports = strconv.Itoa(r.PortRange[0])
			} else {
				ports = fmt.Sprintf("%d-%d", r.PortRange[0], r.PortRange[1])
			}
		}
		rows = append(rows, []string{r.Protocol, ports, r.SourceNetwork, r.Description})
	}
	printListTable([]string{"PROTOCOL", "PORTS", "SOURCE", "DESCRIPTION"}, rows)
}

// parseRule parses a single "proto:ports:source[:description]" string.
func parseRule(s string) (lambdaclient.FirewallRule, error) {
	parts := strings.SplitN(s, ":", 4)
	if len(parts) < 3 {
		return lambdaclient.FirewallRule{}, fmt.Errorf("invalid rule %q: expected proto:ports:source[:description]", s)
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
			return lambdaclient.FirewallRule{}, fmt.Errorf("invalid port in rule %q: %v", s, err)
		}
		high := low
		if len(portParts) == 2 {
			high, err = strconv.Atoi(portParts[1])
			if err != nil {
				return lambdaclient.FirewallRule{}, fmt.Errorf("invalid port range in rule %q: %v", s, err)
			}
		}
		rule.PortRange = []int{low, high}
	}
	return rule, nil
}

// parseRules parses multiple rule strings, returning all rules or the first error.
func parseRules(lines []string) ([]lambdaclient.FirewallRule, error) {
	var rules []lambdaclient.FirewallRule
	for _, line := range lines {
		r, err := parseRule(line)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// parseRuleFlags parses rule flags, calling fatalf on error (for CLI use).
func parseRuleFlags(flags []string) []lambdaclient.FirewallRule {
	rules, err := parseRules(flags)
	if err != nil {
		fatalf("%v", err)
	}
	return rules
}

// formatRuleText converts rules back to the text format for pre-population.
func formatRuleText(rules []lambdaclient.FirewallRule) string {
	var lines []string
	for _, r := range rules {
		ports := ""
		if len(r.PortRange) == 2 {
			if r.PortRange[0] == r.PortRange[1] {
				ports = strconv.Itoa(r.PortRange[0])
			} else {
				ports = fmt.Sprintf("%d-%d", r.PortRange[0], r.PortRange[1])
			}
		}
		line := r.Protocol + ":" + ports + ":" + r.SourceNetwork
		if r.Description != "" {
			line += ":" + r.Description
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}
