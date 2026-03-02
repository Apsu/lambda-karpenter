package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
)

// tab is the interface each resource tab implements.
type tab interface {
	Name() string
	Init(client *lambdaclient.Client) tea.Cmd
	Refresh() tea.Cmd
	Update(msg tea.Msg) (tea.Cmd, bool) // returns (cmd, handled)
	View(width, height int) string
	SetSize(width, height int)

	HasDetail() bool
	DetailView(width, height int) string
	SelectedID() string

	// Loaded reports whether the tab has completed at least one fetch.
	Loaded() bool

	// Filtering
	StartFilter()
	IsFiltering() bool
	ClearFilter()

	// HasCreate reports whether this tab supports the create action.
	HasCreate() bool
}

// baseTab provides shared table state for all tabs.
type baseTab struct {
	client  *lambdaclient.Client
	table   table.Model
	loaded  bool
	loading bool
	err     error
	width   int
	height  int

	// Filtering state
	filterInput textinput.Model
	filtering   bool
	filterQuery string
	allRows     []table.Row
}

func newBaseTab() baseTab {
	t := table.New(
		table.WithFocused(true),
		table.WithStyles(dashboardTableStyles()),
	)
	fi := textinput.New()
	fi.Prompt = "/ "
	fi.PromptStyle = styleFilterPrompt
	return baseTab{table: t, filterInput: fi}
}

func (b *baseTab) SetSize(width, height int) {
	b.width = width
	h := height
	if b.filtering || b.filterQuery != "" {
		h-- // reserve 1 line for filter bar
	}
	b.height = height
	b.table.SetWidth(width)
	b.table.SetHeight(h)
}

func (b *baseTab) Loaded() bool { return b.loaded }

// HasCreate defaults to false; tabs override as needed.
func (b *baseTab) HasCreate() bool { return false }

// --- filtering ---

func (b *baseTab) StartFilter() {
	b.filtering = true
	b.filterInput.Focus()
	b.filterInput.SetValue(b.filterQuery)
	// Reduce table height by 1 for filter bar
	b.table.SetHeight(b.table.Height() - 1)
}

func (b *baseTab) IsFiltering() bool { return b.filtering }

func (b *baseTab) ClearFilter() {
	b.filtering = false
	b.filterInput.Blur()
	b.filterQuery = ""
	b.filterInput.SetValue("")
	b.applyFilter()
	// Restore table height
	b.table.SetHeight(b.table.Height() + 1)
}

func (b *baseTab) commitFilter() {
	b.filtering = false
	b.filterInput.Blur()
	b.filterQuery = b.filterInput.Value()
	b.applyFilter()
}

// setAllRows stores the unfiltered rows and applies the current filter.
func (b *baseTab) setAllRows(rows []table.Row) {
	b.allRows = rows
	b.applyFilter()
}

// applyFilter filters allRows by filterQuery and sets the table rows.
func (b *baseTab) applyFilter() {
	if b.filterQuery == "" {
		b.table.SetRows(b.allRows)
		return
	}
	q := strings.ToLower(b.filterQuery)
	var filtered []table.Row
	for _, row := range b.allRows {
		for _, cell := range row {
			if strings.Contains(strings.ToLower(cell), q) {
				filtered = append(filtered, row)
				break
			}
		}
	}
	b.table.SetRows(filtered)
}

// viewWithFilter wraps the table view with a filter bar when active.
func (b *baseTab) viewWithFilter() string {
	content := b.table.View()
	if b.filtering {
		bar := styleFilterBar.Render(b.filterInput.View())
		return content + "\n" + bar
	}
	if b.filterQuery != "" {
		total := len(b.allRows)
		shown := len(b.table.Rows())
		info := fmt.Sprintf("filter: %s (%d/%d)", b.filterQuery, shown, total)
		bar := styleFilterBar.Render(info)
		return content + "\n" + bar
	}
	return content
}

// updateTable delegates key messages to the embedded bubbles/table,
// but intercepts keys when filtering is active.
func (b *baseTab) updateTable(msg tea.Msg) tea.Cmd {
	if b.filtering {
		if km, ok := msg.(tea.KeyMsg); ok {
			switch km.String() {
			case "esc":
				b.ClearFilter()
				return nil
			case "enter":
				b.commitFilter()
				return nil
			default:
				var cmd tea.Cmd
				b.filterInput, cmd = b.filterInput.Update(msg)
				// Live filter as user types
				b.filterQuery = b.filterInput.Value()
				b.applyFilter()
				return cmd
			}
		}
	}
	var cmd tea.Cmd
	b.table, cmd = b.table.Update(msg)
	return cmd
}
