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
}

type pendingTool struct {
	start time.Time
	index int
	name  string
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

// SetSaveFn injects a persistence hook (tests).
func (m *Model) SetSaveFn(fn func(TUIConfig) error) { m.saveFn = fn }

// SetClient injects a client (tests).
func (m *Model) SetClient(c TUIClient) { m.client = c }

// SetClock injects a clock (tests).
func (m *Model) SetClock(c Clock) { m.clock = c }

// SetEventsChannel injects the event channel (tests).
func (m *Model) SetEventsChannel(ch <-chan daemon.JSONRPCNotification) { m.eventsCh = ch }

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
		m.toast = "commands: /layout <name>, /palette <name>, /resume, /model <name>, /mark, /help"
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
func (m *Model) handleMessageDelta(payload daemon.MessageDeltaPayload) tea.Cmd {
	// Session filter like other events.
	if m.sessionID != "" && payload.SessionID != "" && payload.SessionID != m.sessionID {
		return nil
	}
	// Cross-session or stray before session creation — ignore if we have a
	// session filter mismatch already handled; also ignore empty deltas.
	if payload.Delta == "" {
		return nil
	}
	// Late/stray: if no active turn context and no streaming entry, ignore.
	// Active turn is spinner true (turn in-flight) or an existing streaming
	// entry that is still marked streaming. This prevents stray deltas after
	// swap/failure from creating phantom entries.
	if idx := m.findStreamingIndex(); idx == -1 {
		// No streaming entry — only create one if a turn is currently in-flight.
		if !m.spinner {
			return nil
		}
		// No streaming yet but turn is active: create new preview entry.
		if m.sessionID == "" {
			// Before session creation (should not happen via filter, but defensive).
			return nil
		}
		m.entries = append(m.entries, components.Entry{
			Role:      "assistant",
			Content:   payload.Delta,
			Streaming: true,
		})
		m.rebuildTranscript()
		return nil
	}
	// Append to existing streaming entry (search, don't assume last).
	idx := m.findStreamingIndex()
	if idx == -1 {
		return nil
	}
	m.entries[idx].Content += payload.Delta
	m.rebuildTranscript()
	return nil
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
		Tokens:      m.totalTokens,
	}.Render()

	sidebar := components.SidebarModel{
		Data: components.SidebarData{
			SessionID: m.sessionID,
			Sessions:  m.sessions,
			Palette:   compPal,
			MarkedIDs: m.markedIDs,
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



