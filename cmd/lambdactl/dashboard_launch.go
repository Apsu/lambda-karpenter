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

// launchFormModel wraps a huh.Form for launching an instance.
type launchFormModel struct {
	form      *huh.Form
	result    *launchResult // set on submit
	confirmed *bool         // bound to the Confirm field
	err       error
}

type launchResult struct {
	name            string
	region          string
	instanceType    string
	sshKeys         []string
	imageFamily     string
	userData        string
	firewallIDs     []string
	filesystemNames []string
}

// launchDataMsg carries data fetched in the background for the launch form.
type launchDataMsg struct {
	types       map[string]lambdaclient.InstanceTypesItem
	keys        []lambdaclient.SSHKey
	images      []lambdaclient.Image
	firewalls   []lambdaclient.FirewallRuleset
	filesystems []lambdaclient.Filesystem
	err         error
}

type launchDoneMsg struct {
	ids []string
	err error
}

// openLaunchForm starts fetching data needed for the form.
func (m *dashboardModel) openLaunchForm() tea.Cmd {
	client := m.client
	return func() tea.Msg {
		ctx := context.Background()
		types, typErr := client.ListInstanceTypes(ctx)
		keys, keyErr := client.ListSSHKeys(ctx)
		images, imgErr := client.ListImages(ctx)
		firewalls, fwErr := client.ListFirewallRulesets(ctx)
		filesystems, fsErr := client.ListFilesystems(ctx)

		// Return first error.
		for _, e := range []error{typErr, keyErr, imgErr, fwErr, fsErr} {
			if e != nil {
				return launchDataMsg{err: e}
			}
		}
		return launchDataMsg{
			types: types, keys: keys, images: images,
			firewalls: firewalls, filesystems: filesystems,
		}
	}
}

func (m *dashboardModel) doLaunch(r launchResult) tea.Cmd {
	client := m.client
	return func() tea.Msg {
		req := lambdaclient.LaunchRequest{
			Name:             r.name,
			RegionName:       r.region,
			InstanceTypeName: r.instanceType,
			SSHKeyNames:      r.sshKeys,
			UserData:         r.userData,
		}
		for _, name := range r.filesystemNames {
			if name != "" {
				req.FileSystemNames = append(req.FileSystemNames, name)
			}
		}
		if r.imageFamily != "" {
			req.Image = &lambdaclient.ImageSpec{Family: r.imageFamily}
		}
		for _, fwID := range r.firewallIDs {
			if fwID != "" {
				req.FirewallRulesets = append(req.FirewallRulesets, lambdaclient.FirewallRulesetEntry{ID: fwID})
			}
		}
		ids, err := client.LaunchInstance(context.Background(), req)
		return launchDoneMsg{ids: ids, err: err}
	}
}

// buildLaunchForm builds the huh.Form from fetched data.
func buildLaunchForm(data launchDataMsg) *launchFormModel {
	lm := &launchFormModel{}

	// Build region options from instance types.
	regionSet := make(map[string]bool)
	for _, item := range data.types {
		for _, r := range item.Regions {
			if r.Name != "" {
				regionSet[r.Name] = true
			}
		}
	}
	regionNames := make([]string, 0, len(regionSet))
	for r := range regionSet {
		regionNames = append(regionNames, r)
	}
	sort.Strings(regionNames)

	regionOpts := make([]huh.Option[string], len(regionNames))
	for i, r := range regionNames {
		regionOpts[i] = huh.NewOption(r, r)
	}

	var result launchResult

	// Instance type select uses OptionsFunc bound to &result.region so it
	// re-evaluates whenever the selected region changes.
	typeSelectFunc := func() []huh.Option[string] {
		var names []string
		for name, item := range data.types {
			if result.region == "" {
				// No region selected yet — show all types.
				names = append(names, name)
				continue
			}
			for _, r := range item.Regions {
				if r.Name == result.region {
					names = append(names, name)
					break
				}
			}
		}
		sort.Strings(names)

		opts := make([]huh.Option[string], len(names))
		for i, name := range names {
			item := data.types[name]
			price := fmt.Sprintf("$%.2f/hr", float64(item.InstanceType.PriceCents)/100.0)
			label := name + " — " + item.InstanceType.GPUDesc + " — " + price
			opts[i] = huh.NewOption(label, name)
		}
		return opts
	}

	// SSH key options.
	keyOpts := make([]huh.Option[string], len(data.keys))
	for i, k := range data.keys {
		keyOpts[i] = huh.NewOption(k.Name, k.Name)
	}

	// Image family options (deduplicated).
	familySet := make(map[string]bool)
	for _, img := range data.images {
		if img.Family != "" {
			familySet[img.Family] = true
		}
	}
	familyNames := make([]string, 0, len(familySet)+1)
	familyNames = append(familyNames, "(default)")
	for f := range familySet {
		familyNames = append(familyNames, f)
	}
	sort.Strings(familyNames[1:])

	familyOpts := make([]huh.Option[string], len(familyNames))
	for i, f := range familyNames {
		val := f
		if f == "(default)" {
			val = ""
		}
		familyOpts[i] = huh.NewOption(f, val)
	}

	// Firewall options (region-filtered via OptionsFunc).
	fwSelectFunc := func() []huh.Option[string] {
		var opts []huh.Option[string]
		for _, fw := range data.firewalls {
			if result.region == "" || fw.Region.Name == result.region {
				opts = append(opts, huh.NewOption(fw.Name+" ("+fw.Region.Name+")", fw.ID))
			}
		}
		return opts
	}

	// Filesystem options (region-filtered via OptionsFunc).
	fsSelectFunc := func() []huh.Option[string] {
		var opts []huh.Option[string]
		for _, fs := range data.filesystems {
			if result.region == "" || fs.Region.Name == result.region {
				opts = append(opts, huh.NewOption(fs.Name+" ("+fs.Region.Name+")", fs.Name))
			}
		}
		return opts
	}

	// Bind a bool to the Confirm field so we can check its value.
	confirmed := true
	lm.confirmed = &confirmed

	lm.form = huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Instance Name").
				Description("Optional name for the instance").
				Value(&result.name),

			huh.NewSelect[string]().
				Title("Region").
				Options(regionOpts...).
				Value(&result.region),

			huh.NewSelect[string]().
				Title("Instance Type").
				OptionsFunc(typeSelectFunc, &result.region).
				Value(&result.instanceType),
		),
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("SSH Keys").
				Description("Select one or more SSH keys").
				Options(keyOpts...).
				Value(&result.sshKeys),

			huh.NewSelect[string]().
				Title("Image Family").
				Options(familyOpts...).
				Value(&result.imageFamily),
		),
		huh.NewGroup(
			huh.NewText().
				Title("User Data (cloud-init)").
				Description("Optional cloud-init user data script").
				Value(&result.userData),

			huh.NewMultiSelect[string]().
				Title("Firewall Rulesets").
				Description("Select firewalls to attach").
				OptionsFunc(fwSelectFunc, &result.region).
				Value(&result.firewallIDs),

			huh.NewMultiSelect[string]().
				Title("Filesystems").
				Description("Select filesystems to attach").
				OptionsFunc(fsSelectFunc, &result.region).
				Value(&result.filesystemNames),

			huh.NewConfirm().
				Title("Launch this instance?").
				Affirmative("Launch").
				Negative("Cancel").
				Value(&confirmed),
		),
	)

	lm.result = &result

	return lm
}

func (lm *launchFormModel) Update(msg tea.Msg) (tea.Cmd, bool) {
	// Handle the data-fetch message to build the form.
	if data, ok := msg.(launchDataMsg); ok {
		if data.err != nil {
			lm.err = data.err
			return nil, true // done with error
		}
		built := buildLaunchForm(data)
		*lm = *built
		return lm.form.Init(), false
	}

	if lm.form == nil {
		return nil, false
	}

	// Intercept esc to cancel the form — huh only aborts on ctrl+c.
	if msg, ok := msg.(tea.KeyMsg); ok {
		if msg.String() == "esc" {
			lm.result = nil
			return nil, true
		}
	}

	model, cmd := lm.form.Update(msg)
	lm.form = model.(*huh.Form)

	switch lm.form.State {
	case huh.StateCompleted:
		// Only launch if the user confirmed (not cancelled).
		if lm.confirmed == nil || !*lm.confirmed {
			lm.result = nil
		}
		return nil, true
	case huh.StateAborted:
		lm.result = nil
		return nil, true
	}

	return cmd, false
}

// View returns the inner content for the overlay.
func (lm *launchFormModel) View(width, height int) string {
	if lm.err != nil {
		return styleError.Render("Error: " + lm.err.Error())
	}
	if lm.form == nil {
		return loadingStyle.Render("Loading launch form...")
	}
	return lm.form.View()
}

// handleLaunchData is called from the dashboard update when we receive launch data.
func (m *dashboardModel) handleLaunchData(msg launchDataMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.statusMsg = styleError.Render("Launch: " + msg.err.Error())
		m.launch = nil
		return m, nil
	}
	built := buildLaunchForm(msg)
	// Size the form to fit the overlay's inner dimensions.
	frameW, frameH := overlayStyle.GetFrameSize()
	innerW := m.overlayW - frameW
	innerH := m.overlayH - frameH
	if innerW > 0 {
		built.form.WithWidth(innerW)
	}
	if innerH > 0 {
		built.form.WithHeight(innerH)
	}
	m.launch = built
	return m, m.launch.form.Init()
}

// handleLaunchDone processes the result of a launch.
func (m *dashboardModel) handleLaunchDone(msg launchDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		if lambdaclient.IsCapacityError(msg.err) {
			m.statusMsg = styleWarn.Render("No capacity — " + msg.err.Error())
		} else {
			m.statusMsg = styleError.Render("Launch failed: " + msg.err.Error())
		}
		return m, nil
	}
	m.statusMsg = styleSuccess.Render("Launched: " + strings.Join(msg.ids, ", "))
	// Refresh instances tab.
	if len(m.tabs) > 0 {
		return m, m.tabs[0].Refresh()
	}
	return m, nil
}
