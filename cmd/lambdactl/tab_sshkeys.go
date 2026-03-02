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

// wordWrap breaks s into lines of at most width characters.
func wordWrap(s string, width int) string {
	if width <= 0 || len(s) <= width {
		return s
	}
	var b strings.Builder
	for len(s) > width {
		b.WriteString(s[:width])
		b.WriteByte('\n')
		s = s[width:]
	}
	b.WriteString(s)
	return b.String()
}

type sshKeysLoadedMsg struct {
	items []lambdaclient.SSHKey
	err   error
}

type sshKeyDeletedMsg struct {
	id  string
	err error
}

type sshKeysTab struct {
	baseTab
	keys []lambdaclient.SSHKey
}

func newSSHKeysTab() *sshKeysTab {
	t := &sshKeysTab{baseTab: newBaseTab()}
	t.table.SetColumns(sshKeyColumns(80))
	return t
}

func (t *sshKeysTab) Name() string { return "SSH Keys" }

func (t *sshKeysTab) Init(client *lambdaclient.Client) tea.Cmd {
	t.client = client
	t.loading = true
	return t.fetch
}

func (t *sshKeysTab) Refresh() tea.Cmd {
	if t.client == nil {
		return nil
	}
	return t.fetch
}

func (t *sshKeysTab) fetch() tea.Msg {
	ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
	defer cancel()
	items, err := t.client.ListSSHKeys(ctx)
	return sshKeysLoadedMsg{items: items, err: err}
}

func (t *sshKeysTab) Update(msg tea.Msg) (tea.Cmd, bool) {
	switch msg := msg.(type) {
	case sshKeysLoadedMsg:
		t.loading = false
		t.loaded = true
		t.err = msg.err
		if msg.err == nil {
			t.keys = msg.items
			t.rebuildRows()
		}
		return nil, true

	case sshKeyDeletedMsg:
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

func (t *sshKeysTab) View(width, height int) string {
	if t.err != nil && !t.loaded {
		return styleError.Render("Error: " + t.err.Error())
	}
	if !t.loaded {
		return loadingStyle.Render("Loading SSH keys...")
	}
	if len(t.keys) == 0 {
		return styleMuted.Render("No SSH keys.")
	}
	return t.viewWithFilter()
}

func (t *sshKeysTab) SetSize(width, height int) {
	t.baseTab.SetSize(width, height)
	t.table.SetColumns(sshKeyColumns(width))
}

func (t *sshKeysTab) HasDetail() bool { return true }
func (t *sshKeysTab) HasCreate() bool { return true }

func (t *sshKeysTab) DetailView(width, _ int) string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	id := row[0]
	for _, k := range t.keys {
		if k.ID == id {
			return renderSSHKeyDetail(k, width)
		}
	}
	return ""
}

func renderSSHKeyDetail(k lambdaclient.SSHKey, width int) string {
	var b strings.Builder
	field := func(label, value string) {
		fmt.Fprintf(&b, "%s  %s\n",
			styleDetailLabel.Render(label),
			styleDetailValue.Render(value))
	}

	field("ID", k.ID)
	field("Name", k.Name)
	b.WriteString("\n")
	b.WriteString(styleDetailLabel.Render("Public Key:"))
	b.WriteString("\n")
	b.WriteString(wordWrap(k.PublicKey, width-2))
	b.WriteString("\n")

	return lipgloss.NewStyle().MaxWidth(width).Render(b.String())
}
func (t *sshKeysTab) SelectedID() string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	return row[0]
}

func (t *sshKeysTab) SelectedName() string {
	row := t.table.SelectedRow()
	if row == nil {
		return ""
	}
	return row[1]
}

func (t *sshKeysTab) DeleteSelected() tea.Cmd {
	id := t.SelectedID()
	if id == "" {
		return nil
	}
	return func() tea.Msg {
		err := t.client.DeleteSSHKey(context.Background(), id)
		return sshKeyDeletedMsg{id: id, err: err}
	}
}

func sshKeyColumns(width int) []table.Column {
	// ID(40) NAME(20) PUBLIC KEY(remainder)
	fixed := 40 + 20 + 3*2
	pkW := width - fixed
	if pkW < 20 {
		pkW = 20
	}
	return []table.Column{
		{Title: "ID", Width: 40},
		{Title: "NAME", Width: 20},
		{Title: "PUBLIC KEY", Width: pkW},
	}
}

func (t *sshKeysTab) rebuildRows() {
	rows := make([]table.Row, 0, len(t.keys))
	for _, k := range t.keys {
		pub := k.PublicKey
		if len(pub) > 60 {
			pub = pub[:57] + "..."
		}
		rows = append(rows, table.Row{k.ID, k.Name, pub})
	}
	t.setAllRows(rows)
}
