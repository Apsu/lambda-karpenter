package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// confirmModel is an overlay dialog for destructive actions.
// Rendered as overlay content; the dashboard composites it on top of the background.
type confirmModel struct {
	title    string
	subtitle string
	onYes    func() tea.Cmd
}

func newConfirmModel(title, subtitle string, onYes func() tea.Cmd) *confirmModel {
	return &confirmModel{title: title, subtitle: subtitle, onYes: onYes}
}

func (m *confirmModel) Update(msg tea.Msg) (tea.Cmd, bool) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			return m.onYes(), true
		case "n", "N", "esc":
			return nil, true // caller clears the overlay
		}
		return nil, true // swallow all other keys while dialog is open
	}
	return nil, false
}

// View returns the inner content for the overlay (no border — dashboard adds overlayStyle).
func (m *confirmModel) View() string {
	var b strings.Builder

	b.WriteString(styleConfirmTitle.Render(m.title))
	b.WriteString("\n")
	b.WriteString(styleMuted.Render(m.subtitle))
	b.WriteString("\n\n")
	b.WriteString(fmt.Sprintf("  %s    %s",
		styleInfo.Render("[y] Confirm"),
		styleConfirmHint.Render("[n] Cancel")))

	return b.String()
}
