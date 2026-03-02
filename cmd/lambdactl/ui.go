package main

import (
	"fmt"
	"os"

	"github.com/charmbracelet/lipgloss"
	ltable "github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/x/term"
)

// Shared styles used across commands.
var (
	styleAdded   = lipgloss.NewStyle().Foreground(lipgloss.Color("10")) // green
	styleRemoved = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))  // red
	styleChanged = lipgloss.NewStyle().Foreground(lipgloss.Color("11")) // yellow
	styleDim     = lipgloss.NewStyle().Faint(true)
	styleHeader  = lipgloss.NewStyle().Bold(true).Faint(true)
	styleLabel   = lipgloss.NewStyle().Faint(true)
)

func termWidth() int {
	w, _, err := term.GetSize(os.Stdout.Fd())
	if err != nil || w <= 0 {
		return 120
	}
	return w
}

// printListTable prints a standard borderless list table to stdout.
func printListTable(headers []string, rows [][]string) {
	if len(rows) == 0 {
		fmt.Println(styleDim.Render("(none)"))
		return
	}
	fmt.Println(newListTable(headers, rows, termWidth(), nil))
}

// newListTable builds a borderless lipgloss table. An optional StyleFunc
// overrides the default per-cell style (receives row, col indices).
func newListTable(headers []string, rows [][]string, width int, sf ltable.StyleFunc) string {
	if sf == nil {
		sf = defaultStyleFunc
	}
	return ltable.New().
		Headers(headers...).
		Rows(rows...).
		Width(width).
		Wrap(false).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderHeader(false).
		StyleFunc(sf).
		Render()
}

func defaultStyleFunc(row, col int) lipgloss.Style {
	if row == ltable.HeaderRow {
		return styleHeader
	}
	return lipgloss.NewStyle()
}

// printDetailTable prints a key-value detail view (label: value pairs).
func printDetailTable(fields [][]string) {
	t := ltable.New().
		Rows(fields...).
		Wrap(false).
		BorderTop(false).
		BorderBottom(false).
		BorderLeft(false).
		BorderRight(false).
		BorderColumn(false).
		BorderHeader(false).
		StyleFunc(func(row, col int) lipgloss.Style {
			if col == 0 {
				return styleLabel
			}
			return lipgloss.NewStyle()
		}).
		Render()
	fmt.Println(t)
}
