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

type imagesLoadedMsg struct {
	items []lambdaclient.Image
	err   error
}

type imagesTab struct {
	baseTab
	images []lambdaclient.Image
}

func newImagesTab() *imagesTab {
	t := &imagesTab{baseTab: newBaseTab()}
	t.table.SetColumns(imageColumns(80))
	return t
}

func (t *imagesTab) Name() string { return "Images" }

func (t *imagesTab) Init(client *lambdaclient.Client) tea.Cmd {
	t.client = client
	t.loading = true
	return t.fetch
}

func (t *imagesTab) Refresh() tea.Cmd {
	if t.client == nil {
		return nil
	}
	return t.fetch
}

func (t *imagesTab) fetch() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	items, err := t.client.ListImages(ctx)
	return imagesLoadedMsg{items: items, err: err}
}

func (t *imagesTab) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case imagesLoadedMsg:
		t.loading = false
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.images = msg.items
			t.rebuildRows()
		}
		return nil, true

	case tea.KeyMsg:
		cmd := t.updateTable(msg)
		return cmd, false
	}
	return nil, false
}

func (t *imagesTab) View(width, height int) string {
	if t.err != nil && !t.loaded {
		return styleError.Render("Error: " + t.err.Error())
	}
	if !t.loaded {
		return loadingStyle.Render("Loading images...")
	}
	if len(t.images) == 0 {
		return styleMuted.Render("No images.")
	}
	return t.viewWithFilter()
}

func (t *imagesTab) SetSize(width, height int) {
	t.baseTab.SetSize(width, height)
	t.table.SetColumns(imageColumns(width))
}

func (t *imagesTab) HasDetail() bool { return true }

func (t *imagesTab) DetailView(width, _ int) string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	id := row[0]
	for _, img := range t.images {
		if img.ID == id {
			return renderImageDetail(img, width)
		}
	}
	return ""
}

func renderImageDetail(img lambdaclient.Image, width int) string {
	var b strings.Builder
	field := func(label, value string) {
		if value != "" {
			fmt.Fprintf(&b, "%s  %s\n",
				styleDetailLabel.Render(label),
				styleDetailValue.Render(value))
		}
	}

	field("ID", img.ID)
	field("Family", img.Family)
	field("Name", img.Name)
	if img.Region.Description != "" {
		field("Region", img.Region.Name+" ("+img.Region.Description+")")
	} else {
		field("Region", img.Region.Name)
	}
	field("Architecture", img.Arch)
	if !img.CreatedTime.IsZero() {
		field("Created", img.CreatedTime.Format(time.RFC3339))
	}
	if !img.UpdatedTime.IsZero() {
		field("Updated", img.UpdatedTime.Format(time.RFC3339)+" ("+shortDuration(time.Since(img.UpdatedTime))+" ago)")
	}

	return lipgloss.NewStyle().MaxWidth(width).Render(b.String())
}
func (t *imagesTab) SelectedID() string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	return row[0]
}

func imageColumns(width int) []table.Column {
	// ID(40) FAMILY(20) NAME(remainder) REGION(14) ARCH(8)
	fixed := 40 + 20 + 14 + 8 + 5*2
	nameW := width - fixed
	if nameW < 16 {
		nameW = 16
	}
	return []table.Column{
		{Title: "ID", Width: 40},
		{Title: "FAMILY", Width: 20},
		{Title: "NAME", Width: nameW},
		{Title: "REGION", Width: 14},
		{Title: "ARCH", Width: 8},
	}
}

func (t *imagesTab) rebuildRows() {
	rows := make([]table.Row, 0, len(t.images))
	for _, img := range t.images {
		updated := ""
		if !img.UpdatedTime.IsZero() {
			updated = shortDuration(time.Since(img.UpdatedTime)) + " ago"
		}
		_ = updated // not shown in columns to save space; available for detail
		rows = append(rows, table.Row{
			img.ID, img.Family, img.Name, img.Region.Name, img.Arch,
		})
	}
	t.setAllRows(rows)
}
