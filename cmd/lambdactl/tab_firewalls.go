package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

type firewallsLoadedMsg struct {
	items  []lambdaclient.FirewallRuleset
	global *lambdaclient.GlobalFirewallRuleset
	err    error
}

type firewallDetailMsg struct {
	rs  *lambdaclient.FirewallRuleset
	err error
}

type firewallDeletedMsg struct {
	id  string
	err error
}

type firewallsTab struct {
	baseTab
	rulesets   []lambdaclient.FirewallRuleset // includes converted global at index 0
	globalID   string                         // ID of the global ruleset (empty if not loaded)
	detail     *lambdaclient.FirewallRuleset
	showDetail bool
}

func newFirewallsTab() *firewallsTab {
	t := &firewallsTab{baseTab: newBaseTab()}
	t.table.SetColumns(fwColumns(80))
	return t
}

func (t *firewallsTab) Name() string { return "Firewalls" }

func (t *firewallsTab) Init(client *lambdaclient.Client) tea.Cmd {
	t.client = client
	t.loading = true
	return t.fetch
}

func (t *firewallsTab) Refresh() tea.Cmd {
	if t.client == nil {
		return nil
	}
	return t.fetch
}

func (t *firewallsTab) fetch() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	items, err := t.client.ListFirewallRulesets(ctx)
	if err != nil {
		return firewallsLoadedMsg{err: err}
	}
	global, gErr := t.client.GetGlobalFirewallRuleset(ctx)
	if gErr != nil {
		// Non-fatal — show regional rulesets without the global one.
		return firewallsLoadedMsg{items: items}
	}
	return firewallsLoadedMsg{items: items, global: global}
}

func (t *firewallsTab) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case firewallsLoadedMsg:
		t.loading = false
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.rulesets = nil
			t.globalID = ""

			// Prepend global ruleset (converted) if present.
			if msg.global != nil {
				t.globalID = msg.global.ID
				t.rulesets = append(t.rulesets, globalToRuleset(msg.global))
			}
			t.rulesets = append(t.rulesets, msg.items...)
			t.rebuildRows()
		}
		return nil, true

	case firewallDetailMsg:
		t.loading = false
		if msg.err != nil {
			t.err = msg.err
			return nil, true
		}
		t.detail = msg.rs
		t.showDetail = true
		return nil, true

	case firewallDeletedMsg:
		if msg.err != nil {
			t.err = msg.err
		}
		return t.fetch, true

	case tea.KeyMsg:
		cmd := t.updateTable(msg)
		return cmd, false
	}
	return nil, false
}

func (t *firewallsTab) View(width, height int) string {
	if t.err != nil && !t.loaded {
		return styleError.Render("Error: " + t.err.Error())
	}
	if !t.loaded {
		return loadingStyle.Render("Loading firewalls...")
	}
	if len(t.rulesets) == 0 {
		return styleMuted.Render("No firewall rulesets.")
	}
	return t.viewWithFilter()
}

func (t *firewallsTab) SetSize(width, height int) {
	t.baseTab.SetSize(width, height)
	t.table.SetColumns(fwColumns(width))
}

func (t *firewallsTab) HasDetail() bool { return true }
func (t *firewallsTab) HasCreate() bool { return true }
func (t *firewallsTab) SelectedID() string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	return row[0]
}

func (t *firewallsTab) SelectedName() string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	return row[1]
}

// IsGlobalSelected reports whether the currently selected row is the global ruleset.
func (t *firewallsTab) IsGlobalSelected() bool {
	return t.globalID != "" && t.SelectedID() == t.globalID
}

func (t *firewallsTab) DetailView(width, height int) string {
	if t.detail == nil {
		return loadingStyle.Render("Loading...")
	}
	return renderFirewallDetail(t.detail, width)
}

func (t *firewallsTab) FetchDetail() tea.Cmd {
	id := t.SelectedID()
	if id == "" {
		return nil
	}
	t.loading = true
	client := t.client
	isGlobal := t.IsGlobalSelected()
	return func() tea.Msg {
		ctx := context.Background()
		if isGlobal {
			grs, err := client.GetGlobalFirewallRuleset(ctx)
			if err != nil {
				return firewallDetailMsg{err: err}
			}
			rs := globalToRuleset(grs)
			return firewallDetailMsg{rs: &rs}
		}
		rs, err := client.GetFirewallRuleset(ctx, id)
		return firewallDetailMsg{rs: rs, err: err}
	}
}

func (t *firewallsTab) DeleteSelected() tea.Cmd {
	id := t.SelectedID()
	if id == "" || t.IsGlobalSelected() {
		return nil // can't delete the global ruleset
	}
	return func() tea.Msg {
		err := t.client.DeleteFirewallRuleset(context.Background(), id)
		return firewallDeletedMsg{id: id, err: err}
	}
}

func (t *firewallsTab) ClearDetail() {
	t.showDetail = false
	t.detail = nil
}

func (t *firewallsTab) ShowingDetail() bool {
	return t.showDetail
}

// --- helpers ---

// globalToRuleset converts a GlobalFirewallRuleset into a FirewallRuleset
// so it can be displayed uniformly in the table and detail view.
func globalToRuleset(g *lambdaclient.GlobalFirewallRuleset) lambdaclient.FirewallRuleset {
	return lambdaclient.FirewallRuleset{
		ID:    g.ID,
		Name:  g.Name,
		Region: lambdaclient.Region{Name: "(global)"},
		Rules: g.Rules,
	}
}

func fwColumns(width int) []table.Column {
	return []table.Column{
		{Title: "ID", Width: 40},
		{Title: "NAME", Width: 24},
		{Title: "REGION", Width: 14},
		{Title: "RULES", Width: 8},
		{Title: "INSTANCES", Width: 10},
	}
}

func (t *firewallsTab) rebuildRows() {
	rows := make([]table.Row, 0, len(t.rulesets))
	for _, rs := range t.rulesets {
		rows = append(rows, table.Row{
			rs.ID, rs.Name, rs.Region.Name,
			strconv.Itoa(len(rs.Rules)),
			strconv.Itoa(len(rs.InstanceIDs)),
		})
	}
	t.setAllRows(rows)
}

func renderFirewallDetail(rs *lambdaclient.FirewallRuleset, width int) string {
	var b strings.Builder
	field := func(label, value string) {
		fmt.Fprintf(&b, "%s  %s\n",
			styleDetailLabel.Render(label),
			styleDetailValue.Render(value))
	}

	field("ID", rs.ID)
	field("Name", rs.Name)
	field("Region", rs.Region.Name)
	if rs.Created != "" {
		field("Created", rs.Created)
	}
	if len(rs.InstanceIDs) > 0 {
		field("Instances", strings.Join(rs.InstanceIDs, ", "))
	}

	if len(rs.Rules) > 0 {
		b.WriteString("\n")
		b.WriteString(styleDetailLabel.Render("Rules:"))
		b.WriteString("\n")
		for _, r := range rs.Rules {
			ports := "-"
			if len(r.PortRange) == 2 {
				if r.PortRange[0] == r.PortRange[1] {
					ports = strconv.Itoa(r.PortRange[0])
				} else {
					ports = fmt.Sprintf("%d-%d", r.PortRange[0], r.PortRange[1])
				}
			}
			desc := r.Description
			if desc == "" {
				desc = "-"
			}
			fmt.Fprintf(&b, "  %s  %s  %s  %s\n",
				lipgloss.NewStyle().Width(8).Render(r.Protocol),
				lipgloss.NewStyle().Width(12).Render(ports),
				lipgloss.NewStyle().Width(18).Render(r.SourceNetwork),
				desc)
		}
	}

	return lipgloss.NewStyle().MaxWidth(width).Render(b.String())
}
