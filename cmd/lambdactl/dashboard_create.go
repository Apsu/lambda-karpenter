package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

// --- SSH Key Create ---

type sshKeyCreatedMsg struct {
	key *lambdaclient.GeneratedSSHKey
	err error
}

type sshKeyCreateForm struct {
	client    *lambdaclient.Client
	form      *huh.Form
	confirmed *bool
	name      string
	publicKey string
}

func newSSHKeyCreateForm(client *lambdaclient.Client) *sshKeyCreateForm {
	f := &sshKeyCreateForm{client: client}

	confirmed := true
	f.confirmed = &confirmed

	f.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Name").
				Description("SSH key name").
				Value(&f.name).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("name is required")
					}
					return nil
				}),

			huh.NewText().
				Title("Public Key").
				Description("Leave blank to generate a key pair").
				Value(&f.publicKey),

			huh.NewConfirm().
				Title("Create SSH key?").
				Affirmative("Create").
				Negative("Cancel").
				Value(&confirmed),
		),
	)

	return f
}

func (f *sshKeyCreateForm) Init() tea.Cmd {
	return f.form.Init()
}

func (f *sshKeyCreateForm) Update(msg tea.Msg) (tea.Cmd, bool) {
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

func (f *sshKeyCreateForm) submit() tea.Cmd {
	client := f.client
	name := strings.TrimSpace(f.name)
	pubKey := strings.TrimSpace(f.publicKey)
	return func() tea.Msg {
		key, err := client.AddSSHKey(context.Background(), name, pubKey)
		return sshKeyCreatedMsg{key: key, err: err}
	}
}

func (f *sshKeyCreateForm) View(width, height int) string {
	if f.form == nil {
		return loadingStyle.Render("Loading...")
	}
	f.form.WithWidth(width).WithHeight(height)
	return f.form.View()
}

// --- Filesystem Create ---

type fsFormDataMsg struct {
	regions []string
	err     error
}

type fsCreatedMsg struct {
	fs  *lambdaclient.Filesystem
	err error
}

type filesystemCreateForm struct {
	client    *lambdaclient.Client
	form      *huh.Form
	confirmed *bool
	name      string
	region    string
	loading   bool
}

func newFilesystemCreateForm(client *lambdaclient.Client) *filesystemCreateForm {
	return &filesystemCreateForm{client: client, loading: true}
}

func (f *filesystemCreateForm) Init() tea.Cmd {
	client := f.client
	return func() tea.Msg {
		ctx := context.Background()
		types, err := client.ListInstanceTypes(ctx)
		if err != nil {
			return fsFormDataMsg{err: err}
		}
		regionSet := make(map[string]bool)
		for _, item := range types {
			for _, r := range item.Regions {
				if r.Name != "" {
					regionSet[r.Name] = true
				}
			}
		}
		regions := make([]string, 0, len(regionSet))
		for r := range regionSet {
			regions = append(regions, r)
		}
		sort.Strings(regions)
		return fsFormDataMsg{regions: regions}
	}
}

func (f *filesystemCreateForm) Update(msg tea.Msg) (tea.Cmd, bool) {
	if data, ok := msg.(fsFormDataMsg); ok {
		if data.err != nil {
			return func() tea.Msg { return fsCreatedMsg{err: data.err} }, true
		}
		f.buildForm(data.regions)
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

func (f *filesystemCreateForm) buildForm(regions []string) {
	confirmed := true
	f.confirmed = &confirmed

	opts := make([]huh.Option[string], len(regions))
	for i, r := range regions {
		opts[i] = huh.NewOption(r, r)
	}

	f.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Name").
				Description("Filesystem name").
				Value(&f.name).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("name is required")
					}
					return nil
				}),

			huh.NewSelect[string]().
				Title("Region").
				Options(opts...).
				Value(&f.region),

			huh.NewConfirm().
				Title("Create filesystem?").
				Affirmative("Create").
				Negative("Cancel").
				Value(&confirmed),
		),
	)
}

func (f *filesystemCreateForm) submit() tea.Cmd {
	client := f.client
	name := strings.TrimSpace(f.name)
	region := f.region
	return func() tea.Msg {
		fs, err := client.CreateFilesystem(context.Background(), name, region)
		return fsCreatedMsg{fs: fs, err: err}
	}
}

func (f *filesystemCreateForm) View(width, height int) string {
	if f.loading {
		return loadingStyle.Render("Loading regions...")
	}
	if f.form == nil {
		return ""
	}
	f.form.WithWidth(width).WithHeight(height)
	return f.form.View()
}

// --- Firewall Create ---

type fwFormDataMsg struct {
	regions []string
	err     error
}

type fwCreatedMsg struct {
	rs  *lambdaclient.FirewallRuleset
	err error
}

type firewallCreateForm struct {
	client    *lambdaclient.Client
	form      *huh.Form
	confirmed *bool
	name      string
	region    string
	rulesText string
	loading   bool
}

func newFirewallCreateForm(client *lambdaclient.Client) *firewallCreateForm {
	return &firewallCreateForm{client: client, loading: true}
}

func (f *firewallCreateForm) Init() tea.Cmd {
	client := f.client
	return func() tea.Msg {
		ctx := context.Background()
		types, err := client.ListInstanceTypes(ctx)
		if err != nil {
			return fwFormDataMsg{err: err}
		}
		regionSet := make(map[string]bool)
		for _, item := range types {
			for _, r := range item.Regions {
				if r.Name != "" {
					regionSet[r.Name] = true
				}
			}
		}
		regions := make([]string, 0, len(regionSet))
		for r := range regionSet {
			regions = append(regions, r)
		}
		sort.Strings(regions)
		return fwFormDataMsg{regions: regions}
	}
}

func (f *firewallCreateForm) Update(msg tea.Msg) (tea.Cmd, bool) {
	if data, ok := msg.(fwFormDataMsg); ok {
		if data.err != nil {
			return func() tea.Msg { return fwCreatedMsg{err: data.err} }, true
		}
		f.buildForm(data.regions)
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

func (f *firewallCreateForm) buildForm(regions []string) {
	confirmed := true
	f.confirmed = &confirmed

	opts := make([]huh.Option[string], len(regions))
	for i, r := range regions {
		opts[i] = huh.NewOption(r, r)
	}

	f.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Name").
				Description("Ruleset name").
				Value(&f.name).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("name is required")
					}
					return nil
				}),

			huh.NewSelect[string]().
				Title("Region").
				Options(opts...).
				Value(&f.region),

			huh.NewText().
				Title("Rules").
				Description("One per line: proto:ports:source[:desc]\ne.g. tcp:22:0.0.0.0/0:SSH access").
				Value(&f.rulesText),

			huh.NewConfirm().
				Title("Create firewall ruleset?").
				Affirmative("Create").
				Negative("Cancel").
				Value(&confirmed),
		),
	)
}

func (f *firewallCreateForm) submit() tea.Cmd {
	client := f.client
	name := strings.TrimSpace(f.name)
	region := f.region
	rulesText := f.rulesText
	return func() tea.Msg {
		var rules []lambdaclient.FirewallRule
		if strings.TrimSpace(rulesText) != "" {
			lines := splitNonEmpty(rulesText)
			var err error
			rules, err = parseRules(lines)
			if err != nil {
				return fwCreatedMsg{err: err}
			}
		}
		rs, err := client.CreateFirewallRuleset(context.Background(), name, region, rules)
		return fwCreatedMsg{rs: rs, err: err}
	}
}

func (f *firewallCreateForm) View(width, height int) string {
	if f.loading {
		return loadingStyle.Render("Loading regions...")
	}
	if f.form == nil {
		return ""
	}
	f.form.WithWidth(width).WithHeight(height)
	return f.form.View()
}

// splitNonEmpty splits text by newlines and returns non-empty trimmed lines.
func splitNonEmpty(s string) []string {
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}
