package main

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

// --- Firewall Ruleset Edit ---

type fwEditDataMsg struct {
	rs  *lambdaclient.FirewallRuleset
	err error
}

type fwUpdatedMsg struct {
	rs  *lambdaclient.FirewallRuleset
	err error
}

type firewallEditForm struct {
	client    *lambdaclient.Client
	id        string
	form      *huh.Form
	confirmed *bool
	name      string
	rulesText string
	loading   bool
}

func newFirewallEditForm(client *lambdaclient.Client, id string) *firewallEditForm {
	return &firewallEditForm{client: client, id: id, loading: true}
}

func (f *firewallEditForm) Init() tea.Cmd {
	client := f.client
	id := f.id
	return func() tea.Msg {
		rs, err := client.GetFirewallRuleset(context.Background(), id)
		return fwEditDataMsg{rs: rs, err: err}
	}
}

func (f *firewallEditForm) Update(msg tea.Msg) (tea.Cmd, bool) {
	if data, ok := msg.(fwEditDataMsg); ok {
		if data.err != nil {
			return func() tea.Msg { return fwUpdatedMsg{err: data.err} }, true
		}
		f.name = data.rs.Name
		f.rulesText = formatRuleText(data.rs.Rules)
		f.buildForm()
		f.loading = false
		return f.form.Init(), false
	}

	if f.form == nil {
		return nil, false
	}

	// Intercept esc to cancel.
	if km, ok := msg.(tea.KeyMsg); ok {
		if km.String() == "esc" {
			return nil, true
		}
	}

	model, cmd := f.form.Update(msg)
	f.form = model.(*huh.Form)

	switch f.form.State {
	case huh.StateCompleted:
		if f.confirmed == nil || !*f.confirmed {
			return nil, true
		}
		return f.submit(), false
	case huh.StateAborted:
		return nil, true
	}

	return cmd, false
}

func (f *firewallEditForm) buildForm() {
	confirmed := true
	f.confirmed = &confirmed

	f.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Name").
				Value(&f.name),

			huh.NewText().
				Title("Rules").
				Description("One per line: proto:ports:source[:desc]").
				Value(&f.rulesText),

			huh.NewConfirm().
				Title("Update firewall ruleset?").
				Affirmative("Update").
				Negative("Cancel").
				Value(&confirmed),
		),
	)
}

func (f *firewallEditForm) submit() tea.Cmd {
	client := f.client
	id := f.id
	name := strings.TrimSpace(f.name)
	rulesText := f.rulesText
	return func() tea.Msg {
		var namePtr *string
		if name != "" {
			namePtr = &name
		}
		var rules []lambdaclient.FirewallRule
		if strings.TrimSpace(rulesText) != "" {
			lines := splitNonEmpty(rulesText)
			var err error
			rules, err = parseRules(lines)
			if err != nil {
				return fwUpdatedMsg{err: err}
			}
		}
		rs, err := client.UpdateFirewallRuleset(context.Background(), id, namePtr, rules)
		return fwUpdatedMsg{rs: rs, err: err}
	}
}

func (f *firewallEditForm) View(width, height int) string {
	if f.loading {
		return loadingStyle.Render("Loading firewall ruleset...")
	}
	if f.form == nil {
		return ""
	}
	f.form.WithWidth(width).WithHeight(height)
	return f.form.View()
}

// --- Global Firewall Edit ---

type globalFWDataMsg struct {
	rs  *lambdaclient.GlobalFirewallRuleset
	err error
}

type globalFWUpdatedMsg struct {
	rs  *lambdaclient.GlobalFirewallRuleset
	err error
}

type globalFirewallEditForm struct {
	client    *lambdaclient.Client
	form      *huh.Form
	confirmed *bool
	rulesText string
	loading   bool
}

func newGlobalFirewallEditForm(client *lambdaclient.Client) *globalFirewallEditForm {
	return &globalFirewallEditForm{client: client, loading: true}
}

func (f *globalFirewallEditForm) Init() tea.Cmd {
	client := f.client
	return func() tea.Msg {
		rs, err := client.GetGlobalFirewallRuleset(context.Background())
		return globalFWDataMsg{rs: rs, err: err}
	}
}

func (f *globalFirewallEditForm) Update(msg tea.Msg) (tea.Cmd, bool) {
	if data, ok := msg.(globalFWDataMsg); ok {
		if data.err != nil {
			return func() tea.Msg { return globalFWUpdatedMsg{err: data.err} }, true
		}
		f.rulesText = formatRuleText(data.rs.Rules)
		f.buildForm()
		f.loading = false
		return f.form.Init(), false
	}

	if f.form == nil {
		return nil, false
	}

	// Intercept esc to cancel.
	if km, ok := msg.(tea.KeyMsg); ok {
		if km.String() == "esc" {
			return nil, true
		}
	}

	model, cmd := f.form.Update(msg)
	f.form = model.(*huh.Form)

	switch f.form.State {
	case huh.StateCompleted:
		if f.confirmed == nil || !*f.confirmed {
			return nil, true
		}
		return f.submit(), false
	case huh.StateAborted:
		return nil, true
	}

	return cmd, false
}

func (f *globalFirewallEditForm) buildForm() {
	confirmed := true
	f.confirmed = &confirmed

	f.form = huh.NewForm(
		huh.NewGroup(
			huh.NewText().
				Title("Global Firewall Rules").
				Description("One per line: proto:ports:source[:desc]").
				Value(&f.rulesText),

			huh.NewConfirm().
				Title("Update global firewall rules?").
				Affirmative("Update").
				Negative("Cancel").
				Value(&confirmed),
		),
	)
}

func (f *globalFirewallEditForm) submit() tea.Cmd {
	client := f.client
	rulesText := f.rulesText
	return func() tea.Msg {
		var rules []lambdaclient.FirewallRule
		if strings.TrimSpace(rulesText) != "" {
			lines := splitNonEmpty(rulesText)
			var err error
			rules, err = parseRules(lines)
			if err != nil {
				return globalFWUpdatedMsg{err: err}
			}
		}
		rs, err := client.UpdateGlobalFirewallRuleset(context.Background(), rules)
		return globalFWUpdatedMsg{rs: rs, err: err}
	}
}

func (f *globalFirewallEditForm) View(width, height int) string {
	if f.loading {
		return loadingStyle.Render("Loading global firewall rules...")
	}
	if f.form == nil {
		return ""
	}
	f.form.WithWidth(width).WithHeight(height)
	return f.form.View()
}

// openFirewallEdit opens the edit form for a firewall ruleset.
func (m *dashboardModel) openFirewallEdit(id string) tea.Cmd {
	f := newFirewallEditForm(m.client, id)
	m.create = f
	return f.Init()
}

// openGlobalFirewallEdit opens the edit form for global firewall rules.
func (m *dashboardModel) openGlobalFirewallEdit() tea.Cmd {
	f := newGlobalFirewallEditForm(m.client)
	m.create = f
	return f.Init()
}
