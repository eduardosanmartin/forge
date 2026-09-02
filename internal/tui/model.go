package tui

import (
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"

	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/tui/components"
	"github.com/eduardosanmartin/forge/internal/tui/layouts"
)

// Layout constants.
const (
	LayoutHybrid  = "hybrid"
	LayoutSession = "session"
	LayoutMinimal = "minimal"
)

// order for cycling.
var layoutOrder = []string{LayoutHybrid, LayoutSession, LayoutMinimal}

// Model is the single tea.Model for the TUI.
type Model struct {
	layout      string
	palette     Palette
	paletteName string
	showSidebar bool
	width       int
	height      int

	// Config persistence.
	config     TUIConfig
	configPath string
	saveFn     func(TUIConfig) error // injectable for tests; defaults to SaveTUIConfig

	// Client abstraction for daemon calls (optional for tests).
	client TUIClient

	// Session state.
	sessionID string
	sessions  []daemon.SessionResult
	entries   []components.Entry
	lastSeq   int // highest daemon message seq appended to entries (dedup guard)
	// transcript viewport & input are held as components
	viewport viewport.Model
	input    components.InputModel

	// UI state.
	toast       string
	daemonAddr  string
	daemonVers  string
	daemonErr   string
	spinner     bool
	cwd         string
	keyMap      KeyMap
}

// TUIClient abstracts daemon RPC for the model.
type TUIClient interface {
	Status() (*daemon.StatusResult, error)
	ListSessions(limit int) (*daemon.ListSessionsResult, error)
	CreateSession() (*daemon.SessionResult, error)
	ExecuteTurn(sessionID, message string) (*daemon.ExecuteTurnResult, error)
	GetMessagesSince(sessionID string, sinceSeq int) (*daemon.GetMessagesResult, error)
}

// Messages for async daemon responses.
type statusMsg struct{ res *daemon.StatusResult; err error }
type listSessionsMsg struct{ res *daemon.ListSessionsResult; err error }
type createSessionMsg struct{ res *daemon.SessionResult; err error }
type executeTurnMsg struct{ res *daemon.ExecuteTurnResult; err error }
type messagesSinceMsg struct{ res *daemon.GetMessagesResult; err error }

// NewModel creates a model with defaults, optionally using cfg and client.
func NewModel(cfg TUIConfig, pal Palette, palName, configPath string, client TUIClient) Model {
	if cfg.Layout == "" {
		cfg = DefaultTUIConfig()
	}
	palResolved := pal
	if p, ok := GetPalette(cfg.Palette); ok {
		palResolved = p
		palName = cfg.Palette
	}
	layout := cfg.Layout
	if !IsValidLayout(layout) {
		layout = LayoutHybrid
	}
	m := Model{
		layout:      layout,
		palette:     palResolved,
		paletteName: palName,
		showSidebar: cfg.Sidebar,
		config:      cfg,
		configPath:  configPath,
		client:      client,
		cwd:         cwd(),
		keyMap:      DefaultKeyMap(),
	}
	if m.saveFn == nil {
		m.saveFn = func(c TUIConfig) error { return SaveTUIConfig(configPath, c) }
	}
	// Input area: single setup path via components (rebinds backspace-only
	// delete and shift+enter newline; enter is handled globally as send).
	m.input = components.NewInput("Type a message… (/help for commands)", 80, 3)

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))
	m.viewport = vp

	return m
}

func cwd() string {
	d, _ := os.Getwd()
	if d == "" {
		return "."
	}
	return d
}

// Accessors for tests.
func (m Model) Layout() string       { return m.layout }
func (m Model) ShowSidebar() bool    { return m.showSidebar }
func (m Model) PaletteName() string  { return m.paletteName }
func (m Model) SessionID() string    { return m.sessionID }
func (m Model) Toast() string        { return m.toast }
func (m Model) Entries() []components.Entry { return m.entries }

// SetSaveFn injects a persistence hook (tests).
func (m *Model) SetSaveFn(fn func(TUIConfig) error) { m.saveFn = fn }

// SetClient injects a client (tests).
func (m *Model) SetClient(c TUIClient) { m.client = c }

// SetSize sets terminal size and propagates to subcomponents.
func (m *Model) SetSize(w, h int) {
	m.width = w
	m.height = h
	// roughly allocate: transcript height = h - input - footer
	inputH := 4
	footerH := 2
	transH := h - inputH - footerH
	if transH < 5 {
		transH = 5
	}
	m.viewport.SetWidth(w)
	m.viewport.SetHeight(transH)
	m.input.SetSize(w, inputH)
}

// Init returns initial commands: fetch status and sessions.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.cmdStatus(), m.cmdListSessions())
}

func (m Model) cmdStatus() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.Status()
		return statusMsg{res: res, err: err}
	}
}
func (m Model) cmdListSessions() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.ListSessions(10)
		return listSessionsMsg{res: res, err: err}
	}
}
func (m Model) cmdCreateSession() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.CreateSession()
		return createSessionMsg{res: res, err: err}
	}
}
func (m Model) cmdExecuteTurn(msg string) tea.Cmd {
	if m.client == nil {
		return nil
	}
	sid := m.sessionID
	return func() tea.Msg {
		res, err := m.client.ExecuteTurn(sid, msg)
		return executeTurnMsg{res: res, err: err}
	}
}
func (m Model) cmdGetMessagesSince(since int) tea.Cmd {
	if m.client == nil {
		return nil
	}
	sid := m.sessionID
	return func() tea.Msg {
		res, err := m.client.GetMessagesSince(sid, since)
		return messagesSinceMsg{res: res, err: err}
	}
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		m.rebuildTranscript()
		return m, nil

	case statusMsg:
		if msg.err != nil {
			m.daemonErr = msg.err.Error()
		} else if msg.res != nil {
			m.daemonAddr = msg.res.Addr
			m.daemonVers = msg.res.Version
			m.daemonErr = ""
		}
		return m, nil

	case listSessionsMsg:
		if msg.err != nil {
			m.daemonErr = msg.err.Error()
			return m, nil
		}
		if msg.res != nil {
			m.sessions = msg.res.Sessions
		}
		// Start new session by default.
		return m, m.cmdCreateSession()

	case createSessionMsg:
		if msg.err != nil {
			m.daemonErr = msg.err.Error()
			return m, nil
		}
		if msg.res != nil {
			m.sessionID = msg.res.ID
			// refresh sessions list
			m.sessions = append(m.sessions, *msg.res)
		}
		return m, nil

	case executeTurnMsg:
		m.spinner = false
		if msg.err != nil {
			m.toast = msg.err.Error()
			return m, nil
		}
		if msg.res != nil {
			// ExecuteTurnResult.Messages is the authoritative transcript of the
			// turn — append it and advance lastSeq. No refetch: a get_messages_since
			// from 0 here would re-append the whole session history.
			newEntries := components.EntriesFromMessages(msg.res.Messages, msg.res.ToolTrace)
			// Replace the optimistic local echo with the daemon-confirmed copy.
			if len(newEntries) > 0 && len(m.entries) > 0 {
				last := m.entries[len(m.entries)-1]
				first := newEntries[0]
				if last.Local && last.Role == "user" && first.Role == "user" &&
					strings.TrimSpace(last.Content) == strings.TrimSpace(first.Content) {
					m.entries = m.entries[:len(m.entries)-1]
				}
			}
			m.entries = append(m.entries, newEntries...)
			for _, mr := range msg.res.Messages {
				if mr.Seq > m.lastSeq {
					m.lastSeq = mr.Seq
				}
			}
			m.rebuildTranscript()
		}
		return m, nil

	case messagesSinceMsg:
		if msg.err != nil {
			m.toast = msg.err.Error()
			return m, nil
		}
		// Append only messages newer than what we already rendered (dedup guard).
		var fresh []daemon.MessageResult
		for _, mr := range msg.res.Messages {
			if mr.Seq > m.lastSeq {
				fresh = append(fresh, mr)
			}
		}
		if len(fresh) > 0 {
			newEntries := components.EntriesFromMessages(fresh, nil)
			m.entries = append(m.entries, newEntries...)
			for _, mr := range fresh {
				if mr.Seq > m.lastSeq {
					m.lastSeq = mr.Seq
				}
			}
			m.rebuildTranscript()
		}
		return m, nil

	case tea.KeyPressMsg:
		// Global key handling BEFORE textarea.
		switch {
		case key.Matches(msg, m.keyMap.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keyMap.ToggleSidebar):
			m.toggleSidebar()
			return m, nil
		case key.Matches(msg, m.keyMap.CycleLayout):
			m.cycleLayout()
			// persist
			if err := m.persistConfig(); err != nil {
				m.toast = err.Error()
			}
			return m, nil
		default:
			// enter = send (slash commands parsed first). Any other key —
			// including shift+enter, bound in the textarea to InsertNewline —
			// falls through to the textarea delegate below.
			if msg.String() == "enter" {
				text := strings.TrimSpace(m.input.Value())
				if text == "" {
					return m, nil
				}
				// Slash command parsing.
				if strings.HasPrefix(text, "/") {
					if done, cmd := m.handleSlash(text); done {
						m.input.Reset()
						return m, cmd
					}
				}
				// Normal turn: optimistic local echo, clear input, spinner, execute.
				// The daemon-confirmed copy replaces this echo in executeTurnMsg.
				m.entries = append(m.entries, components.Entry{Role: "user", Content: text, Local: true})
				m.rebuildTranscript()
				m.input.Reset()
				m.spinner = true
				m.toast = ""
				return m, m.cmdExecuteTurn(text)
			}
		}
	}

	// Delegate to textarea for input editing (unless quit etc)
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) toggleSidebar() {
	// Semantics: hybrid and minimal render the sidebar as a floating overlay,
	// session renders it as a permanent column; in all three ctrl+o toggles
	// its visibility.
	m.showSidebar = !m.showSidebar
}

func (m *Model) cycleLayout() {
	idx := 0
	for i, l := range layoutOrder {
		if l == m.layout {
			idx = i
			break
		}
	}
	m.layout = layoutOrder[(idx+1)%len(layoutOrder)]
	m.config.Layout = m.layout
}

func (m *Model) persistConfig() error {
	if m.saveFn == nil {
		return nil
	}
	m.config.Palette = m.paletteName
	m.config.Layout = m.layout
	m.config.Sidebar = m.showSidebar
	return m.saveFn(m.config)
}

func (m *Model) handleSlash(text string) (bool, tea.Cmd) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return false, nil
	}
	cmd := parts[0]
	switch cmd {
	case "/layout":
		if len(parts) < 2 {
			m.toast = "usage: /layout <hybrid|session|minimal>"
			return true, nil
		}
		name := parts[1]
		if !IsValidLayout(name) {
			m.toast = fmt.Sprintf("unknown layout %q", name)
			return true, nil
		}
		m.layout = name
		if err := m.persistConfig(); err != nil {
			m.toast = err.Error()
		} else {
			m.toast = fmt.Sprintf("layout → %s", name)
		}
		return true, nil
	case "/palette":
		if len(parts) < 2 {
			m.toast = "usage: /palette <name>"
			return true, nil
		}
		name := parts[1]
		pal, ok := GetPalette(name)
		if !ok {
			m.toast = fmt.Sprintf("unknown palette %q", name)
			return true, nil
		}
		m.palette = pal
		m.paletteName = name
		if err := m.persistConfig(); err != nil {
			m.toast = err.Error()
		} else {
			m.toast = fmt.Sprintf("palette → %s", name)
		}
		m.rebuildTranscript()
		return true, nil
	case "/help":
		m.toast = "commands: /layout <name>, /palette <name>, /help"
		return true, nil
	default:
		m.toast = fmt.Sprintf("unknown command %q (/help for list)", cmd)
		return true, nil
	}
}

func toCompPalette(p Palette) components.Palette {
	return components.Palette{
		BG:         p.BG,
		BGElevated: p.BGElevated,
		Border:     p.Border,
		Text:       p.Text,
		Dim:        p.Dim,
		Faint:      p.Faint,
		Accent:     p.Accent,
		Success:    p.Success,
		Warning:    p.Warning,
		Error:      p.Error,
	}
}

func (m *Model) rebuildTranscript() {
	content := components.BuildContent(m.entries, toCompPalette(m.palette), m.width)
	m.viewport.SetContent(content)
	m.viewport.GotoBottom()
}

// View routes by layout.
func (m Model) View() tea.View {
	// Build subcomponents
	transcriptView := m.viewport.View()
	inputView := m.input.View()

	compPal := toCompPalette(m.palette)

	footer := components.FooterModel{
		Palette:     compPal,
		Width:       m.width,
		Cwd:         m.cwd,
		SessionID:   m.sessionID,
		DaemonAddr:  m.daemonAddr,
		Version:     m.daemonVers,
		Toast:       m.toast,
		DaemonErr:   m.daemonErr,
		ShowSpinner: m.spinner,
	}.Render()

	sidebar := components.SidebarModel{
		Data: components.SidebarData{
			SessionID: m.sessionID,
			Sessions:  m.sessions,
			Palette:   compPal,
		},
		Width:  28,
		Height: m.height - 2, // approx
	}

	var content string
	switch m.layout {
	case LayoutSession:
		if m.showSidebar {
			content = layouts.Session(sidebar, transcriptView, inputView, footer)
		} else {
			content = layouts.Minimal(transcriptView, inputView, footer)
		}
	case LayoutMinimal:
		content = layouts.Minimal(transcriptView, inputView, footer)
		if m.showSidebar {
			// overlay
			content = layouts.Hybrid(transcriptView, inputView, footer, sidebar, true)
		}
	default: // hybrid
		content = layouts.Hybrid(transcriptView, inputView, footer, sidebar, m.showSidebar)
	}

	// Wrap with palette background
	bg := lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BG)).Foreground(lipgloss.Color(m.palette.Text)).Width(m.width).Height(m.height).Render(content)

	v := tea.NewView(bg)
	v.AltScreen = true
	return v
}
