package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

// --- messages ---

type instancesLoadedMsg struct {
	items []lambdaclient.Instance
	err   error
}

type instanceDetailMsg struct {
	inst *lambdaclient.Instance
	err  error
}

type instanceTerminatedMsg struct {
	id  string
	err error
}

// --- tab ---

type instancesTab struct {
	baseTab
	instances []lambdaclient.Instance
	detail    *lambdaclient.Instance
	showDetail bool
}

func newInstancesTab() *instancesTab {
	t := &instancesTab{baseTab: newBaseTab()}
	t.table.SetColumns(instanceColumns(80))
	return t
}

func (t *instancesTab) Name() string { return "Instances" }

func (t *instancesTab) Init(client *lambdaclient.Client) tea.Cmd {
	t.client = client
	t.loading = true
	return t.fetchInstances
}

func (t *instancesTab) Refresh() tea.Cmd {
	if t.client == nil {
		return nil
	}
	return t.fetchInstances
}

func (t *instancesTab) fetchInstances() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	items, err := t.client.ListInstances(ctx)
	return instancesLoadedMsg{items: items, err: err}
}

func (t *instancesTab) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case instancesLoadedMsg:
		t.loading = false
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.instances = msg.items
			t.rebuildRows()
		}
		return nil, true

	case instanceDetailMsg:
		t.loading = false
		if msg.err != nil {
			t.err = msg.err
			return nil, true
		}
		t.detail = msg.inst
		t.showDetail = true
		return nil, true

	case instanceTerminatedMsg:
		if msg.err != nil {
			t.err = msg.err
		}
		// Refresh list after terminate.
		return t.fetchInstances, true

	case tea.KeyMsg:
		cmd := t.updateTable(msg)
		return cmd, false // let dashboard also handle global keys
	}

	return nil, false
}

func (t *instancesTab) View(width, height int) string {
	if t.err != nil && !t.loaded {
		return styleError.Render("Error: " + t.err.Error())
	}
	if !t.loaded {
		return loadingStyle.Render("Loading instances...")
	}
	if len(t.instances) == 0 {
		return styleMuted.Render("No instances.")
	}
	return t.viewWithFilter()
}

func (t *instancesTab) SetSize(width, height int) {
	t.baseTab.SetSize(width, height)
	t.table.SetColumns(instanceColumns(width))
}

func (t *instancesTab) HasDetail() bool   { return true }
func (t *instancesTab) SelectedID() string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	return row[0]
}

func (t *instancesTab) DetailView(width, height int) string {
	if t.detail == nil {
		return loadingStyle.Render("Loading...")
	}
	return renderInstanceDetail(t.detail, width)
}

// FetchDetail fetches detailed info for the selected instance.
func (t *instancesTab) FetchDetail() tea.Cmd {
	id := t.SelectedID()
	if id == "" {
		return nil
	}
	t.loading = true
	return func() tea.Msg {
		inst, err := t.client.GetInstance(context.Background(), id)
		return instanceDetailMsg{inst: inst, err: err}
	}
}

// Terminate returns a command to terminate the selected instance.
func (t *instancesTab) Terminate() tea.Cmd {
	id := t.SelectedID()
	if id == "" {
		return nil
	}
	return func() tea.Msg {
		err := t.client.TerminateInstance(context.Background(), id)
		return instanceTerminatedMsg{id: id, err: err}
	}
}

// SelectedInstance returns summary info for the selected row (for confirm dialog).
func (t *instancesTab) SelectedInstance() (id, typeName, region string) {
	row := t.table.SelectedRow()
	if row == nil {
		return "", "", ""
	}
	return row[0], row[3], row[4]
}

func (t *instancesTab) ClearDetail() {
	t.showDetail = false
	t.detail = nil
}

func (t *instancesTab) ShowingDetail() bool {
	return t.showDetail
}

// --- helpers ---

func instanceColumns(width int) []table.Column {
	// Allocate widths proportionally.
	// ID(12) Name(16) Status(12) Type(24) Region(14) IP(16) Tags(remainder)
	fixed := 12 + 16 + 12 + 24 + 14 + 16 + 6*2 // columns + padding
	tagW := width - fixed
	if tagW < 10 {
		tagW = 10
	}
	return []table.Column{
		{Title: "ID", Width: 12},
		{Title: "NAME", Width: 16},
		{Title: "STATUS", Width: 12},
		{Title: "TYPE", Width: 24},
		{Title: "REGION", Width: 14},
		{Title: "IP", Width: 16},
		{Title: "TAGS", Width: tagW},
	}
}

func (t *instancesTab) rebuildRows() {
	rows := make([]table.Row, 0, len(t.instances))
	for _, inst := range t.instances {
		rows = append(rows, table.Row{
			inst.ID, inst.Name, inst.Status,
			inst.Type.Name, inst.Region.Name, inst.IP,
			formatTags(inst.Tags),
		})
	}
	t.setAllRows(rows)
}

func renderInstanceDetail(inst *lambdaclient.Instance, width int) string {
	var b strings.Builder
	field := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s  %s\n",
				styleDetailLabel.Render(label),
				styleDetailValue.Render(value))
		}
	}

	statusLine := inst.Type.Name
	if inst.Status != "" {
		statusLine += " — " + inst.Status
	}

	field("Type", statusLine)
	field("ID", inst.ID)
	field("Name", inst.Name)
	field("Hostname", inst.Hostname)
	if inst.Region.Description != "" {
		field("Region", inst.Region.Name+" ("+inst.Region.Description+")")
	} else {
		field("Region", inst.Region.Name)
	}
	field("IP", inst.IP)
	field("Private IP", inst.PrivateIP)
	field("Description", inst.Type.Description)
	field("GPU", inst.Type.GPUDesc)
	if inst.Type.PriceCents > 0 {
		field("Price", fmt.Sprintf("$%.2f/hr", float64(inst.Type.PriceCents)/100.0))
	}
	specs := inst.Type.Specs
	if specs.VCPUs > 0 {
		field("Specs", fmt.Sprintf("%d vCPU, %d GiB RAM, %d GiB disk, %d GPU",
			specs.VCPUs, specs.MemoryGiB, specs.StorageGiB, specs.GPUs))
	}
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

	return lipgloss.NewStyle().MaxWidth(width).Render(b.String())
}
