package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	ltable "github.com/charmbracelet/lipgloss/table"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

// InstanceTypeCmd is the parent command for instance type queries.
type InstanceTypeCmd struct {
	List InstanceTypeListCmd `cmd:"" help:"List available instance types."`
}

type InstanceTypeListCmd struct {
	APIFlags
	Region    string        `name:"region" help:"Filter to types available in this region."`
	MinGPUs   int           `name:"min-gpus" default:"0" help:"Minimum GPU count."`
	MinVCPUs  int           `name:"min-vcpus" default:"0" help:"Minimum vCPU count."`
	MinMemory int           `name:"min-memory" default:"0" help:"Minimum memory in GiB."`
	Watch     bool          `name:"watch" short:"w" help:"Watch for availability changes."`
	Interval  time.Duration `name:"interval" default:"30s" help:"Poll interval for --watch."`
}

func (c *InstanceTypeListCmd) Run() error {
	client := c.mustClient()

	if !c.Watch {
		items, err := client.ListInstanceTypes(context.Background())
		fatalIf(err)
		fmt.Println(c.renderTable(items, nil, termWidth()))
		return nil
	}

	m := newWatchModel(client, c)
	p := tea.NewProgram(m)
	_, err := p.Run()
	return err
}

// --- bubbletea watch model ---

type watchModel struct {
	client   *lambdaclient.Client
	cmd      *InstanceTypeListCmd
	spinner  spinner.Model
	items    map[string]lambdaclient.InstanceTypesItem
	prev     map[string]string // name → sorted regions
	diff     map[string]diffKind
	status   string
	lastPoll time.Time
	err      error
	interval time.Duration
	width    int
	quitting bool
}

type pollResultMsg struct {
	items map[string]lambdaclient.InstanceTypesItem
	err   error
}

type tickMsg struct{}

func newWatchModel(client *lambdaclient.Client, cmd *InstanceTypeListCmd) watchModel {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	s.Style = styleDim
	return watchModel{
		client:   client,
		cmd:      cmd,
		spinner:  s,
		interval: cmd.Interval,
		width:    120,
		status:   "connecting...",
	}
}

func (m watchModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.doPoll)
}

func (m watchModel) doPoll() tea.Msg {
	items, err := m.client.ListInstanceTypes(context.Background())
	return pollResultMsg{items: items, err: err}
}

func (m watchModel) doTick() tea.Cmd {
	return tea.Tick(m.interval, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case tickMsg:
		return m, m.doPoll

	case pollResultMsg:
		m.lastPoll = time.Now()
		if msg.err != nil {
			m.err = msg.err
			m.status = styleRemoved.Render("error: " + msg.err.Error())
			return m, m.doTick()
		}
		m.err = nil
		m.items = msg.items

		cur := m.cmd.snapshot(msg.items)
		if m.prev == nil {
			m.status = styleDim.Render(fmt.Sprintf("watching every %s (q to quit)", m.interval))
			m.prev = cur
			return m, m.doTick()
		}

		added, removed, changed := diffSnapshots(m.prev, cur)
		if len(added)+len(removed)+len(changed) == 0 {
			return m, m.doTick()
		}

		m.diff = makeDiffSet(m.prev, cur)
		var parts []string
		for _, name := range added {
			parts = append(parts, styleAdded.Render("+"+name))
		}
		for _, name := range removed {
			parts = append(parts, styleRemoved.Render("-"+name))
		}
		for _, name := range changed {
			parts = append(parts, styleChanged.Render("~"+name))
		}
		m.status = strings.Join(parts, " ")
		m.prev = cur
		return m, m.doTick()
	}
	return m, nil
}

func (m watchModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	ts := ""
	if !m.lastPoll.IsZero() {
		ts = styleDim.Render(m.lastPoll.Format("15:04:05")) + " "
	}
	fmt.Fprintf(&b, "%s %s%s\n", m.spinner.View(), ts, m.status)

	if m.items != nil {
		b.WriteString(m.cmd.renderTable(m.items, m.diff, m.width))
	}

	return b.String()
}

// --- diff helpers ---

type diffKind int

const (
	diffAdded diffKind = iota
	diffRemoved
	diffChanged
)

func makeDiffSet(prev, cur map[string]string) map[string]diffKind {
	diff := make(map[string]diffKind)
	for name := range cur {
		if _, ok := prev[name]; !ok {
			diff[name] = diffAdded
		} else if prev[name] != cur[name] {
			diff[name] = diffChanged
		}
	}
	for name := range prev {
		if _, ok := cur[name]; !ok {
			diff[name] = diffRemoved
		}
	}
	return diff
}

func (c *InstanceTypeListCmd) snapshot(items map[string]lambdaclient.InstanceTypesItem) map[string]string {
	snap := make(map[string]string)
	for name, item := range items {
		if !c.matchFilters(item) {
			continue
		}
		regions := regionString(item)
		if regions != "" {
			snap[name] = regions
		}
	}
	return snap
}

func diffSnapshots(prev, cur map[string]string) (added, removed, changed []string) {
	for name, regions := range cur {
		old, ok := prev[name]
		if !ok {
			added = append(added, name)
		} else if old != regions {
			changed = append(changed, name)
		}
	}
	for name := range prev {
		if _, ok := cur[name]; !ok {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	return
}

// --- table rendering ---

func (c *InstanceTypeListCmd) matchFilters(item lambdaclient.InstanceTypesItem) bool {
	specs := item.InstanceType.Specs
	if c.MinGPUs > 0 && specs.GPUs < c.MinGPUs {
		return false
	}
	if c.MinVCPUs > 0 && specs.VCPUs < c.MinVCPUs {
		return false
	}
	if c.MinMemory > 0 && specs.MemoryGiB < c.MinMemory {
		return false
	}
	if c.Region != "" {
		found := false
		for _, r := range item.Regions {
			if strings.EqualFold(r.Name, c.Region) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func regionString(item lambdaclient.InstanceTypesItem) string {
	names := make([]string, 0, len(item.Regions))
	for _, r := range item.Regions {
		if r.Name != "" {
			names = append(names, r.Name)
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

func (c *InstanceTypeListCmd) renderTable(items map[string]lambdaclient.InstanceTypesItem, diff map[string]diffKind, width int) string {
	var names []string
	for name, item := range items {
		if c.matchFilters(item) {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	if len(names) == 0 {
		return styleDim.Render("no matching instance types")
	}

	var rows [][]string
	for _, name := range names {
		item := items[name]
		specs := item.InstanceType.Specs
		regions := regionString(item)
		if regions == "" {
			regions = "-"
		}
		rows = append(rows, []string{
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

	t := ltable.New().
		Headers("NAME", "GPU", "VCPU", "RAM", "DISK", "GPUS", "PRICE", "REGIONS").
		Rows(rows...).
		Width(width).
		Wrap(false).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderHeader(false).
		StyleFunc(func(row, col int) lipgloss.Style {
			if row == ltable.HeaderRow {
				return styleHeader
			}
			if diff != nil && row >= 0 && row < len(names) {
				if kind, ok := diff[names[row]]; ok {
					switch kind {
					case diffAdded:
						return styleAdded
					case diffChanged:
						return styleChanged
					}
				}
			}
			return lipgloss.NewStyle()
		})

	return t.Render()
}
