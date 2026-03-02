package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

type typesLoadedMsg struct {
	items map[string]lambdaclient.InstanceTypesItem
	err   error
}

type typesTab struct {
	baseTab
	items map[string]lambdaclient.InstanceTypesItem
	names []string // sorted type names for row→data mapping
}

func newTypesTab() *typesTab {
	t := &typesTab{baseTab: newBaseTab()}
	t.table.SetColumns(typesColumns(80))
	return t
}

func (t *typesTab) Name() string { return "Instance Types" }

func (t *typesTab) Init(client *lambdaclient.Client) tea.Cmd {
	t.client = client
	t.loading = true
	return t.fetch
}

func (t *typesTab) Refresh() tea.Cmd {
	if t.client == nil {
		return nil
	}
	return t.fetch
}

func (t *typesTab) fetch() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	items, err := t.client.ListInstanceTypes(ctx)
	return typesLoadedMsg{items: items, err: err}
}

func (t *typesTab) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case typesLoadedMsg:
		t.loading = false
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.items = msg.items
			t.rebuildRows()
		}
		return nil, true

	case tea.KeyMsg:
		cmd := t.updateTable(msg)
		return cmd, false
	}
	return nil, false
}

func (t *typesTab) View(width, height int) string {
	if t.err != nil && !t.loaded {
		return styleError.Render("Error: " + t.err.Error())
	}
	if !t.loaded {
		return loadingStyle.Render("Loading instance types...")
	}
	if len(t.names) == 0 {
		return styleMuted.Render("No instance types available.")
	}
	return t.viewWithFilter()
}

func (t *typesTab) SetSize(width, height int) {
	t.baseTab.SetSize(width, height)
	t.table.SetColumns(typesColumns(width))
}

func (t *typesTab) HasDetail() bool { return true }

func (t *typesTab) DetailView(width, _ int) string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	name := row[0]
	item, ok := t.items[name]
	if !ok {
		return ""
	}
	return renderTypeDetail(name, item, width)
}

func renderTypeDetail(name string, item lambdaclient.InstanceTypesItem, width int) string {
	var b strings.Builder
	field := func(label, value string) {
		fmt.Fprintf(&b, "%s  %s\n",
			styleDetailLabel.Render(label),
			styleDetailValue.Render(value))
	}

	field("Name", name)
	if item.InstanceType.Description != "" {
		field("Description", item.InstanceType.Description)
	}
	field("GPU", item.InstanceType.GPUDesc)
	if item.InstanceType.PriceCents > 0 {
		field("Price", fmt.Sprintf("$%.2f/hr", float64(item.InstanceType.PriceCents)/100.0))
	}
	specs := item.InstanceType.Specs
	if specs.VCPUs > 0 {
		field("Specs", fmt.Sprintf("%d vCPU, %d GiB RAM, %d GiB disk, %d GPU",
			specs.VCPUs, specs.MemoryGiB, specs.StorageGiB, specs.GPUs))
	}

	if len(item.Regions) > 0 {
		b.WriteString("\n")
		b.WriteString(styleDetailLabel.Render("Regions:"))
		b.WriteString("\n")
		for _, r := range item.Regions {
			desc := r.Name
			if r.Description != "" {
				desc += " (" + r.Description + ")"
			}
			b.WriteString("  " + desc + "\n")
		}
	}

	return lipgloss.NewStyle().MaxWidth(width).Render(b.String())
}
func (t *typesTab) SelectedID() string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	return row[0]
}

func typesColumns(width int) []table.Column {
	// NAME(28) GPU(20) VCPU(6) RAM(10) DISK(10) GPUS(6) PRICE(10) REGIONS(remainder)
	fixed := 28 + 20 + 6 + 10 + 10 + 6 + 10 + 8*2
	regW := width - fixed
	if regW < 12 {
		regW = 12
	}
	return []table.Column{
		{Title: "NAME", Width: 28},
		{Title: "GPU", Width: 20},
		{Title: "VCPU", Width: 6},
		{Title: "RAM", Width: 10},
		{Title: "DISK", Width: 10},
		{Title: "GPUS", Width: 6},
		{Title: "PRICE", Width: 10},
		{Title: "REGIONS", Width: regW},
	}
}

func (t *typesTab) rebuildRows() {
	t.names = make([]string, 0, len(t.items))
	for name := range t.items {
		t.names = append(t.names, name)
	}
	sort.Strings(t.names)

	rows := make([]table.Row, 0, len(t.names))
	for _, name := range t.names {
		item := t.items[name]
		specs := item.InstanceType.Specs
		regions := typesRegionString(item)
		if regions == "" {
			regions = "-"
		}
		rows = append(rows, table.Row{
			name,
			item.InstanceType.GPUDesc,
			fmt.Sprintf("%d", specs.VCPUs),
			fmt.Sprintf("%d GiB", specs.MemoryGiB),
			fmt.Sprintf("%d GiB", specs.StorageGiB),
			fmt.Sprintf("%d", specs.GPUs),
			fmt.Sprintf("$%.2f/hr", float64(item.InstanceType.PriceCents)/100.0),
			regions,
		})
	}
	t.setAllRows(rows)
}

func typesRegionString(item lambdaclient.InstanceTypesItem) string {
	names := make([]string, 0, len(item.Regions))
	for _, r := range item.Regions {
		if r.Name != "" {
			names = append(names, r.Name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
