package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
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

	// TUI-2: live streaming and daemon control.
	clock        Clock
	totalTokens  int
	eventsCh     <-chan daemon.JSONRPCNotification
	pendingTools map[string]pendingTool // toolCallID -> pending
	pendingOrder []string               // insertion order for fallback matching
	markedIDs    map[string]bool        // sessions marked success

	// TUI-4: spinner animation, model/elapsed, suggestions, help, session cycle
	spinnerModel spinner.Model
	currentModel string    // from ExecuteTurnResult.Model (when present)
	turnStart    time.Time // clock time when turn was sent
	helpVisible  bool
	helpViewport viewport.Model
	// slash suggestions
	suggestions        []slashSuggestion
	suggestionIdx      int
	suggestionsVisible bool

	// TUI-5: session focus mode (arrow navigation), delta burst coalescing, scroll state
	sessionFocus    bool
	sessionFocusIdx int
	deltaPending    bool
	rebuildCount    int // instrumentation for coalescing tests; counts viewport SetContent calls
}

type pendingTool struct {
	start time.Time
	index int
	name  string
}

// slashSuggestion describes one slash command for autocomplete.
type slashSuggestion struct {
	Command     string
	Description string
}

var allSlashSuggestions = []slashSuggestion{
	{"/help", "show help panel and keybindings"},
	{"/layout", "change layout: hybrid | session | minimal"},
	{"/palette", "change palette"},
	{"/resume", "resume current session"},
	{"/model", "switch model"},
	{"/mark", "mark session as success"},
}

// TUIClient abstracts daemon RPC for the model.
type TUIClient interface {
	Status() (*daemon.StatusResult, error)
	ListSessions(limit int) (*daemon.ListSessionsResult, error)
	CreateSession() (*daemon.SessionResult, error)
	ExecuteTurn(sessionID, message string) (*daemon.ExecuteTurnResult, error)
	GetMessagesSince(sessionID string, sinceSeq int) (*daemon.GetMessagesResult, error)
	HaltSession(sessionID, reason string) error
	ResumeSession(sessionID string) error
	SwitchModel(sessionID, model string) error
	MarkSuccess(sessionID string) error
	Events(ctx context.Context) (<-chan daemon.JSONRPCNotification, error)
}

// Messages for async daemon responses.
type statusMsg struct{ res *daemon.StatusResult; err error }
type listSessionsMsg struct{ res *daemon.ListSessionsResult; err error }
type createSessionMsg struct{ res *daemon.SessionResult; err error }
type executeTurnMsg struct{ res *daemon.ExecuteTurnResult; err error }
type messagesSinceMsg struct{ res *daemon.GetMessagesResult; err error }

// TUI-2 daemon control result messages.
type haltResultMsg struct{ err error }
type resumeResultMsg struct{ err error }
type switchModelResultMsg struct {
	model string
	err   error
}
type markSuccessResultMsg struct{ err error }

// Event streaming messages.
type daemonEventMsg struct{ notif daemon.JSONRPCNotification }
type eventClosedMsg struct{}
type eventErrorMsg struct{ err error }

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
		layout:       layout,
		palette:      palResolved,
		paletteName:  palName,
		showSidebar:  cfg.Sidebar,
		config:       cfg,
		configPath:   configPath,
		client:       client,
		cwd:          cwd(),
		keyMap:       DefaultKeyMap(),
		clock:        realClock{},
		pendingTools: make(map[string]pendingTool),
		markedIDs:    make(map[string]bool),
	}
	if m.saveFn == nil {
		m.saveFn = func(c TUIConfig) error { return SaveTUIConfig(configPath, c) }
	}
	// Input area: single setup path via components (rebinds backspace-only
	// delete and shift+enter+ctrl+j newline; enter is handled globally as send).
	m.input = components.NewInput("Type a message… (/help for commands)", 80, 3)

	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(20))
	m.viewport = vp

	// Spinner v2: Line spinner distinct from OpenCode's braille dots.
	// Choice: Line (| / - \) is visually distinct from MiniDot braille dots (⠋⠙⠹…) used in chat,
	// and its 4-frame cycle at 500ms/frame (2 FPS) is clearly perceptible without
	// being distracting. FPS is set to 2/sec (500ms) via Spinner.FPS.
	// Determinism: tests drive TickMsg manually; wall timing not used in assertions.
	lineSpinner := spinner.Line
	lineSpinner.FPS = time.Second / 2
	m.spinnerModel = spinner.New(spinner.WithSpinner(lineSpinner))

	// Help viewport (floating overlay, scrollable)
	helpVP := viewport.New(viewport.WithWidth(60), viewport.WithHeight(20))
	m.helpViewport = helpVP

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
func (m Model) Layout() string                  { return m.layout }
func (m Model) ShowSidebar() bool               { return m.showSidebar }
func (m Model) PaletteName() string             { return m.paletteName }
func (m Model) SessionID() string               { return m.sessionID }
func (m Model) Toast() string                   { return m.toast }
func (m Model) Entries() []components.Entry     { return m.entries }
func (m Model) TotalTokens() int                { return m.totalTokens }
func (m Model) MarkedIDs() map[string]bool      { return m.markedIDs }
func (m Model) IsSpinner() bool                 { return m.spinner }
func (m Model) EventsCh() <-chan daemon.JSONRPCNotification { return m.eventsCh }
func (m Model) CurrentModel() string            { return m.currentModel }
func (m Model) IsHelpVisible() bool             { return m.helpVisible }
func (m Model) IsSuggestionsVisible() bool      { return m.suggestionsVisible }
func (m Model) SuggestionIndex() int            { return m.suggestionIdx }
func (m Model) SuggestionsList() []slashSuggestion { return m.suggestions }
func (m Model) SpinnerFrame() string            { return m.spinnerModel.View() }
func (m Model) RebuildCount() int               { return m.rebuildCount }
func (m Model) IsSessionFocus() bool            { return m.sessionFocus }
func (m Model) SessionFocusIdx() int            { return m.sessionFocusIdx }
func (m Model) SpinnerFPS() time.Duration       { return m.spinnerModel.Spinner.FPS }
func (m Model) ViewportYOffset() int            { return m.viewport.YOffset() }
func (m Model) ViewportAtBottom() bool          { return m.viewport.AtBottom() }

// SetSaveFn injects a persistence hook (tests).
func (m *Model) SetSaveFn(fn func(TUIConfig) error) { m.saveFn = fn }

// SetClient injects a client (tests).
func (m *Model) SetClient(c TUIClient) { m.client = c }

// SetClock injects a clock (tests).
func (m *Model) SetClock(c Clock) { m.clock = c }

// SetEventsChannel injects the event channel (tests).
func (m *Model) SetEventsChannel(ch <-chan daemon.JSONRPCNotification) { m.eventsCh = ch }

func (m Model) effectiveTranscriptWidth() int {
	if m.layout == LayoutSession && m.showSidebar {
		sidebarW := 28
		avail := m.width - sidebarW - 2 // gap for join/border
		if avail < 20 {
			avail = 20
			if m.width > sidebarW+1 && avail > m.width-sidebarW-1 {
				avail = m.width - sidebarW - 1
				if avail < 10 {
					avail = 10
				}
			}
		}
		if avail > m.width {
			avail = m.width
		}
		return avail
	}
	return m.width
}

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
	tw := m.effectiveTranscriptWidth()
	m.viewport.SetWidth(tw)
	m.viewport.SetHeight(transH)
	m.input.SetSize(tw, inputH)
	// Help overlay should stay within terminal width with padding
	hw := w - 4
	if hw < 40 {
		hw = 40
	}
	if hw > w {
		hw = w
	}
	hh := h - 4
	if hh < 10 {
		hh = 10
	}
	m.helpViewport.SetWidth(hw)
	m.helpViewport.SetHeight(hh)
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

func (m Model) cmdHalt() tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return haltResultMsg{err: fmt.Errorf("not connected")} }
	}
	sid := m.sessionID
	return func() tea.Msg {
		err := m.client.HaltSession(sid, "user halt via TUI")
		return haltResultMsg{err: err}
	}
}

func (m Model) cmdResume() tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return resumeResultMsg{err: fmt.Errorf("not connected")} }
	}
	sid := m.sessionID
	return func() tea.Msg {
		err := m.client.ResumeSession(sid)
		return resumeResultMsg{err: err}
	}
}

func (m Model) cmdSwitchModel(name string) tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return switchModelResultMsg{model: name, err: fmt.Errorf("not connected")} }
	}
	sid := m.sessionID
	return func() tea.Msg {
		err := m.client.SwitchModel(sid, name)
		return switchModelResultMsg{model: name, err: err}
	}
}

func (m Model) cmdMarkSuccess() tea.Cmd {
	if m.client == nil {
		return func() tea.Msg { return markSuccessResultMsg{err: fmt.Errorf("not connected")} }
	}
	sid := m.sessionID
	return func() tea.Msg {
		err := m.client.MarkSuccess(sid)
		return markSuccessResultMsg{err: err}
	}
}

func (m Model) cmdSubscribeEvents() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		ch, err := m.client.Events(context.Background())
		if err != nil {
			return eventErrorMsg{err: err}
		}
		// Return a sentinel that Update will use to store channel and start wait.
		// We use daemonEventMsg with empty notif as sentinel? Instead return a
		// custom message carrying the channel. Introduce eventsSubscribedMsg.
		return eventsSubscribedMsg{ch: ch}
	}
}

type eventsSubscribedMsg struct{ ch <-chan daemon.JSONRPCNotification }

// deltaRebuildMsg coalesces burst deltas to a single rebuild at ~14fps.
type deltaRebuildMsg struct{}

func (m Model) cmdWaitEvent(ch <-chan daemon.JSONRPCNotification) tea.Cmd {
	return func() tea.Msg {
		notif, ok := <-ch
		if !ok {
			return eventClosedMsg{}
		}
		return daemonEventMsg{notif: notif}
	}
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case spinner.TickMsg:
		// Deterministic spinner: only advance while turn in flight; ignore stray ticks after stop.
		if !m.spinner {
			return m, nil
		}
		if msg.ID != 0 && msg.ID != m.spinnerModel.ID() {
			return m, nil
		}
		var scmd tea.Cmd
		m.spinnerModel, scmd = m.spinnerModel.Update(msg)
		// Rebuild to refresh spinner frame beside pending user message / footer
		m.rebuildTranscript()
		if m.spinner {
			cmds = append(cmds, scmd)
			if nxt := m.cmdSpinnerTick(); nxt != nil {
				cmds = append(cmds, nxt)
			}
		}
		return m, tea.Batch(cmds...)

	case deltaRebuildMsg:
		m.deltaPending = false
		m.rebuildTranscript()
		return m, nil

	case tea.MouseWheelMsg:
		if m.helpVisible {
			return m, nil
		}
		// Forward wheel to viewport (scroll)
		m.viewport, _ = m.viewport.Update(msg)
		return m, nil

	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		m.rebuildTranscript()
		return m, nil

	case eventsSubscribedMsg:
		m.eventsCh = msg.ch
		return m, m.cmdWaitEvent(m.eventsCh)

	case daemonEventMsg:
		// After processing, re-arm wait for next event.
		cmds = append(cmds, m.handleDaemonEvent(msg.notif))
		if m.eventsCh != nil {
			cmds = append(cmds, m.cmdWaitEvent(m.eventsCh))
		}
		return m, tea.Batch(cmds...)

	case eventClosedMsg:
		m.toast = "event stream closed"
		m.daemonErr = "event stream closed"
		return m, nil

	case eventErrorMsg:
		m.toast = msg.err.Error()
		m.daemonErr = msg.err.Error()
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
		// Start new session by default and subscribe to events.
		return m, tea.Batch(m.cmdCreateSession(), m.cmdSubscribeEvents())

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
		// Ensure we are subscribed after session creation if not already.
		if m.eventsCh == nil {
			return m, m.cmdSubscribeEvents()
		}
		return m, nil

	case executeTurnMsg:
		// Authoritative signal for spinner: executeTurnMsg is the single
		// source of truth for turn completion. Session.event is NOT used to
		// stop the spinner to avoid double-stop races between the synchronous
		// result and live notifications. Halt events stop spinner separately.
		m.spinner = false
		m.deltaPending = false
		if msg.err != nil {
			// RF-2.6 failure semantics: if a streaming preview is in-flight,
			// keep the partial text visible but mark it as interrupted — do NOT
			// silently delete what the user was reading. This preserves the
			// live preview as evidence of the failure point.
			if idx := m.findStreamingIndex(); idx != -1 {
				m.entries[idx].Streaming = false
				if m.entries[idx].Meta == "" {
					m.entries[idx].Meta = "stream interrupted"
				} else if !strings.Contains(m.entries[idx].Meta, "stream interrupted") {
					m.entries[idx].Meta = m.entries[idx].Meta + " · stream interrupted"
				}
				m.rebuildTranscript()
			}
			m.toast = msg.err.Error()
			return m, nil
		}
		if msg.res != nil {
			// Update current model (documented source: ExecuteTurnResult.Model when present).
			if msg.res.Model != "" {
				m.currentModel = msg.res.Model
			}
			// Compute CLIENT-side elapsed from send to arrival using injected Clock.
			elapsedStr := ""
			if !m.turnStart.IsZero() && m.clock != nil {
				elapsed := m.clock.Now().Sub(m.turnStart)
				if elapsed < 0 {
					elapsed = 0
				}
				if elapsed < time.Second {
					elapsedStr = fmt.Sprintf("%dms", elapsed.Milliseconds())
				} else {
					elapsedStr = fmt.Sprintf("%.1fs", elapsed.Seconds())
				}
			}
			// Accumulate tokens.
			if msg.res.Usage != nil {
				m.totalTokens += msg.res.Usage.TotalTokens
			} else {
				for _, mr := range msg.res.Messages {
					if mr.Usage != nil {
						m.totalTokens += mr.Usage.TotalTokens
					}
				}
			}
			// Message-level dedup FIRST: live events may have delivered some of
			// these messages already. EntriesFromMessages can emit more entries
			// than messages (assistant tool calls), so dedup must happen on
			// message seqs before conversion — never by pairing entry indexes.
			existingSeqs := make(map[int]bool, len(m.entries))
			for _, ex := range m.entries {
				if ex.Seq != 0 {
					existingSeqs[ex.Seq] = true
				}
			}
			var fresh []daemon.MessageResult
			for _, mr := range msg.res.Messages {
				if mr.Seq == 0 || !existingSeqs[mr.Seq] {
					fresh = append(fresh, mr)
				}
			}
			newEntries := components.EntriesFromMessages(fresh, msg.res.ToolTrace)
			// Inject model+elapsed into assistant entries Meta: "<model> · <elapsed>"
			// When both present join with " · ", otherwise show whichever is available.
			if elapsedStr != "" || m.currentModel != "" {
				metaPiece := ""
				if m.currentModel != "" && elapsedStr != "" {
					metaPiece = m.currentModel + " · " + elapsedStr
				} else if m.currentModel != "" {
					metaPiece = m.currentModel
				} else {
					metaPiece = elapsedStr
				}
				for i, e := range newEntries {
					if e.Role == "assistant" && !e.IsTool {
						// Append to existing meta (e.g. tokens) with separator if needed.
						if e.Meta != "" && metaPiece != "" {
							newEntries[i].Meta = e.Meta + " · " + metaPiece
						} else if metaPiece != "" {
							newEntries[i].Meta = metaPiece
						}
						break // only first assistant entry gets model/elapsed per turn
					}
				}
			}
			// Streaming confirmation swap (TUI-3): if a streaming preview is
			// active, the authoritative assistant entry replaces it in-place
			// rather than being appended. Search, don't assume position —
			// tool events may have landed after the streaming entry.
			if idx := m.findStreamingIndex(); idx != -1 {
				for j, e := range newEntries {
					if e.Role == "assistant" && !e.IsTool {
						m.entries[idx] = e
						newEntries = append(newEntries[:j], newEntries[j+1:]...)
						break
					}
				}
				// If no assistant entry matched (e.g. tool-only turn), just
				// finalize the preview to avoid a lingering streaming caret.
				if idx < len(m.entries) && m.entries[idx].Streaming {
					m.entries[idx].Streaming = false
					if m.entries[idx].Meta == "" {
						m.entries[idx].Meta = "stream interrupted"
					}
				}
				// Replace user echo in-place to preserve order when tool
				// events interleaved. Normal path uses removal+append which
				// would place user after tool if tool was after echo.
				for j, e := range newEntries {
					if e.Role == "user" {
						if m.replaceLocalEchoInPlace(e) {
							newEntries = append(newEntries[:j], newEntries[j+1:]...)
						}
						break
					}
				}
				m.entries = append(m.entries, newEntries...)
			} else {
				// Replace the optimistic local echo with the daemon-confirmed
				// copy — in-place to preserve order when tool events
				// interleaved. On in-place success the confirmed entry is
				// already in the transcript, so drop it from the append list;
				// fall back to search-and-remove otherwise.
				if len(newEntries) > 0 {
					if m.replaceLocalEchoInPlace(newEntries[0]) {
						newEntries = newEntries[1:]
					} else {
						m.absorbLocalEcho(newEntries[0])
					}
				}
				m.entries = append(m.entries, newEntries...)
			}
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

	case haltResultMsg:
		m.spinner = false
		m.deltaPending = false
		if msg.err != nil {
			m.toast = msg.err.Error()
		} else {
			m.toast = "halted"
		}
		return m, nil

	case resumeResultMsg:
		if msg.err != nil {
			m.toast = msg.err.Error()
		} else {
			m.toast = "resumed"
		}
		return m, nil

	case switchModelResultMsg:
		if msg.err != nil {
			m.toast = msg.err.Error()
		} else {
			m.toast = fmt.Sprintf("model → %s", msg.model)
			m.currentModel = msg.model
		}
		return m, nil

	case markSuccessResultMsg:
		if msg.err != nil {
			m.toast = msg.err.Error()
		} else {
			m.toast = "marked success"
			if m.sessionID != "" {
				m.markedIDs[m.sessionID] = true
				// Update local sessions slice metadata if present.
				for i, s := range m.sessions {
					if s.ID == m.sessionID {
						if m.sessions[i].Metadata == nil {
							m.sessions[i].Metadata = map[string]any{}
						}
						m.sessions[i].Metadata["success"] = true
					}
				}
			}
		}
		return m, nil

	case tea.KeyPressMsg:
		// Help overlay intercepts everything: esc or any key closes it.
		if m.helpVisible {
			m.helpVisible = false
			return m, nil
		}
		// Session focus mode intercepts BEFORE suggestions and globals
		if m.sessionFocus {
			switch msg.String() {
			case "up":
				if len(m.sessions) > 0 {
					m.sessionFocusIdx--
					if m.sessionFocusIdx < 0 {
						m.sessionFocusIdx = len(m.sessions) - 1
					}
				}
				return m, nil
			case "down":
				if len(m.sessions) > 0 {
					m.sessionFocusIdx = (m.sessionFocusIdx + 1) % len(m.sessions)
				}
				return m, nil
			case "enter":
				if len(m.sessions) > 0 && m.sessionFocusIdx >= 0 && m.sessionFocusIdx < len(m.sessions) {
					sel := m.sessions[m.sessionFocusIdx]
					// Same load semantics as ctrl+g cycle: lastSeq=0, echo cleared, GetMessagesSince(0)
					m.sessionID = sel.ID
					m.lastSeq = 0
					// Clear local echo entries
					m.entries = nil
					m.rebuildTranscriptForceBottom()
					m.toast = fmt.Sprintf("session → %s", sel.ID[:8])
					m.suggestionsVisible = false
					m.sessionFocus = false
					m.input.Focus()
					if m.client != nil {
						return m, m.cmdGetMessagesSince(0)
					}
					return m, nil
				}
				m.sessionFocus = false
				m.input.Focus()
				return m, nil
			case "esc":
				m.sessionFocus = false
				m.input.Focus()
				return m, nil
			default:
				if key.Matches(msg, m.keyMap.GrabSession) {
					// ctrl+g again toggles off
					m.sessionFocus = false
					m.input.Focus()
					return m, nil
				}
				// Any other key consumed, not sent to input
				return m, nil
			}
		}
		// Suggestion navigation intercepts before global keys
		if m.suggestionsVisible && len(m.suggestions) > 0 {
			switch msg.String() {
			case "up":
				if m.suggestionIdx > 0 {
					m.suggestionIdx--
				} else {
					m.suggestionIdx = len(m.suggestions) - 1
				}
				return m, nil
			case "down":
				m.suggestionIdx = (m.suggestionIdx + 1) % len(m.suggestions)
				return m, nil
			case "tab":
				m.suggestionComplete()
				return m, nil
			case "esc":
				m.suggestionsVisible = false
				m.suggestions = nil
				m.suggestionIdx = 0
				return m, nil
			case "enter":
				// tab or enter COMPLETES the highlighted suggestion into the input
				// (enter sends only when no suggestion is highlighted)
				m.suggestionComplete()
				return m, nil
			}
		}
		// Global key handling BEFORE textarea.
		switch {
		case key.Matches(msg, m.keyMap.Quit):
			return m, tea.Quit
		case key.Matches(msg, m.keyMap.Halt):
			// ctrl+h must work even during a turn (not swallowed by textarea).
			// The textarea's DeleteCharacterBackward is rebound to backspace only,
			// so ctrl+h is free and reaches here.
			if m.sessionID == "" {
				m.toast = "no session to halt"
				return m, nil
			}
			m.spinner = false
			m.deltaPending = false
			// Keep input enabled (textarea stays focused); just stop spinner and toast.
			return m, m.cmdHalt()
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
		case key.Matches(msg, m.keyMap.GrabSession):
			// TUI-5: ctrl+g toggles session focus mode instead of immediate cycle.
			// In focus mode: sidebar highlighted, footer hint, up/down to navigate, enter to switch.
			if len(m.sessions) == 0 {
				m.toast = "no sessions"
				return m, nil
			}
			m.sessionFocus = true
			// Initialize selection to current session index
			idx := 0
			for i, s := range m.sessions {
				if s.ID == m.sessionID {
					idx = i
					break
				}
			}
			m.sessionFocusIdx = idx
			m.input.Blur()
			return m, nil
		case msg.String() == "esc":
			if m.suggestionsVisible {
				m.suggestionsVisible = false
				m.suggestions = nil
				m.suggestionIdx = 0
				return m, nil
			}
			return m, nil
		default:
			// Scroll keys forwarded to viewport when help closed and not in focus mode
			// (pgup/pgdn/home/end). Viewport handles PageUp/PageDown; home/end via GotoTop/Bottom.
			switch msg.String() {
			case "pgup", "pgdown":
				m.viewport, _ = m.viewport.Update(msg)
				return m, nil
			case "home":
				m.viewport.GotoTop()
				return m, nil
			case "end":
				m.viewport.GotoBottom()
				return m, nil
			}
			// enter = send (slash commands parsed first). Any other key —
			// including shift+enter, bound in the textarea to InsertNewline —
			// falls through to the textarea delegate below.
			if msg.String() == "enter" {
				// INPUT NOT LOCKED DURING IN-FLIGHT TURN: while spinner true, enter does NOT send — show hint.
				if m.spinner {
					m.toast = "turn in flight… ctrl+h to halt"
					return m, nil
				}
				// Close suggestions on send
				m.suggestionsVisible = false
				text := strings.TrimSpace(m.input.Value())
				if text == "" {
					return m, nil
				}
				// Slash command parsing.
				if strings.HasPrefix(text, "/") {
					if done, cmd := m.handleSlash(text); done {
						m.input.Reset()
						m.suggestionsVisible = false
						return m, cmd
					}
				}
				// Defensive reset: if a previous streaming buffer is still
				// unresolved (e.g. no confirmation arrived), finalize it so
				// the new turn starts clean. Keep partial text as interrupted
				// rather than silently discarding it.
				if idx := m.findStreamingIndex(); idx != -1 {
					m.entries[idx].Streaming = false
					if m.entries[idx].Meta == "" {
						m.entries[idx].Meta = "stream interrupted"
					} else if !strings.Contains(m.entries[idx].Meta, "stream interrupted") {
						m.entries[idx].Meta = m.entries[idx].Meta + " · stream interrupted"
					}
				}
				// Normal turn: optimistic local echo, clear input, spinner, execute.
				// The daemon-confirmed copy replaces this echo in executeTurnMsg.
				m.entries = append(m.entries, components.Entry{Role: "user", Content: text, Local: true})
				m.rebuildTranscriptForceBottom()
				m.input.Reset()
				m.suggestionsVisible = false
				m.spinner = true
				if m.clock == nil {
					m.clock = realClock{}
				}
				m.turnStart = m.clock.Now()
				m.toast = ""
				// Animated spinner: tick loop while m.spinner, plus immediate frame
				tickImmediate := func() tea.Msg { return m.spinnerModel.Tick() }
				return m, tea.Batch(m.cmdExecuteTurn(text), tickImmediate, m.cmdSpinnerTick())
			}
		}
	}

	// Delegate to textarea for input editing (unless quit etc)
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	// Update suggestions after input change
	m.updateSuggestions()
	return m, tea.Batch(cmds...)
}

func (m *Model) handleDaemonEvent(notif daemon.JSONRPCNotification) tea.Cmd {
	switch notif.Method {
	case daemon.MethodMessageEvent:
		var payload daemon.MessageEventPayload
		if err := json.Unmarshal(notif.Params, &payload); err != nil {
			m.toast = err.Error()
			return nil
		}
		// Filter by session if we have one.
		if m.sessionID != "" && payload.SessionID != "" && payload.SessionID != m.sessionID {
			return nil
		}
		seq := payload.Message.Seq
		if seq <= m.lastSeq {
			return nil
		}
		// Build entry from payload.
		mr := daemon.MessageResult{
			Seq:     seq,
			Role:    payload.Message.Role,
			Content: payload.Message.Content,
		}
		entries := components.EntriesFromMessages([]daemon.MessageResult{mr}, nil)
		// Streaming confirmation swap (TUI-3): if this authoritative
		// assistant message corresponds to an active preview, replace the
		// streaming entry in-place. Search, don't assume position.
		if len(entries) > 0 && entries[0].Role == "assistant" && m.hasStreamingEntry() {
			if m.absorbStreamingEntry(entries[0]) {
				if seq > m.lastSeq {
					m.lastSeq = seq
				}
				m.rebuildTranscript()
				return nil
			}
		}
		// A live-confirmed user message replaces the optimistic local echo.
		if len(entries) > 0 {
			m.absorbLocalEcho(entries[0])
		}
		m.entries = append(m.entries, entries...)
		if seq > m.lastSeq {
			m.lastSeq = seq
		}
		// If payload carries usage, accumulate (not currently in payload but future-proof).
		m.rebuildTranscript()
		return nil

	case daemon.MethodMessageDelta:
		var payload daemon.MessageDeltaPayload
		if err := json.Unmarshal(notif.Params, &payload); err != nil {
			m.toast = err.Error()
			return nil
		}
		return m.handleMessageDelta(payload)

	case daemon.MethodToolCallEvent:
		var payload daemon.ToolCallEventPayload
		if err := json.Unmarshal(notif.Params, &payload); err != nil {
			m.toast = err.Error()
			return nil
		}
		if m.sessionID != "" && payload.SessionID != "" && payload.SessionID != m.sessionID {
			return nil
		}
		return m.handleToolCallEvent(payload)

	case daemon.MethodSessionEvent:
		var payload daemon.SessionEventPayload
		if err := json.Unmarshal(notif.Params, &payload); err != nil {
			m.toast = err.Error()
			return nil
		}
		// Could update session list toast etc. For now just ensure sidebar reflects changes if needed.
		// If this is a success marking, update markedIds.
		if payload.Metadata != nil {
			if v, ok := payload.Metadata["success"]; ok {
				if b, ok := v.(bool); ok && b {
					m.markedIDs[payload.SessionID] = true
					for i, s := range m.sessions {
						if s.ID == payload.SessionID {
							if m.sessions[i].Metadata == nil {
								m.sessions[i].Metadata = map[string]any{}
							}
							m.sessions[i].Metadata["success"] = true
						}
					}
				}
			}
		}
		return nil

	case daemon.MethodEmergencyHalt:
		var payload daemon.EmergencyHaltPayload
		if err := json.Unmarshal(notif.Params, &payload); err != nil {
			m.toast = err.Error()
			return nil
		}
		m.spinner = false
		m.toast = "halted"
		if payload.Reason != "" {
			m.toast = fmt.Sprintf("halted (%s)", payload.Reason)
		}
		return nil

	default:
		return nil
	}
}

// handleToolCallEvent manages pending tool call timing and transcript.
// Started events create a pending entry; finished/error events compute
// CLIENT-COMPUTED duration and update the entry's Meta to "Nms".
// Matching is primarily by ToolCallID; if payload.ToolCallID is empty the
// heuristic falls back to matching by name + pending-order (oldest pending
// with same name). Orphan completions (no pending) are ignored gracefully.
func (m *Model) handleToolCallEvent(payload daemon.ToolCallEventPayload) tea.Cmd {
	if m.clock == nil {
		m.clock = realClock{}
	}
	switch payload.Status {
	case "started":
		// Create pending entry.
		entry := components.Entry{IsTool: true, ToolName: payload.Name}
		m.entries = append(m.entries, entry)
		idx := len(m.entries) - 1
		key := payload.ToolCallID
		if key == "" {
			// Use name + index as synthetic key for fallback case.
			key = fmt.Sprintf("__pending:%s:%d", payload.Name, idx)
		}
		m.pendingTools[key] = pendingTool{start: m.clock.Now(), index: idx, name: payload.Name}
		m.pendingOrder = append(m.pendingOrder, key)
		m.rebuildTranscript()
		return nil
	case "finished", "error":
		// Try exact ID match first.
		key := payload.ToolCallID
		pt, ok := m.pendingTools[key]
		if !ok && key == "" {
			// Heuristic: find oldest pending with same name.
			for _, k := range m.pendingOrder {
				if p, exists := m.pendingTools[k]; exists && p.name == payload.Name {
					pt = p
					key = k
					ok = true
					break
				}
			}
		} else if !ok && key != "" {
			// Also try heuristic if ID not found but name matches pending order.
			for _, k := range m.pendingOrder {
				if p, exists := m.pendingTools[k]; exists && p.name == payload.Name {
					pt = p
					key = k
					ok = true
					break
				}
			}
		}
		if !ok {
			// Orphan completion: ignore gracefully, no panic.
			return nil
		}
		elapsed := m.clock.Now().Sub(pt.start)
		ms := elapsed.Milliseconds()
		if ms < 0 {
			ms = 0
		}
		meta := fmt.Sprintf("%dms", ms)
		if payload.Status == "error" && payload.Error != "" {
			// Could append error info, but duration is primary per spec.
			_ = payload.Error
		}
		if pt.index >= 0 && pt.index < len(m.entries) {
			m.entries[pt.index].Meta = meta
		}
		delete(m.pendingTools, key)
		// Remove from order.
		newOrder := m.pendingOrder[:0]
		for _, k := range m.pendingOrder {
			if k != key {
				newOrder = append(newOrder, k)
			}
		}
		m.pendingOrder = newOrder
		m.rebuildTranscript()
		return nil
	default:
		return nil
	}
}

func (m *Model) toggleSidebar() {
	// Per-layout toggle semantics (documented):
	// - hybrid: toggles overlay visibility
	// - session: toggles column visibility
	// - minimal: toggles internal flag but View() is authoritative and ALWAYS renders Minimal (no overlay),
	//   so the toggle has no visual effect in minimal. This guarantees the three layouts remain visually distinct
	//   even when showSidebar was toggled in minimal before cycling.
	m.showSidebar = !m.showSidebar
	m.config.Sidebar = m.showSidebar
	// Recompute effective width so transcript never overflows terminal in session column mode.
	m.viewport.SetWidth(m.effectiveTranscriptWidth())
	m.input.SetSize(m.effectiveTranscriptWidth(), 4)
	m.rebuildTranscript()
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
	// Normalize showSidebar per layout for distinct visuals (fixes LAYOUT COLLAPSE):
	// hybrid → sidebar on (overlay), session → sidebar on (column), minimal → sidebar OFF (no overlay).
	// View() is also authoritative: Minimal always renders Minimal regardless of flag.
	// This ensures ctrl+l always lands on a visually different state.
	switch m.layout {
	case LayoutHybrid:
		m.showSidebar = true
	case LayoutSession:
		m.showSidebar = true
	case LayoutMinimal:
		m.showSidebar = false
	}
	m.config.Sidebar = m.showSidebar
	// Update viewport/input width for session column layout observability.
	m.viewport.SetWidth(m.effectiveTranscriptWidth())
	m.input.SetSize(m.effectiveTranscriptWidth(), 4)
	m.rebuildTranscript()
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
		// Recompute effective width for session column observability
		m.viewport.SetWidth(m.effectiveTranscriptWidth())
		m.input.SetSize(m.effectiveTranscriptWidth(), 4)
		m.rebuildTranscript()
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
	case "/resume":
		if m.client == nil {
			m.toast = "not connected"
			return true, nil
		}
		if m.sessionID == "" {
			m.toast = "no session"
			return true, nil
		}
		return true, m.cmdResume()
	case "/model":
		if len(parts) < 2 {
			m.toast = "usage: /model <name>"
			return true, nil
		}
		name := parts[1]
		if strings.TrimSpace(name) == "" {
			m.toast = "usage: /model <name>"
			return true, nil
		}
		if m.client == nil {
			m.toast = "not connected"
			return true, nil
		}
		if m.sessionID == "" {
			m.toast = "no session"
			return true, nil
		}
		return true, m.cmdSwitchModel(name)
	case "/mark":
		if m.client == nil {
			m.toast = "not connected"
			return true, nil
		}
		if m.sessionID == "" {
			m.toast = "no session"
			return true, nil
		}
		return true, m.cmdMarkSuccess()
	case "/help":
		// TUI-4: /help opens a floating scrollable overlay panel (viewport) with full command list + keybindings.
		// Closed by esc or any key; replaces toast-based help (keep toast for command errors).
		m.openHelp()
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

// absorbLocalEcho removes an optimistic local user echo matching the given
// daemon-confirmed entry, searching the whole transcript (events or tool lines
// may have landed after the echo, so position-based matching is not safe).
// No-op when the confirmed entry is not a user message or no echo matches.
func (m *Model) absorbLocalEcho(confirmed components.Entry) {
	if confirmed.Role != "user" {
		return
	}
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		if e.Local && e.Role == "user" &&
			strings.TrimSpace(e.Content) == strings.TrimSpace(confirmed.Content) {
			m.entries = append(m.entries[:i], m.entries[i+1:]...)
			return
		}
	}
}

// findStreamingIndex returns the index of the active streaming preview entry,
// or -1 if none. There should be at most one streaming entry at a time.
func (m *Model) findStreamingIndex() int {
	for i, e := range m.entries {
		if e.Streaming {
			return i
		}
	}
	return -1
}

func (m *Model) hasStreamingEntry() bool { return m.findStreamingIndex() != -1 }

// absorbStreamingEntry replaces the active streaming preview with the
// confirmed assistant entry. It searches the whole transcript (tool events may
// have landed after the streaming entry, so position-based matching is not safe).
// Returns true if a swap occurred.
func (m *Model) absorbStreamingEntry(confirmed components.Entry) bool {
	if confirmed.Role != "assistant" {
		return false
	}
	idx := m.findStreamingIndex()
	if idx == -1 {
		return false
	}
	// Preserve the confirmed entry's fields; ensure Streaming is false.
	confirmed.Streaming = false
	m.entries[idx] = confirmed
	return true
}

// replaceLocalEchoInPlace replaces an optimistic local echo in-place (preserving
// order) rather than removing and appending at end. Used for streaming swap
// to keep user → assistant → tool order when tool events interleaved.
func (m *Model) replaceLocalEchoInPlace(confirmed components.Entry) bool {
	if confirmed.Role != "user" {
		return false
	}
	for i, e := range m.entries {
		if e.Local && e.Role == "user" && strings.TrimSpace(e.Content) == strings.TrimSpace(confirmed.Content) {
			m.entries[i] = confirmed
			return true
		}
	}
	return false
}

// handleMessageDelta handles passive streaming deltas (message.delta.event).
// Deltas are best-effort previews and carry no Seq; they are purely additive
// and must not affect lastSeq. The TUI behaves identically to TUI-2 when no
// deltas arrive (passive: no config coupling).
// TUI-5 coalescing: each delta mutates the streaming entry immediately but
// viewport rebuild is throttled to ~14fps (70ms) via deltaRebuildMsg. A burst
// of N deltas in <1s thus costs a handful of rebuilds, not N. Correctness
// invariants (swap on message.event, failure semantics) remain untouched.
func (m *Model) handleMessageDelta(payload daemon.MessageDeltaPayload) tea.Cmd {
	// Session filter like other events.
	if m.sessionID != "" && payload.SessionID != "" && payload.SessionID != m.sessionID {
		return nil
	}
	if payload.Delta == "" {
		return nil
	}
	if idx := m.findStreamingIndex(); idx == -1 {
		if !m.spinner {
			return nil
		}
		if m.sessionID == "" {
			return nil
		}
		m.entries = append(m.entries, components.Entry{
			Role:      "assistant",
			Content:   payload.Delta,
			Streaming: true,
		})
		return m.scheduleDeltaRebuild()
	}
	idx := m.findStreamingIndex()
	if idx == -1 {
		return nil
	}
	m.entries[idx].Content += payload.Delta
	return m.scheduleDeltaRebuild()
}

func (m *Model) scheduleDeltaRebuild() tea.Cmd {
	if m.deltaPending {
		return nil
	}
	m.deltaPending = true
	return tea.Tick(70*time.Millisecond, func(t time.Time) tea.Msg {
		return deltaRebuildMsg{}
	})
}

func (m *Model) rebuildTranscript() {
	m.rebuildTranscriptInternal(false)
}

func (m *Model) rebuildTranscriptForceBottom() {
	m.rebuildTranscriptInternal(true)
}

func (m *Model) rebuildTranscriptInternal(forceBottom bool) {
	wasAtBottom := m.viewport.AtBottom()
	w := m.effectiveTranscriptWidth()
	content := components.BuildContent(m.entries, toCompPalette(m.palette), w)
	// Spinner v2: also shown beside pending user message while turn in flight.
	// Implementation choice: render spinner frame as a pending line UNDER the last user entry,
	// rather than inline beside wrapped text. This avoids wrapping distortion and keeps the
	// streaming preview entry as the sole mutable assistant content. The pending line uses
	// the same Line spinner frame shown in the footer, at 500ms/frame (2 FPS), distinct
	// from OpenCode's braille dots. Documented: Line spinner ( | / - \ ) vs MiniDot.
	if m.spinner && m.findStreamingIndex() == -1 {
		hasPendingUser := false
		for _, e := range m.entries {
			if e.Role == "user" {
				hasPendingUser = true
			}
		}
		if hasPendingUser && len(m.entries) > 0 && m.entries[len(m.entries)-1].Role == "user" {
			frame := m.spinnerModel.View()
			if frame == "" {
				frame = "⠋"
			}
			spinnerLine := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render(frame + " working…")
			content = content + "\n" + spinnerLine
		} else if hasPendingUser {
			// Fallback: if last entry not user (e.g. tool interleaved), still show pending line
			frame := m.spinnerModel.View()
			if frame == "" {
				frame = "⠋"
			}
			spinnerLine := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render(frame + " working…")
			content = content + "\n" + spinnerLine
		}
	}
	m.viewport.SetContent(content)
	m.rebuildCount++
	if forceBottom || wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// ---- TUI-4 helpers ----

func (m *Model) updateSuggestions() {
	val := m.input.Value()
	if !strings.HasPrefix(val, "/") {
		m.suggestionsVisible = false
		m.suggestions = nil
		m.suggestionIdx = 0
		return
	}
	prefix := strings.Fields(val)
	if len(prefix) == 0 {
		prefix = []string{val}
	}
	// Use first token as filter, e.g. "/lay" -> filtered list.
	filter := prefix[0]
	var filtered []slashSuggestion
	for _, s := range allSlashSuggestions {
		if strings.HasPrefix(s.Command, filter) {
			filtered = append(filtered, s)
		}
	}
	// If no prefix filter matches, show all slash commands.
	if len(filtered) == 0 && filter == "/" {
		filtered = allSlashSuggestions
	}
	if len(filtered) == 0 {
		m.suggestionsVisible = false
		m.suggestions = nil
		m.suggestionIdx = 0
		return
	}
	m.suggestions = filtered
	m.suggestionsVisible = true
	if m.suggestionIdx >= len(filtered) {
		m.suggestionIdx = 0
	}
}

func (m *Model) suggestionComplete() {
	if !m.suggestionsVisible || len(m.suggestions) == 0 {
		return
	}
	if m.suggestionIdx < 0 || m.suggestionIdx >= len(m.suggestions) {
		m.suggestionIdx = 0
	}
	sel := m.suggestions[m.suggestionIdx].Command
	// Complete with trailing space for commands expecting args; bare commands also get space for ergonomics.
	m.input.SetValue(sel + " ")
	m.suggestionsVisible = false
	m.suggestions = nil
	m.suggestionIdx = 0
}

func (m Model) renderSuggestions() string {
	if !m.suggestionsVisible || len(m.suggestions) == 0 {
		return ""
	}
	pal := m.palette
	styleSel := lipgloss.NewStyle().Background(lipgloss.Color(pal.BGElevated)).Foreground(lipgloss.Color(pal.Accent)).Bold(true)
	styleNorm := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Text))
	styleDim := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Dim))
	styleBorder := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(pal.Border)).Background(lipgloss.Color(pal.BGElevated)).Padding(0, 1)
	var lines []string
	for i, s := range m.suggestions {
		cmdStr := s.Command
		desc := s.Description
		line := cmdStr + "  " + styleDim.Render(desc)
		if i == m.suggestionIdx {
			line = styleSel.Render("▶ " + cmdStr) + " " + styleNorm.Render(desc)
		} else {
			line = styleNorm.Render("  "+cmdStr) + " " + styleDim.Render(desc)
		}
		lines = append(lines, line)
	}
	inner := strings.Join(lines, "\n")
	return styleBorder.Width(m.effectiveTranscriptWidth()).Render(inner)
}

func (m *Model) openHelp() {
	m.helpVisible = true
	// Build help content: commands + keybindings
	var sb strings.Builder
	pal := m.palette
	titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Accent)).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Dim))
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(pal.Text))
	sb.WriteString(titleStyle.Render("Forge TUI Help — /help"))
	sb.WriteString("\n\n")
	sb.WriteString(textStyle.Render("Slash Commands:"))
	sb.WriteString("\n")
	for _, s := range allSlashSuggestions {
		sb.WriteString(textStyle.Render("  "+s.Command) + dimStyle.Render("  —  "+s.Description) + "\n")
	}
	sb.WriteString("\n")
	sb.WriteString(textStyle.Render("Input:"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  enter         send message (blocked while turn in flight — ctrl+h to halt)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  shift+enter   newline (requires terminal enhanced-key reporting)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  ctrl+j        newline (portable alternative)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  / prefix      slash suggestions: ↑/↓ navigate, tab/enter complete, esc dismiss; enter sends only when no suggestion highlighted"))
	sb.WriteString("\n\n")
	sb.WriteString(textStyle.Render("Keybindings:"))
	sb.WriteString("\n")
	for _, b := range m.keyMap.ShortHelp() {
		help := b.Help()
		sb.WriteString(dimStyle.Render("  "+help.Key) + textStyle.Render("  "+help.Desc) + "\n")
	}
	// Document session focus mode and scroll
	sb.WriteString(textStyle.Render("Session Focus:"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  ctrl+g        toggle session focus mode (sidebar highlighted)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  ↑/↓ (focus)   navigate sessions"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  enter (focus) switch to selected session (lastSeq=0, echo cleared)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  esc/ctrl+g    exit focus mode (other keys consumed)"))
	sb.WriteString("\n")
	sb.WriteString(textStyle.Render("Scrolling:"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  pgup/pgdown   scroll transcript"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  home/end      top/bottom"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  mouse wheel   scroll (when terminal mouse reported)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  more below    footer shows ↓ more below when not at bottom (stick-to-bottom)"))
	sb.WriteString("\n")
	// Full help group
	for _, group := range m.keyMap.FullHelp() {
		for _, b := range group {
			// avoid duplicate short help already shown
			_ = b
		}
	}
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("Press esc or any key to close."))
	m.helpViewport.SetContent(sb.String())
	m.helpViewport.GotoTop()
}

func (m *Model) cmdSpinnerTick() tea.Cmd {
	if !m.spinner {
		return nil
	}
	// Schedule next tick via spinner model's FPS
	id := m.spinnerModel.ID()
	// Use the spinner's FPS for deterministic interval.
	fps := m.spinnerModel.Spinner.FPS
	if fps == 0 {
		fps = time.Second / 2
	}
	return tea.Tick(fps, func(t time.Time) tea.Msg {
		return spinner.TickMsg{Time: t, ID: id}
	})
}

func (m *Model) cycleSession() (bool, tea.Cmd) {
	if len(m.sessions) == 0 {
		m.toast = "no sessions"
		return false, nil
	}
	// Find current index
	idx := -1
	for i, s := range m.sessions {
		if s.ID == m.sessionID {
			idx = i
			break
		}
	}
	nextIdx := (idx + 1) % len(m.sessions)
	// If no current (idx -1) then nextIdx 0
	next := m.sessions[nextIdx]
	m.sessionID = next.ID
	// Reset lastSeq to 0 then apply via GetMessagesSince(0) — respect semantics: reset lastSeq to 0 then apply, local echo cleared.
	m.lastSeq = 0
	// For minimal viable: clear all entries and reload transcript via GetMessagesSince(0)
	// Spec says entries replaced, lastSeq reset, echo cleared — so clear entries wholesale.
	m.entries = nil
	m.rebuildTranscriptForceBottom()
	m.toast = fmt.Sprintf("session → %s", next.ID[:8])
	// Also clear suggestions
	m.suggestionsVisible = false
	return true, m.cmdGetMessagesSince(0)
}

func (m *Model) switchToSession(idx int) (bool, tea.Cmd) {
	if len(m.sessions) == 0 || idx < 0 || idx >= len(m.sessions) {
		return false, nil
	}
	sel := m.sessions[idx]
	m.sessionID = sel.ID
	m.lastSeq = 0
	m.entries = nil
	m.rebuildTranscriptForceBottom()
	m.toast = fmt.Sprintf("session → %s", sel.ID[:8])
	m.suggestionsVisible = false
	return true, m.cmdGetMessagesSince(0)
}

// View routes by layout.
func (m Model) View() tea.View {
	// Build subcomponents
	transcriptView := m.viewport.View()
	inputView := m.input.View()
	// Slash suggestions floating list above input
	if m.suggestionsVisible {
		suggView := m.renderSuggestions()
		if suggView != "" {
			inputView = suggView + "\n" + inputView
		}
	}
	// Session focus hint in input area
	if m.sessionFocus {
		focusHint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render("▸ session focus: ↑/↓ navigate · enter switch · esc/ctrl+g back")
		inputView = focusHint + "\n" + inputView
	}

	compPal := toCompPalette(m.palette)

	// Footer: show "more below" hint when viewport not at bottom (stick-to-bottom)
	showMoreBelow := false
	if len(m.entries) > 0 && !m.viewport.AtBottom() {
		showMoreBelow = true
	}
	footerHint := ""
	if m.sessionFocus {
		footerHint = "session focus"
	}
	toastWithHint := m.toast
	if showMoreBelow && m.toast == "" {
		// Footer hint via MoreBelow field is handled in FooterModel; keep toast clean
	}

	footer := components.FooterModel{
		Palette:      compPal,
		Width:        m.width,
		Cwd:          m.cwd,
		SessionID:    m.sessionID,
		DaemonAddr:   m.daemonAddr,
		Version:      m.daemonVers,
		Toast:        toastWithHint,
		DaemonErr:    m.daemonErr,
		ShowSpinner:  m.spinner,
		SpinnerView:  m.spinnerModel.View(),
		Layout:       m.layout,
		ModelName:    m.currentModel,
		Tokens:       m.totalTokens,
		ShowMoreBelow: showMoreBelow,
		FocusHint:    footerHint,
	}.Render()

	// Sidebar: highlight when in session focus mode
	sidebarData := components.SidebarData{
		SessionID: m.sessionID,
		Sessions:  m.sessions,
		Palette:   compPal,
		MarkedIDs: m.markedIDs,
		ModelName: m.currentModel,
		Focused:   m.sessionFocus,
		FocusIdx:  m.sessionFocusIdx,
	}
	sidebar := components.SidebarModel{
		Data:   sidebarData,
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
		// Authoritative: Minimal always renders Minimal, never Hybrid, even if showSidebar true.
		// This ensures three layouts are visually distinct at all times.
		content = layouts.Minimal(transcriptView, inputView, footer)
	default: // hybrid
		content = layouts.Hybrid(transcriptView, inputView, footer, sidebar, m.showSidebar)
	}

	// Help floating overlay (viewport) on top of base content
	if m.helpVisible {
		helpStyle := lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(m.palette.Border)).
			Background(lipgloss.Color(m.palette.BGElevated)).
			Width(m.width-4).
			Height(m.height-4).
			Padding(1, 1)
		helpBox := helpStyle.Render(m.helpViewport.View())
		// For deterministic TTY-free test, stack help below base with marker; real overlay would be centered.
		content = content + "\n--- help overlay ---\n" + helpBox
	}

	// Wrap with palette background
	bg := lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BG)).Foreground(lipgloss.Color(m.palette.Text)).Width(m.width).Height(m.height).Render(content)

	v := tea.NewView(bg)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	return v
}

// formatTokens formats n with thousands separators, e.g. 12431 -> "12,431".
func formatTokens(n int) string {
	if n < 0 {
		n = 0
	}
	s := fmt.Sprintf("%d", n)
	// Insert commas from right.
	if len(s) <= 3 {
		return s
	}
	var out []byte
	rem := len(s) % 3
	if rem > 0 {
		out = append(out, s[:rem]...)
		if len(s) > rem {
			out = append(out, ',')
		}
	}
	for i := rem; i < len(s); i += 3 {
		out = append(out, s[i:i+3]...)
		if i+3 < len(s) {
			out = append(out, ',')
		}
	}
	return string(out)
}



