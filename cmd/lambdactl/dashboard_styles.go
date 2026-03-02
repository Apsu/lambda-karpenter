package main

import (
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
)

// All styles use ANSI colors 0–15 so they follow the terminal's theme
// (Solarized, Dracula, Gruvbox, etc. all remap these).
//
//	0: black    8: bright black (dark gray)
//	1: red      9: bright red
//	2: green   10: bright green
//	3: yellow  11: bright yellow
//	4: blue    12: bright blue
//	5: magenta 13: bright magenta
//	6: cyan    14: bright cyan
//	7: white   15: bright white

var (
	// Status bar (top) — reversed title badge
	styleStatusBadge = lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.ANSIColor(11)).
				Reverse(true).
				Padding(0, 1)
	styleStatusInfo = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(7))
	styleStatusAge  = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))

	// Tab bar
	styleTabActive   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.ANSIColor(15)).Padding(0, 2)
	styleTabInactive = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8)).Padding(0, 2)
	styleTabDivider  = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))

	// Help bar (bottom) — key:desc pairs
	helpKeyStyle  = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))
	helpDescStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(7))

	// Overlay panel (modals: help, confirm, launch)
	overlayStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.ANSIColor(3)). // yellow border
			Padding(0, 1)

	// Confirm dialog content
	styleConfirmTitle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(1)).Bold(true)
	styleConfirmHint  = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))

	// Detail view
	styleDetailLabel = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(3)).Bold(true).Width(14)
	styleDetailValue = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(7))

	// Semantic
	styleError   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(1))
	styleSuccess = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(2))
	styleWarn    = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(3))
	styleInfo    = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(4))
	styleMuted   = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))

	// Loading
	loadingStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8)).Italic(true)

	// Separator
	separatorStyle = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))

	// Filter bar
	styleFilterPrompt = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(3)).Bold(true)
	styleFilterBar    = lipgloss.NewStyle().Foreground(lipgloss.ANSIColor(8))
)

func dashboardTableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).Foreground(lipgloss.ANSIColor(8)).Padding(0, 1)
	s.Cell = s.Cell.Padding(0, 1)
	s.Selected = s.Selected.
		Bold(true).
		Reverse(true)
	return s
}

