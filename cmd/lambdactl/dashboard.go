package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/lambdal/lambda-karpenter/internal/lambdaclient"
	"github.com/lambdal/lambda-karpenter/internal/ratelimit"
)

// runDashboard constructs the client and starts the TUI.
func runDashboard() error {
	baseURL := os.Getenv("LAMBDA_API_BASE_URL")
	if baseURL == "" {
		baseURL = "https://cloud.lambda.ai"
	}
	token := os.Getenv("LAMBDA_API_TOKEN")
	if token == "" {
		if _, err := os.Stat("lambda-api.key"); err == nil {
			data, err := os.ReadFile("lambda-api.key")
			if err == nil {
				token = strings.TrimSpace(string(data))
			}
		}
	}
	if token == "" {
		return fmt.Errorf("LAMBDA_API_TOKEN is required (set in env or .env.local)")
	}

	limiter := ratelimit.New(1, 5*time.Second)
	client, err := lambdaclient.New(baseURL, token, limiter)
	if err != nil {
		return err
	}

	m := newDashboardModel(client)
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}

// --- messages ---

type refreshTickMsg struct{}

type actionSuccessMsg struct{ msg string }
type actionErrorMsg struct{ err error }

// --- view mode ---

type viewMode int

const (
	modeList viewMode = iota
	modeHelp
)

// fetchTimeout bounds how long any single tab refresh can take.
const fetchTimeout = 30 * time.Second

// --- createOverlay interface ---

type createOverlay interface {
	Init() tea.Cmd
	Update(msg tea.Msg) (tea.Cmd, bool)
	View(width, height int) string
}

// --- model ---

type dashboardModel struct {
	client *lambdaclient.Client
	width  int
	height int

	tabs      []tab
	activeTab int
	mode      viewMode

	confirm *confirmModel
	launch  *launchFormModel
	create  createOverlay
	result  *resultModel

	keys    keyMap
	spinner spinner.Model
	lastErr error

	statusMsg   string
	refreshing  bool // true while a manual refresh is in flight
	lastRefresh time.Time
	interval    time.Duration

	// Overlay dimensions (calculated by recalcLayout)
	overlayW int
	overlayH int
	contentH int

	showDetail     bool // true when detail overlay is visible
	detailOverlayW int
	detailOverlayH int
}

func newDashboardModel(client *lambdaclient.Client) dashboardModel {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	s.Style = styleMuted

	tabs := []tab{
		newInstancesTab(),
		newTypesTab(),
		newImagesTab(),
		newSSHKeysTab(),
		newFilesystemsTab(),
		newFirewallsTab(),
	}

	return dashboardModel{
		client:   client,
		tabs:     tabs,
		keys:     newKeyMap(),
		spinner:  s,
		interval: 30 * time.Second,
	}
}

func (m dashboardModel) Init() tea.Cmd {
	cmds := []tea.Cmd{m.spinner.Tick}
	for i := range m.tabs {
		cmds = append(cmds, m.tabs[i].Init(m.client))
	}
	cmds = append(cmds, m.tickRefresh())
	return tea.Batch(cmds...)
}

func (m dashboardModel) tickRefresh() tea.Cmd {
	return tea.Tick(m.interval, func(time.Time) tea.Msg {
		return refreshTickMsg{}
	})
}

func (m dashboardModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.recalcLayout()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd

	case actionSuccessMsg:
		m.statusMsg = styleSuccess.Render(msg.msg)
		return m, nil

	case actionErrorMsg:
		m.lastErr = msg.err
		m.statusMsg = styleError.Render(msg.err.Error())
		return m, nil

	case launchDataMsg:
		return m.handleLaunchData(msg)

	case launchDoneMsg:
		return m.handleLaunchDone(msg)

	// --- Create form messages ---
	case sshKeyCreatedMsg:
		m.create = nil
		if msg.err != nil {
			m.statusMsg = styleError.Render("SSH key: " + msg.err.Error())
			return m, nil
		}
		if msg.key.PrivateKey != "" {
			m.result = newResultModel("Generated SSH Key — save this private key!",
				"Name: "+msg.key.Name+"\nID: "+msg.key.ID+"\n\n"+msg.key.PrivateKey)
		} else {
			m.statusMsg = styleSuccess.Render("SSH key created: " + msg.key.Name)
		}
		return m, m.tabs[3].Refresh() // SSH Keys tab

	case fsFormDataMsg:
		if m.create != nil {
			cmd, _ := m.create.Update(msg)
			return m, cmd
		}
		return m, nil

	case fsCreatedMsg:
		m.create = nil
		if msg.err != nil {
			m.statusMsg = styleError.Render("Filesystem: " + msg.err.Error())
			return m, nil
		}
		m.statusMsg = styleSuccess.Render("Filesystem created: " + msg.fs.Name)
		return m, m.tabs[4].Refresh() // Filesystems tab

	case fwFormDataMsg:
		if m.create != nil {
			cmd, _ := m.create.Update(msg)
			return m, cmd
		}
		return m, nil

	case fwCreatedMsg:
		m.create = nil
		if msg.err != nil {
			m.statusMsg = styleError.Render("Firewall: " + msg.err.Error())
			return m, nil
		}
		m.statusMsg = styleSuccess.Render("Firewall created: " + msg.rs.Name)
		return m, m.tabs[5].Refresh() // Firewalls tab

	// --- Edit form messages ---
	case fwEditDataMsg:
		if m.create != nil {
			cmd, _ := m.create.Update(msg)
			return m, cmd
		}
		return m, nil

	case fwUpdatedMsg:
		m.create = nil
		if msg.err != nil {
			m.statusMsg = styleError.Render("Firewall update: " + msg.err.Error())
			return m, nil
		}
		m.statusMsg = styleSuccess.Render("Firewall updated: " + msg.rs.Name)
		return m, m.tabs[5].Refresh()

	case globalFWDataMsg:
		if m.create != nil {
			cmd, _ := m.create.Update(msg)
			return m, cmd
		}
		return m, nil

	case globalFWUpdatedMsg:
		m.create = nil
		if msg.err != nil {
			m.statusMsg = styleError.Render("Global firewall: " + msg.err.Error())
			return m, nil
		}
		m.statusMsg = styleSuccess.Render(fmt.Sprintf("Global firewall updated — %d rules", len(msg.rs.Rules)))
		return m, nil
	}

	// Confirmation overlay takes priority.
	if m.confirm != nil {
		cmd, handled := m.confirm.Update(msg)
		if handled {
			m.confirm = nil
			if cmd != nil {
				return m, cmd
			}
			return m, nil
		}
	}

	// Launch form overlay.
	if m.launch != nil {
		cmd, done := m.launch.Update(msg)
		if done {
			result := m.launch.result
			m.launch = nil
			if result != nil {
				return m, m.doLaunch(*result)
			}
			return m, nil
		}
		return m, cmd
	}

	// Create/Edit form overlay.
	if m.create != nil {
		cmd, done := m.create.Update(msg)
		if done {
			m.create = nil
			return m, cmd
		}
		return m, cmd
	}

	// Result overlay — any key dismisses.
	if m.result != nil {
		if _, ok := msg.(tea.KeyMsg); ok {
			m.result = nil
			return m, nil
		}
	}

	// Detail overlay — Esc dismisses; r/?/q/tab-switch fall through to global keys.
	if m.showDetail {
		if msg, ok := msg.(tea.KeyMsg); ok {
			switch {
			case key.Matches(msg, m.keys.Back):
				m.showDetail = false
				if it, ok := m.tabs[m.activeTab].(*instancesTab); ok {
					it.ClearDetail()
				}
				if ft, ok := m.tabs[m.activeTab].(*firewallsTab); ok {
					ft.ClearDetail()
				}
				return m, nil
			case key.Matches(msg, m.keys.Refresh),
				key.Matches(msg, m.keys.Help),
				key.Matches(msg, m.keys.Quit),
				key.Matches(msg, m.keys.NextTab),
				key.Matches(msg, m.keys.PrevTab),
				key.Matches(msg, m.keys.Tab1),
				key.Matches(msg, m.keys.Tab2),
				key.Matches(msg, m.keys.Tab3),
				key.Matches(msg, m.keys.Tab4),
				key.Matches(msg, m.keys.Tab5),
				key.Matches(msg, m.keys.Tab6):
				// fall through to global keys
			default:
				return m, nil // consume table nav and action keys
			}
		}
	}

	// If the active tab is filtering, delegate all keys to it.
	if msg, ok := msg.(tea.KeyMsg); ok {
		active := m.tabs[m.activeTab]
		if active.IsFiltering() {
			cmd, _ := active.Update(msg)
			return m, cmd
		}
	}

	// Global keys.
	if msg, ok := msg.(tea.KeyMsg); ok {
		// Help toggle.
		if key.Matches(msg, m.keys.Help) {
			if m.mode == modeHelp {
				m.mode = modeList
			} else {
				m.mode = modeHelp
				m.showDetail = false // detail gives way to help
			}
			return m, nil
		}

		// Quit.
		if key.Matches(msg, m.keys.Quit) {
			return m, tea.Quit
		}

		// Back / Esc.
		if key.Matches(msg, m.keys.Back) {
			if m.mode == modeHelp {
				m.mode = modeList
				return m, nil
			}
			return m, nil
		}

		// Don't process navigation keys while in help overlay.
		if m.mode == modeHelp {
			return m, nil
		}

		// Tab switching.
		if key.Matches(msg, m.keys.NextTab) {
			m.switchTab((m.activeTab + 1) % len(m.tabs))
			return m, nil
		}
		if key.Matches(msg, m.keys.PrevTab) {
			m.switchTab((m.activeTab - 1 + len(m.tabs)) % len(m.tabs))
			return m, nil
		}
		if key.Matches(msg, m.keys.Tab1) && len(m.tabs) > 0 {
			m.switchTab(0)
			return m, nil
		}
		if key.Matches(msg, m.keys.Tab2) && len(m.tabs) > 1 {
			m.switchTab(1)
			return m, nil
		}
		if key.Matches(msg, m.keys.Tab3) && len(m.tabs) > 2 {
			m.switchTab(2)
			return m, nil
		}
		if key.Matches(msg, m.keys.Tab4) && len(m.tabs) > 3 {
			m.switchTab(3)
			return m, nil
		}
		if key.Matches(msg, m.keys.Tab5) && len(m.tabs) > 4 {
			m.switchTab(4)
			return m, nil
		}
		if key.Matches(msg, m.keys.Tab6) && len(m.tabs) > 5 {
			m.switchTab(5)
			return m, nil
		}

		// Refresh.
		if key.Matches(msg, m.keys.Refresh) {
			m.refreshing = true
			m.statusMsg = ""
			m.lastRefresh = time.Now()
			return m, m.refreshAll()
		}

		// Filter.
		if key.Matches(msg, m.keys.Filter) && m.mode == modeList {
			m.tabs[m.activeTab].StartFilter()
			return m, nil
		}

		// Enter for detail view.
		if key.Matches(msg, m.keys.Enter) && m.mode == modeList {
			return m, m.enterDetail()
		}

		// Delete action.
		if key.Matches(msg, m.keys.Delete) && m.mode == modeList {
			m.promptDelete()
			return m, nil
		}

		// Launch action (only from instances tab).
		if key.Matches(msg, m.keys.Launch) && m.activeTab == 0 && m.mode == modeList {
			m.launch = &launchFormModel{}
			return m, m.openLaunchForm()
		}

		// Create action.
		if key.Matches(msg, m.keys.Create) && m.mode == modeList {
			return m, m.openCreate()
		}

		// Edit action (firewalls tab).
		if key.Matches(msg, m.keys.Edit) && m.mode == modeList {
			if ft, ok := m.tabs[m.activeTab].(*firewallsTab); ok {
				id := ft.SelectedID()
				if id != "" {
					if ft.IsGlobalSelected() {
						return m, m.openGlobalFirewallEdit()
					}
					return m, m.openFirewallEdit(id)
				}
			}
		}
	}

	// Refresh tick.
	if _, ok := msg.(refreshTickMsg); ok {
		m.lastRefresh = time.Now()
		cmds = append(cmds, m.refreshLoaded(), m.tickRefresh())
		return m, tea.Batch(cmds...)
	}

	// Delegate to all tabs so they can handle their own messages.
	for i := range m.tabs {
		cmd, handled := m.tabs[i].Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if handled && m.refreshing {
			m.refreshing = false
		}
	}
	if len(cmds) > 0 {
		return m, tea.Batch(cmds...)
	}
	return m, nil
}

func (m *dashboardModel) switchTab(idx int) {
	m.showDetail = false
	m.activeTab = idx
}

func (m *dashboardModel) recalcLayout() {
	// Measure chrome: status bar (1) + tab bar (1) + divider (1) + help bar (1) = 4
	chrome := 4
	m.contentH = m.height - chrome
	if m.contentH < 3 {
		m.contentH = 3
	}

	for i := range m.tabs {
		m.tabs[i].SetSize(m.width, m.contentH)
	}

	// Overlay outer dimensions (60% wide, 2/3 tall, centered).
	m.overlayW = m.width * 3 / 5
	m.overlayH = m.contentH * 2 / 3
	if m.overlayW < 40 {
		m.overlayW = 40
	}
	if m.overlayH < 10 {
		m.overlayH = 10
	}

	// Detail overlay — wider than form overlays for information-dense views.
	m.detailOverlayW = m.width * 4 / 5
	m.detailOverlayH = m.contentH * 3 / 4
	if m.detailOverlayW < 50 {
		m.detailOverlayW = 50
	}
	if m.detailOverlayH < 12 {
		m.detailOverlayH = 12
	}
}

func (m *dashboardModel) enterDetail() tea.Cmd {
	active := m.tabs[m.activeTab]
	if !active.HasDetail() || active.SelectedID() == "" {
		return nil
	}
	m.showDetail = true

	switch t := active.(type) {
	case *instancesTab:
		return t.FetchDetail()
	case *firewallsTab:
		return t.FetchDetail()
	}
	// Types, SSH Keys, Images, Filesystems use data already in the list.
	return nil
}

func (m *dashboardModel) promptDelete() {
	switch t := m.tabs[m.activeTab].(type) {
	case *instancesTab:
		id, typeName, region := t.SelectedInstance()
		if id == "" {
			return
		}
		m.confirm = newConfirmModel(
			"Terminate instance "+id+"?",
			typeName+" in "+region,
			func() tea.Cmd { return t.Terminate() },
		)
	case *sshKeysTab:
		id := t.SelectedID()
		name := t.SelectedName()
		if id == "" {
			return
		}
		m.confirm = newConfirmModel(
			"Delete SSH key "+name+"?",
			id,
			func() tea.Cmd { return t.DeleteSelected() },
		)
	case *filesystemsTab:
		id := t.SelectedID()
		name := t.SelectedName()
		if id == "" {
			return
		}
		m.confirm = newConfirmModel(
			"Delete filesystem "+name+"?",
			id,
			func() tea.Cmd { return t.DeleteSelected() },
		)
	case *firewallsTab:
		if t.IsGlobalSelected() {
			return // can't delete the global ruleset
		}
		id := t.SelectedID()
		name := t.SelectedName()
		if id == "" {
			return
		}
		m.confirm = newConfirmModel(
			"Delete firewall "+name+"?",
			id,
			func() tea.Cmd { return t.DeleteSelected() },
		)
	}
}

// openCreate opens the create form for the active tab.
func (m *dashboardModel) openCreate() tea.Cmd {
	switch m.tabs[m.activeTab].(type) {
	case *sshKeysTab:
		m.create = newSSHKeyCreateForm(m.client)
	case *filesystemsTab:
		m.create = newFilesystemCreateForm(m.client)
	case *firewallsTab:
		m.create = newFirewallCreateForm(m.client)
	}
	if m.create != nil {
		return m.create.Init()
	}
	return nil
}

func (m dashboardModel) refreshAll() tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.tabs {
		if cmd := m.tabs[i].Refresh(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func (m dashboardModel) refreshLoaded() tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.tabs {
		if m.tabs[i].Loaded() {
			if cmd := m.tabs[i].Refresh(); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
	}
	return tea.Batch(cmds...)
}

// --- view ---

func (m dashboardModel) View() string {
	if m.width == 0 {
		return "Starting..."
	}

	var sections []string

	// Status bar (top line)
	sections = append(sections, m.viewStatusBar())

	// Tab bar + divider
	sections = append(sections, m.viewTabBar())
	sections = append(sections, styleTabDivider.Render(strings.Repeat("─", m.width)))

	// Content area — always rendered at full height
	active := m.tabs[m.activeTab]

	content := active.View(m.width, m.contentH)

	// Pad content to fill content area
	content = padToHeight(content, m.contentH)

	// Composite overlay on top of content (background shows through)
	if overlay, ok := m.renderOverlay(); ok {
		content = placeOverlay(m.width, m.contentH, overlay, content)
	}

	sections = append(sections, content)

	// Help bar (bottom line)
	sections = append(sections, m.viewHelpBar())

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

func (m dashboardModel) viewStatusBar() string {
	title := styleStatusBadge.Render("LAMBDACTL")

	var info string
	if m.refreshing {
		info = "  " + styleMuted.Render("Refreshing...")
	} else if m.statusMsg != "" {
		info = "  " + m.statusMsg
	}

	var age string
	if !m.lastRefresh.IsZero() {
		d := time.Since(m.lastRefresh).Truncate(time.Second)
		age = "  " + styleStatusAge.Render(fmt.Sprintf("%s ago", d))
	}

	return m.spinner.View() + " " + title + info + age
}

func (m dashboardModel) viewTabBar() string {
	var tabs []string
	for i, t := range m.tabs {
		label := fmt.Sprintf("%d:%s", i+1, t.Name())
		if i == m.activeTab {
			tabs = append(tabs, styleTabActive.Render(label))
		} else {
			tabs = append(tabs, styleTabInactive.Render(label))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Bottom, tabs...)
}

func (m dashboardModel) viewHelpBar() string {
	var pairs []string

	switch {
	case m.confirm != nil:
		pairs = []string{"y", "confirm", "n", "cancel"}
	case m.launch != nil || m.create != nil:
		pairs = []string{"esc", "cancel"}
	case m.result != nil:
		pairs = []string{"any key", "close"}
	case m.mode == modeHelp:
		pairs = []string{"esc/?", "close"}
	case m.showDetail:
		pairs = []string{"esc", "back", "r", "refresh", "?", "help", "q", "quit"}
	default:
		active := m.tabs[m.activeTab]

		// Check if filtering
		if active.IsFiltering() {
			pairs = []string{"esc", "clear", "enter", "apply"}
			break
		}

		// Normal list mode — context-sensitive
		pairs = []string{"tab", "switch", "j/k", "nav", "/", "filter", "r", "refresh"}
		if active.HasDetail() {
			pairs = append(pairs, "enter", "detail")
		}
		switch m.tabs[m.activeTab].(type) {
		case *instancesTab:
			pairs = append(pairs, "d", "terminate", "L", "launch")
		case *sshKeysTab:
			pairs = append(pairs, "c", "create", "d", "delete")
		case *filesystemsTab:
			pairs = append(pairs, "c", "create", "d", "delete")
		case *firewallsTab:
			pairs = append(pairs, "c", "create", "e", "edit", "d", "delete")
		}
		pairs = append(pairs, "?", "help", "q", "quit")
	}

	var parts []string
	for i := 0; i < len(pairs)-1; i += 2 {
		parts = append(parts, helpKeyStyle.Render(pairs[i])+":"+helpDescStyle.Render(pairs[i+1]))
	}
	return strings.Join(parts, "  ")
}

// renderOverlay returns the rendered overlay string if an overlay is active.
func (m dashboardModel) renderOverlay() (string, bool) {
	// Compute inner dimensions by subtracting the overlay frame (border + padding).
	frameW, frameH := overlayStyle.GetFrameSize()
	innerW := m.overlayW - frameW
	innerH := m.overlayH - frameH
	if innerW < 20 {
		innerW = 20
	}
	if innerH < 4 {
		innerH = 4
	}

	var inner string

	switch {
	case m.confirm != nil:
		inner = m.confirm.View()
	case m.launch != nil:
		inner = m.launch.View(innerW, innerH)
	case m.create != nil:
		inner = m.create.View(innerW, innerH)
	case m.result != nil:
		inner = m.result.View(innerW, innerH)
	case m.showDetail:
		// Detail overlay uses wider dimensions for information-dense views.
		dInnerW := m.detailOverlayW - frameW
		dInnerH := m.detailOverlayH - frameH
		inner = m.tabs[m.activeTab].DetailView(dInnerW, dInnerH)
		s := overlayStyle.Width(dInnerW).Height(dInnerH)
		return s.Render(inner), true
	case m.mode == modeHelp:
		inner = helpContent()
	default:
		return "", false
	}

	s := overlayStyle.Width(innerW).Height(innerH)
	return s.Render(inner), true
}

// placeOverlay composites a foreground panel centered on top of a background,
// preserving background content to the left and right of the overlay.
func placeOverlay(bgWidth, bgHeight int, overlay, background string) string {
	bgLines := strings.Split(background, "\n")
	fgLines := strings.Split(overlay, "\n")

	fgWidth := lipgloss.Width(overlay)
	fgHeight := len(fgLines)

	// Center
	startX := (bgWidth - fgWidth) / 2
	startY := (bgHeight - fgHeight) / 2
	if startX < 0 {
		startX = 0
	}
	if startY < 0 {
		startY = 0
	}

	for i, fgLine := range fgLines {
		row := startY + i
		if row >= len(bgLines) {
			break
		}
		// Pad short background lines so ansi.Truncate can produce
		// the correct left offset for centering.
		bg := bgLines[row]
		if w := lipgloss.Width(bg); w < bgWidth {
			bg += strings.Repeat(" ", bgWidth-w)
		}
		left := ansi.Truncate(bg, startX, "")
		right := ansi.TruncateLeft(bg, startX+fgWidth, "")
		bgLines[row] = left + fgLine + right
	}

	return strings.Join(bgLines, "\n")
}

// padToHeight ensures content has exactly h lines.
func padToHeight(content string, h int) string {
	lines := strings.Count(content, "\n") + 1
	if lines < h {
		content += strings.Repeat("\n", h-lines)
	}
	return content
}

// helpContent builds the help modal text with sections and rows.
func helpContent() string {
	var b strings.Builder

	title := styleConfirmTitle.Render("Keybindings")
	b.WriteString(title)
	b.WriteString("\n\n")

	section := func(name string) {
		b.WriteString(styleDetailLabel.Render(name))
		b.WriteString("\n")
	}
	row := func(kb, desc string) {
		b.WriteString(fmt.Sprintf("  %s  %s\n",
			helpKeyStyle.Render(fmt.Sprintf("%-16s", kb)),
			helpDescStyle.Render(desc)))
	}

	section("Navigation")
	row("j / k, ↑ / ↓", "Move cursor up / down")
	row("g / G", "Go to top / bottom")
	row("Tab / S-Tab", "Next / previous tab")
	row("1-6", "Jump to tab by number")
	b.WriteString("\n")

	section("Actions")
	row("Enter", "Show detail view")
	row("Esc", "Back to list / close overlay")
	row("/", "Filter rows (substring search)")
	row("r", "Refresh all tabs")
	row("d", "Delete / terminate selected")
	row("L", "Launch new instance")
	row("c", "Create (SSH key / filesystem / firewall)")
	row("e", "Edit selected firewall ruleset")
	b.WriteString("\n")

	section("Other")
	row("?", "Toggle this help")
	row("q / Ctrl+C", "Quit")

	return b.String()
}
