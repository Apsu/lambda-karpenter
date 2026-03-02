package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

type filesystemsLoadedMsg struct {
	items []lambdaclient.Filesystem
	err   error
}

type filesystemDeletedMsg struct {
	id  string
	err error
}

type filesystemsTab struct {
	baseTab
	filesystems []lambdaclient.Filesystem
}

func newFilesystemsTab() *filesystemsTab {
	t := &filesystemsTab{baseTab: newBaseTab()}
	t.table.SetColumns(fsColumns(80))
	return t
}

func (t *filesystemsTab) Name() string { return "Filesystems" }

func (t *filesystemsTab) Init(client *lambdaclient.Client) tea.Cmd {
	t.client = client
	t.loading = true
	return t.fetch
}

func (t *filesystemsTab) Refresh() tea.Cmd {
	if t.client == nil {
		return nil
	}
	return t.fetch
}

func (t *filesystemsTab) fetch() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	items, err := t.client.ListFilesystems(ctx)
	return filesystemsLoadedMsg{items: items, err: err}
}

func (t *filesystemsTab) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case filesystemsLoadedMsg:
		t.loading = false
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.filesystems = msg.items
			t.rebuildRows()
		}
		return nil, true

	case filesystemDeletedMsg:
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

func (t *filesystemsTab) View(width, height int) string {
	if t.err != nil && !t.loaded {
		return styleError.Render("Error: " + t.err.Error())
	}
	if !t.loaded {
		return loadingStyle.Render("Loading filesystems...")
	}
	if len(t.filesystems) == 0 {
		return styleMuted.Render("No filesystems.")
	}
	return t.viewWithFilter()
}

func (t *filesystemsTab) SetSize(width, height int) {
	t.baseTab.SetSize(width, height)
	t.table.SetColumns(fsColumns(width))
}

func (t *filesystemsTab) HasDetail() bool { return true }
func (t *filesystemsTab) HasCreate() bool { return true }

func (t *filesystemsTab) DetailView(width, _ int) string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	id := row[0]
	for _, fs := range t.filesystems {
		if fs.ID == id {
			return renderFilesystemDetail(fs, width)
		}
	}
	return ""
}

func renderFilesystemDetail(fs lambdaclient.Filesystem, width int) string {
	var b strings.Builder
	field := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s  %s\n",
				styleDetailLabel.Render(label),
				styleDetailValue.Render(value))
		}
	}

	field("ID", fs.ID)
	field("Name", fs.Name)
	field("Region", fs.Region.Name)
	field("Mount Point", fs.MountPoint)
	inUse := "false"
	if fs.IsInUse {
		inUse = "true"
	}
	field("In Use", inUse)
	field("Size", formatBytes(fs.BytesUsed))
	field("Created", fs.Created)

	return lipgloss.NewStyle().MaxWidth(width).Render(b.String())
}
func (t *filesystemsTab) SelectedID() string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	return row[0]
}

func (t *filesystemsTab) SelectedName() string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	return row[1]
}

func (t *filesystemsTab) DeleteSelected() tea.Cmd {
	id := t.SelectedID()
	if id == "" {
		return nil
	}
	return func() tea.Msg {
		err := t.client.DeleteFilesystem(context.Background(), id)
		return filesystemDeletedMsg{id: id, err: err}
	}
}

func fsColumns(width int) []table.Column {
	// ID(40) NAME(20) REGION(14) MOUNT(20) IN_USE(8) SIZE(12)
	return []table.Column{
		{Title: "ID", Width: 40},
		{Title: "NAME", Width: 20},
		{Title: "REGION", Width: 14},
		{Title: "MOUNT", Width: 20},
		{Title: "IN USE", Width: 8},
		{Title: "SIZE", Width: 12},
	}
}

func (t *filesystemsTab) rebuildRows() {
	rows := make([]table.Row, 0, len(t.filesystems))
	for _, fs := range t.filesystems {
		inUse := "false"
		if fs.IsInUse {
			inUse = "true"
		}
		rows = append(rows, table.Row{
			fs.ID, fs.Name, fs.Region.Name, fs.MountPoint, inUse, formatBytes(fs.BytesUsed),
		})
	}
	t.setAllRows(rows)
}
