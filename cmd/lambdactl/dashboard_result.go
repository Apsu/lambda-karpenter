package main

import (
	"strings"
)

// resultModel is a read-only overlay for displaying operation results
// (primarily the generated SSH private key).
type resultModel struct {
	title string
	body  string
}

func newResultModel(title, body string) *resultModel {
	return &resultModel{title: title, body: body}
}

func (m *resultModel) View(width, height int) string {
	var b strings.Builder

	b.WriteString(styleConfirmTitle.Render(m.title))
	b.WriteString("\n\n")
	b.WriteString(m.body)
	b.WriteString("\n\n")
	b.WriteString(styleConfirmHint.Render("Press any key to close"))

	return b.String()
}
