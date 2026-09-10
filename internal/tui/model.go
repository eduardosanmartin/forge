package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/eduardosanmartin/forge/internal/clipboard"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/tui/components"
	"github.com/eduardosanmartin/forge/internal/version"
)

// copyText writes text to the OS clipboard. Variable (not direct call) so
// tests can substitute a fake without touching the real clipboard.
var copyText = clipboard.Write

// Layout constants — M2: single layout "Status Rail" replaces the 3-layout system.
// The three values are retained for config compatibility but View is now unified.
// ctrl+o toggles the rail; ctrl+l is an alias (documented in keys.go).
const (
	LayoutHybrid  = "hybrid"
	LayoutSession = "session"
	LayoutMinimal = "minimal"
)

// order for cycling — deprecated in M2, retained for tests compatibility.
var layoutOrder = []string{LayoutHybrid, LayoutSession, LayoutMinimal}

// M2 rail constants
const railWidth = 32 // ~250px at ~8px per cell; permanent rail width when visible

// TUI version for title bar — from internal/version.Version; fallback "dev" if empty or "0.0.0-dev".
func tuiVersion() string {
	v := version.Version
	if v == "" || v == "0.0.0-dev" {
		return "dev"
	}
	return v
}

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
	// pendingUserText snapshots the sent text so the turn's final stats
	// ("34,4s :: 1,345 tokens") land on its message at turn end even if
	// live events already swapped the local echo for the confirmed copy.
	pendingUserText string
	// lastWorkingElapsed is the last stamped elapsed second ("34,4s");
	// ticks landing inside the same displayed second skip the rebuild.
	lastWorkingElapsed string
	// lastDaemonMsg is the clock time of the last successful daemon contact
	// (event, turn result, message fetch) or turn send. The ghost-turn
	// watchdog compares against it.
	lastDaemonMsg time.Time
	helpVisible  bool
	helpViewport viewport.Model
	// slash suggestions
	suggestions        []slashSuggestion
	suggestionIdx      int
	suggestionsVisible bool

	// TUI-5: delta burst coalescing, scroll state (session focus superseded by dropdown in TUI-6)
	deltaPending bool
	rebuildCount int // instrumentation for coalescing tests; counts viewport SetContent calls

	// TUI-6: sessions dropdown (replaces inline focus mode), model selection panel, sidebar stats
	sessionsDropdownVisible bool
	sessionsDropdownIdx     int
	modelPanelVisible       bool
	modelPanelIdx           int
	modelPanelList          []string
	turnCount               int
	latencyTotalMs          int64
	latencyCount            int
	lastError               string
	plugins                 []daemon.PluginInfoResult
	skills                  []daemon.SkillInfoResult

	// Deprecated: retained for test compatibility; proxies to sessionsDropdown
	sessionFocus    bool
	sessionFocusIdx int

	// TUI-7: M2 rail, mouse capture, rail panels, sidecar duration
	mouseCapture bool   // true = MouseModeCellMotion, false = off (selection free)
	railPanel    string // "" none, "context", "plugins", "turnstats"
	// sidecar durations: persisted per-turn elapsed keyed by sessionID:seq.
	// One global map holds all sessions, so it is loaded once at startup and
	// needs no reload on session switch.
	sidecar     map[string]int64 // map[compositeKey]durationMs
	sidecarPath string           // .forge/tui-state.json
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
	{"/layout", "toggle status rail (M2 single layout; args ignored)"},
	{"/session", "open sessions panel (arrows + enter)"},
	{"/copy", "copy last response to clipboard"},
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
	PluginList() (*daemon.PluginListResult, error)
	SkillList() (*daemon.SkillListResult, error)
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
type pluginListMsg struct{ res *daemon.PluginListResult; err error }
type skillListMsg struct{ res *daemon.SkillListResult; err error }

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
		// Wheel scroll and click hotspots first: mouse capture defaults ON.
		// Free text selection is one keypress away (ctrl+m toggles capture
		// off; shift+drag also bypasses it in most terminals). The footer
		// shows "mouse off" while capture is off.
		mouseCapture: true,
		sidecar:      make(map[string]int64),
	}
	if configPath != "" {
		m.sidecarPath = filepath.Join(filepath.Dir(configPath), "tui-state.json")
	} else {
		m.sidecarPath = filepath.Join(".forge", "tui-state.json")
	}
	// Load sidecar (ignore corrupt)
	m.sidecar = loadSidecar(m.sidecarPath)
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
func (m Model) IsSessionFocus() bool            { return m.sessionFocus || m.sessionsDropdownVisible }
func (m Model) SessionFocusIdx() int {
	if m.sessionsDropdownVisible {
		return m.sessionsDropdownIdx
	}
	return m.sessionFocusIdx
}
func (m Model) SpinnerFPS() time.Duration       { return m.spinnerModel.Spinner.FPS }
func (m Model) ViewportYOffset() int            { return m.viewport.YOffset() }
func (m Model) ViewportAtBottom() bool          { return m.viewport.AtBottom() }
func (m Model) IsSessionsDropdownVisible() bool { return m.sessionsDropdownVisible }
func (m Model) SessionsDropdownIdx() int        { return m.sessionsDropdownIdx }
func (m Model) IsModelPanelVisible() bool       { return m.modelPanelVisible }
func (m Model) ModelPanelList() []string        { return m.modelPanelList }
func (m Model) ModelPanelIdx() int              { return m.modelPanelIdx }
func (m Model) TurnCount() int                  { return m.turnCount }
func (m Model) LastError() string               { return m.lastError }
func (m Model) Plugins() []daemon.PluginInfoResult { return m.plugins }
func (m Model) Skills() []daemon.SkillInfoResult   { return m.skills }
func (m Model) AvgLatencyMs() int64 {
	if m.latencyCount == 0 {
		return 0
	}
	return m.latencyTotalMs / int64(m.latencyCount)
}

// SetSaveFn injects a persistence hook (tests).
func (m *Model) SetSaveFn(fn func(TUIConfig) error) { m.saveFn = fn }

// SetClient injects a client (tests).
func (m *Model) SetClient(c TUIClient) { m.client = c }

// SetClock injects a clock (tests).
func (m *Model) SetClock(c Clock) { m.clock = c }

// SetEventsChannel injects the event channel (tests).
func (m *Model) SetEventsChannel(ch <-chan daemon.JSONRPCNotification) { m.eventsCh = ch }

func (m Model) effectiveTranscriptWidth() int {
	// M2: single rail layout. Rail width = 32 when visible, else full width.
	if m.showSidebar {
		avail := m.width - railWidth - 1 // 1 for vertical border/join
		if avail < 20 {
			avail = 20
			if m.width > railWidth+1 && avail > m.width-railWidth-1 {
				avail = m.width - railWidth - 1
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

// Accessors for TUI-7
func (m Model) IsMouseCapture() bool { return m.mouseCapture }
func (m Model) RailPanel() string    { return m.railPanel }
func (m Model) IsRailVisible() bool  { return m.showSidebar }

// SetSize sets terminal size and propagates to subcomponents.
//
// Height accounting (TUI-7 footer fix): every chrome row is MEASURED or
// explicitly reserved — no hardcoded stale footer height.
//
//	title bar     titleHeightRows (renderTitleBar bordered box: top border,
//	                content, bottom border)
//	transcript    remainder (this value)
//	separator     1
//	input         4 (+ suggestion box lines while suggestions are visible,
//	                + 1 per inline overlay hint line: sessions dropdown,
//	                model select)
//	footer        lipgloss.Height of the rendered footer (the footer content
//	                line and toast line are hard-truncated to the width so the
//	                measurement can never go stale via wrapping) + 1 headroom
//	                row while no toast is shown (a toast renders one extra line
//	                above the footer bar and can appear at runtime without a
//	                WindowSizeMsg)
//
// The animated spinner lives inside the footer bar, so no headroom row is
// reserved for it; the pending message carries the static Working marker.
//
// Overlay toggles that change the input-area height (suggestions, sessions
// dropdown, model panel) call relayout() so the footer's top position stays
// pinned to (height - footerHeight) instead of drifting off-screen. Known
// transient overflow outside this guarantee: none in the base frame; the
// floating overlays (/help, dropdown list, model panel, rail panels) are
// marker-stacked below the frame for TTY-free test determinism, matching the
// documented TUI-4..6 rendering approach.
func (m *Model) SetSize(w, h int) {
	m.width = w
	m.height = h
	footerH := m.measureFooterHeight(w)
	if m.toast == "" {
		footerH++ // toast appearance headroom (see comment above)
	}
	inputH := 4
	if m.suggestionsVisible && len(m.suggestions) > 0 {
		if sugg := m.renderSuggestions(); sugg != "" {
			inputH += lipgloss.Height(sugg) + 1 // suggestion box + join line
		}
	}
	if m.sessionsDropdownVisible || m.sessionFocus {
		inputH++ // inline "session focus" hint line above the input
	}
	if m.modelPanelVisible {
		inputH++ // inline "model select" hint line above the input
	}
	transH := h - titleHeightRows /*title*/ - 1 /*separator*/ - inputH - footerH
	if transH < 3 {
		transH = 3 // floor guard: at extreme sizes clipping is unavoidable
	}
	tw := m.effectiveTranscriptWidth()
	m.viewport.SetWidth(tw)
	m.viewport.SetHeight(transH)
	m.input.SetSize(tw, 4)
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

// relayout recomputes the chrome height budget at the current size. Called
// whenever an overlay toggles that changes the input-area height, so the
// footer top position remains pinned instead of clipping off-screen.
func (m *Model) relayout() {
	wasAtBottom := m.viewport.AtBottom()
	m.SetSize(m.width, m.height)
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

func (m Model) measureFooterHeight(w int) int {
	if w <= 0 {
		w = m.width
		if w <= 0 {
			w = 80
		}
	}
	// Build footer identical to View's footer composition for height measurement
	compPal := toCompPalette(m.palette)
	footer := components.FooterModel{
		Palette:       compPal,
		Width:         w,
		Cwd:           m.cwd,
		SessionID:     m.sessionID,
		DaemonAddr:    m.daemonAddr,
		Toast:         m.toast,
		DaemonErr:     m.daemonErr,
		ShowSpinner:   m.spinner,
		SpinnerView:   m.spinnerModel.View(),
		Layout:        m.footerLayoutLabel(),
		ModelName:     m.currentModel,
		Tokens:        m.totalTokens,
		ShowMoreBelow: false,
		FocusHint:     "",
	}.Render()
	h := lipgloss.Height(footer)
	if h < 2 {
		h = 2
	}
	return h
}

func (m Model) footerLayoutLabel() string {
	if m.showSidebar {
		return "rail on"
	}
	return "rail off"
}

// renderTitleBar renders the M2 full-width title bar: "forge <tui-version> ·
// daemon <daemon-version>" left, cwd right. Rendered as a bordered box in the
// same style as the footer bar (NormalBorder + BGElevated) so the version is
// always visible as top chrome. The box is EXACTLY 3 rows (top border,
// content, bottom border) — SetSize and handleMouseClick account for
// titleHeightRows, and the cwd is hard-truncated so the content never wraps.
func (m Model) renderTitleBar() string {
	ver := tuiVersion()
	daemonVer := m.daemonVers
	if daemonVer == "" {
		daemonVer = "dev"
	}
	left := fmt.Sprintf("forge %s · daemon %s", ver, daemonVer)
	right := m.cwd
	width := m.width
	if width <= 0 {
		width = 80
	}
	leftLen := lipgloss.Width(left)
	if gap := width - leftLen - lipgloss.Width(right) - 2; gap < 1 || lipgloss.Width(right) > width-leftLen-2 {
		// cwd does not fit next to the version block: truncate it to what remains
		avail := width - leftLen - 2
		if avail < 1 {
			avail = 1
		}
		right = ansi.Truncate(right, avail, "…")
	}
	gap := width - leftLen - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	bar := left + strings.Repeat(" ", gap) + right
	// Box Width includes the borders: cap the inner bar to the content area
	// (width-2) so it can never wrap to a second row and break the frame.
	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	bar = ansi.Truncate(bar, inner, "")
	content := lipgloss.NewStyle().
		Foreground(lipgloss.Color(m.palette.Text)).
		Render(bar)
	return lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color(m.palette.Border)).
		Background(lipgloss.Color(m.palette.BGElevated)).
		Width(width).
		Render(content)
}

// titleHeightRows is the exact rendered height of the title bar box
// (top border + content + bottom border). Keep in sync with renderTitleBar.
const titleHeightRows = 3

func (m Model) renderSeparator() string {
	w := m.width
	if w <= 0 {
		w = 80
	}
	line := strings.Repeat("─", w)
	return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render(line)
}

// --- Sidecar for elapsed duration survival (TUI-7) ---
// .forge/tui-state.json is a JSON map of composite keys "sessionID:seq" -> durationMs.
// Written on executeTurnMsg arrival, loaded on session switch/startup, merged into assistant Entry.Meta.
// Corrupt file -> ignore (return empty). No store schema changes.

func sidecarKey(sessionID string, seq int) string {
	return fmt.Sprintf("%s:%d", sessionID, seq)
}

func loadSidecar(path string) map[string]int64 {
	out := make(map[string]int64)
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return out
	}
	var raw map[string]int64
	if err := json.Unmarshal(data, &raw); err != nil {
		return out
	}
	return raw
}

func saveSidecar(path string, m map[string]int64) error {
	if path == "" {
		return nil
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, "tui-state-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// Init returns initial commands: fetch status and sessions, plus the input
// cursor blink. The blink Cmd must be returned here: NewInput focuses the
// textarea but its Focus Cmd was dropped, so without this the cursor stays
// solid until the first keystroke restarts blinking.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.cmdStatus(), m.cmdListSessions(), m.input.Focus())
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

func (m Model) cmdRefreshPlugins() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.PluginList()
		return pluginListMsg{res: res, err: err}
	}
}

func (m Model) cmdRefreshSkills() tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		res, err := m.client.SkillList()
		return skillListMsg{res: res, err: err}
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
		// Architectural fix TUI-6: spinner ticks are DYNAMIC (footer + pending line compose at View time)
		// and must NOT rebuild the static transcript (O(entries × width) wrapping).
		// This prevents stutter where burst deltas queue behind tick rebuilds.
		if !m.spinner {
			return m, nil
		}
		if msg.ID != 0 && msg.ID != m.spinnerModel.ID() {
			return m, nil
		}
		var scmd tea.Cmd
		m.spinnerModel, scmd = m.spinnerModel.Update(msg)
		if m.spinner {
			// Single tick driver: bubbles re-arms itself with an
			// incremented tag (stale ticks rejected on ID+tag). Appending
			// a second re-arm here forked successors exponentially —
			// GBs of queued ticks and burned cores on long turns.
			cmds = append(cmds, scmd)
			// In-bubble elapsed: rebuild at most once per displayed second.
			if m.stampWorkingElapsed() {
				m.rebuildTranscript()
			}
			// Ghost-turn watchdog: silent turns recover visibly.
			if wcmd := m.checkWatchdog(); wcmd != nil {
				cmds = append(cmds, wcmd)
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

	case tea.MouseClickMsg:
		m.handleMouseClick(tea.Mouse(msg))
		return m, nil

	case tea.WindowSizeMsg:
		m.SetSize(msg.Width, msg.Height)
		m.rebuildTranscript()
		return m, nil

	case eventsSubscribedMsg:
		m.eventsCh = msg.ch
		return m, m.cmdWaitEvent(m.eventsCh)

	case daemonEventMsg:
		// Any daemon traffic resets the ghost-turn watchdog clock.
		m.touchDaemon()
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
			// Track turn stats even on failure
			m.turnCount++
			if !m.turnStart.IsZero() && m.clock != nil {
				elapsed := m.clock.Now().Sub(m.turnStart)
				if elapsed >= 0 {
					m.latencyTotalMs += elapsed.Milliseconds()
					m.latencyCount++
				}
			}
		m.lastError = truncateError(msg.err.Error(), 80)
		// Turn ended in error: keep the elapsed on the pending message
		// (no token count is known); without a clock just clear the marker.
		meta := ""
		if !m.turnStart.IsZero() && m.clock != nil {
			meta = formatWorkingElapsed(m.clock.Now().Sub(m.turnStart))
		}
		if m.finalizeWorkingMarkers(meta) {
			m.rebuildTranscript()
		}
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
			// A completed turn result is daemon contact (resets watchdog).
			m.touchDaemon()
			// Update current model (documented source: ExecuteTurnResult.Model when present).
			if msg.res.Model != "" {
				m.currentModel = msg.res.Model
			}
			// Compute CLIENT-side elapsed from send to arrival using injected Clock.
			elapsedStr := ""
			var durationMs int64 = -1
			if !m.turnStart.IsZero() && m.clock != nil {
				elapsed := m.clock.Now().Sub(m.turnStart)
				if elapsed < 0 {
					elapsed = 0
				}
				durationMs = elapsed.Milliseconds()
				elapsedStr = formatDurationMs(durationMs)
			}
			// Turn stats: count and latency
			m.turnCount++
			if durationMs >= 0 {
				m.latencyTotalMs += durationMs
				m.latencyCount++
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
		// Turn summary on the FINAL assistant entry: "tokens T · model ·
		// elapsed" in dim. Intermediate entries keep their per-call meta;
		// the last reply carries the canonical turn totals.
		turnTokens := 0
		if msg.res.Usage != nil {
			turnTokens = msg.res.Usage.TotalTokens
		} else {
			for _, mr := range msg.res.Messages {
				if mr.Usage != nil {
					turnTokens += mr.Usage.TotalTokens
				}
			}
		}
		summaryParts := []string{}
		if turnTokens > 0 {
			summaryParts = append(summaryParts, fmt.Sprintf("tokens %d", turnTokens))
		}
		if m.currentModel != "" {
			summaryParts = append(summaryParts, m.currentModel)
		}
		if elapsedStr != "" {
			summaryParts = append(summaryParts, elapsedStr)
		}
		if len(summaryParts) > 0 {
			summary := strings.Join(summaryParts, " · ")
			for i := len(newEntries) - 1; i >= 0; i-- {
				if newEntries[i].Role == "assistant" && !newEntries[i].IsTool {
					newEntries[i].Meta = summary
					newEntries[i].Summary = true
					break
				}
			}
		}
			// Sidecar persistence for elapsed survival (TUI-7): persist per-turn duration keyed by sessionID:seq
			// Documented sidecar .forge/tui-state.json survives reloads; corrupt file ignored.
			if durationMs >= 0 && m.sessionID != "" {
				for _, e := range newEntries {
					if e.Role == "assistant" && !e.IsTool && e.Seq != 0 {
						key := sidecarKey(m.sessionID, e.Seq)
						m.sidecar[key] = durationMs
						break // only first assistant per turn (matches elapsed injection)
					}
				}
				_ = saveSidecar(m.sidecarPath, m.sidecar)
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
			// Turn completed: stamp "elapsed :: tokens" onto its user
			// message (the echo was replaced by its confirmed copy above;
			// the stats survive on whichever copy remains).
			stats := ""
			if durationMs >= 0 {
				stats = formatWorkingElapsed(time.Duration(durationMs) * time.Millisecond)
				turnTokens := 0
				if msg.res.Usage != nil {
					turnTokens = msg.res.Usage.TotalTokens
				} else {
					for _, mr := range msg.res.Messages {
						if mr.Usage != nil {
							turnTokens += mr.Usage.TotalTokens
						}
					}
				}
				if turnTokens > 0 {
					stats += " :: " + formatTokens(turnTokens) + " tokens"
				}
			}
			m.finalizeWorkingMarkers(stats)
			m.rebuildTranscript()
		}
		return m, nil

	case messagesSinceMsg:
		if msg.err != nil {
			m.toast = msg.err.Error()
			return m, nil
		}
		// Fresh daemon contact (resets watchdog even when nothing is new).
		m.touchDaemon()
		// Append only messages newer than what we already rendered (dedup guard).
		var fresh []daemon.MessageResult
		for _, mr := range msg.res.Messages {
			if mr.Seq > m.lastSeq {
				fresh = append(fresh, mr)
			}
		}
		if len(fresh) > 0 {
			newEntries := components.EntriesFromMessages(fresh, nil)
			// Merge sidecar durations into reloaded entries (TUI-7): elapsed
			// survives restarts/session switches via .forge/tui-state.json.
			newEntries = mergeSidecarDurations(newEntries, m.sidecar, m.sessionID, m.currentModel)
			// Absorb the local echo when its confirmed copy arrives here
			// (watchdog refetch or live catch-up): same content-match rule
			// as the turn-end merge, so no duplicate user bubble lingers.
			for _, e := range newEntries {
				if e.Role == "user" {
					m.absorbLocalEcho(e)
				}
			}
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
		// Halt ends the turn: keep the elapsed on the pending message if
		// the echo survived (no token count is known on halt).
		meta := ""
		if !m.turnStart.IsZero() && m.clock != nil {
			meta = formatWorkingElapsed(m.clock.Now().Sub(m.turnStart))
		}
		if m.finalizeWorkingMarkers(meta) {
			m.rebuildTranscript()
		}
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
			m.lastError = truncateError(msg.err.Error(), 80)
		} else {
			m.toast = fmt.Sprintf("model → %s", msg.model)
			m.currentModel = msg.model
		}
		return m, nil

	case markSuccessResultMsg:
		if msg.err != nil {
			m.toast = msg.err.Error()
			m.lastError = truncateError(msg.err.Error(), 80)
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

	case pluginListMsg:
		if msg.err != nil {
			m.toast = msg.err.Error()
			m.lastError = truncateError(msg.err.Error(), 80)
		} else if msg.res != nil {
			m.plugins = msg.res.Plugins
		}
		return m, nil

	case skillListMsg:
		if msg.err != nil {
			m.toast = msg.err.Error()
			m.lastError = truncateError(msg.err.Error(), 80)
		} else if msg.res != nil {
			m.skills = msg.res.Skills
		}
		return m, nil

	case tea.KeyPressMsg:
		// Rail panel intercepts: esc closes it, any panel overlay closes on esc
		if m.railPanel != "" {
			if msg.String() == "esc" {
				m.railPanel = ""
				return m, nil
			}
			// Any other key when rail panel visible? Let global handle but esc is priority.
			// For simplicity, allow esc only; other keys close as well? Spec: Esc closes any panel.
			// Keep rail panel until esc or toggle.
		}
		// Help overlay intercepts everything: esc or any key closes it.
		if m.helpVisible {
			m.helpVisible = false
			return m, nil
		}
		// Sessions dropdown (TUI-6) intercepts BEFORE suggestions and globals — floating overlay like /help
		if m.sessionsDropdownVisible || m.sessionFocus {
			switch msg.String() {
			case "up":
				if len(m.sessions) > 0 {
					if m.sessionsDropdownVisible {
						m.sessionsDropdownIdx--
						if m.sessionsDropdownIdx < 0 {
							m.sessionsDropdownIdx = len(m.sessions) - 1
						}
					} else {
						m.sessionFocusIdx--
						if m.sessionFocusIdx < 0 {
							m.sessionFocusIdx = len(m.sessions) - 1
						}
					}
					// Mirror for compat
					m.sessionFocusIdx = m.sessionsDropdownIdx
					if m.sessionsDropdownVisible {
						m.sessionFocus = true
					}
				}
				return m, nil
			case "down":
				if len(m.sessions) > 0 {
					if m.sessionsDropdownVisible {
						m.sessionsDropdownIdx = (m.sessionsDropdownIdx + 1) % len(m.sessions)
					} else {
						m.sessionFocusIdx = (m.sessionFocusIdx + 1) % len(m.sessions)
						m.sessionsDropdownIdx = m.sessionFocusIdx
					}
					m.sessionFocusIdx = m.sessionsDropdownIdx
				}
				return m, nil
			case "enter":
				// Prefer dropdown idx when dropdown visible, else legacy idx
				idx := m.sessionsDropdownIdx
				if !m.sessionsDropdownVisible {
					idx = m.sessionFocusIdx
				}
				if len(m.sessions) > 0 && idx >= 0 && idx < len(m.sessions) {
					sel := m.sessions[idx]
				m.sessionID = sel.ID
				m.lastSeq = 0
				m.entries = nil
				m.pendingUserText = ""
				m.rebuildTranscriptForceBottom()
					m.toast = fmt.Sprintf("session → %s", sel.ID[:8])
					m.suggestionsVisible = false
					m.sessionsDropdownVisible = false
					m.sessionFocus = false
					m.input.Focus()
					m.relayout() // focus hint row leaves the input area
					// Refresh plugins/skills on session switch if cheap (async)
					var cmdsSwitch []tea.Cmd
					if m.client != nil {
						cmdsSwitch = append(cmdsSwitch, m.cmdGetMessagesSince(0))
						if plCmd := m.cmdRefreshPlugins(); plCmd != nil {
							cmdsSwitch = append(cmdsSwitch, plCmd)
						}
						if skCmd := m.cmdRefreshSkills(); skCmd != nil {
							cmdsSwitch = append(cmdsSwitch, skCmd)
						}
					}
					if len(cmdsSwitch) > 0 {
						return m, tea.Batch(cmdsSwitch...)
					}
					return m, nil
				}
				m.sessionsDropdownVisible = false
				m.sessionFocus = false
				m.input.Focus()
				m.relayout()
				return m, nil
			case "esc":
				m.sessionsDropdownVisible = false
				m.sessionFocus = false
				m.input.Focus()
				m.relayout()
				return m, nil
			default:
				if key.Matches(msg, m.keyMap.GrabSession) {
					m.sessionsDropdownVisible = false
					m.sessionFocus = false
					m.input.Focus()
					m.relayout()
					return m, nil
				}
				// Any other key consumed, not sent to input
				return m, nil
			}
		}
		// Model selection panel intercepts before suggestions
		if m.modelPanelVisible {
			switch msg.String() {
			case "up":
				if len(m.modelPanelList) > 0 {
					m.modelPanelIdx--
					if m.modelPanelIdx < 0 {
						m.modelPanelIdx = len(m.modelPanelList) - 1
					}
				}
				return m, nil
			case "down":
				if len(m.modelPanelList) > 0 {
					m.modelPanelIdx = (m.modelPanelIdx + 1) % len(m.modelPanelList)
				}
				return m, nil
			case "enter":
				if len(m.modelPanelList) > 0 && m.modelPanelIdx >= 0 && m.modelPanelIdx < len(m.modelPanelList) {
					chosen := m.modelPanelList[m.modelPanelIdx]
					m.modelPanelVisible = false
					m.relayout() // model select hint row leaves the input area
					if m.sessionID == "" {
						m.toast = "no session"
						return m, nil
					}
					if m.client == nil {
						m.toast = "not connected"
						return m, nil
					}
					return m, m.cmdSwitchModel(chosen)
				}
				m.modelPanelVisible = false
				m.relayout()
				return m, nil
			case "esc":
				m.modelPanelVisible = false
				m.relayout()
				return m, nil
			default:
				return m, nil
			}
		}
		// Suggestion navigation intercepts before global keys.
		// TUI-7 fix: Enter only completes when completion would actually
		// CHANGE the input. When the input already equals the highlighted
		// suggestion — exactly ("/model") or with a trailing space
		// ("/model ") — Enter dismisses the suggestions and falls through
		// to the global send path instead of completing into a no-op.
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
				m.dismissSuggestions()
				return m, nil
			case "enter":
				if sel, ok := m.highlightedSuggestion(); ok {
					cur := m.input.Value()
					if strings.TrimSpace(cur) == sel.Command || cur == sel.Command+" " {
						m.dismissSuggestions()
						m.relayout() // suggestion box rows leave the input area
						break // fall through to global enter handling (send)
					}
				}
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
			if err := m.persistConfig(); err != nil {
				m.toast = err.Error()
			}
			return m, nil
		case key.Matches(msg, m.keyMap.CycleLayout):
			m.cycleLayout()
			// persist
			if err := m.persistConfig(); err != nil {
				m.toast = err.Error()
			}
			return m, nil
		case key.Matches(msg, m.keyMap.ToggleMouse):
			m.mouseCapture = !m.mouseCapture
			if m.mouseCapture {
				m.toast = "mouse capture on (text selection via shift+drag)"
			} else {
				m.toast = "mouse capture off (text selection free)"
			}
			return m, nil
		case key.Matches(msg, m.keyMap.ShowContext):
			if m.railPanel == "context" {
				m.railPanel = ""
			} else {
				m.railPanel = "context"
			}
			return m, nil
		case key.Matches(msg, m.keyMap.ShowPlugins):
			if m.railPanel == "plugins" {
				m.railPanel = ""
			} else {
				m.railPanel = "plugins"
			}
			return m, nil
		case key.Matches(msg, m.keyMap.ShowTurnStats):
			if m.railPanel == "turnstats" {
				m.railPanel = ""
			} else {
				m.railPanel = "turnstats"
			}
			return m, nil
		case key.Matches(msg, m.keyMap.GrabSession):
			// TUI-6: ctrl+g opens sessions dropdown (floating overlay like /help)
			// Position: floating overlay stacked with marker for TTY-free determinism.
			// Replaces TUI-5 inline focus mode (superseded).
			if len(m.sessions) == 0 {
				m.toast = "no sessions"
				return m, nil
			}
			m.sessionsDropdownVisible = true
			m.sessionFocus = true // compat
			idx := 0
			for i, s := range m.sessions {
				if s.ID == m.sessionID {
					idx = i
					break
				}
			}
			m.sessionsDropdownIdx = idx
			m.sessionFocusIdx = idx
			m.input.Blur()
			m.relayout() // inline focus hint adds a row above the input
			// Refresh plugins/skills if cheap (async, optional)
			var refreshCmds []tea.Cmd
			if pc := m.cmdRefreshPlugins(); pc != nil {
				refreshCmds = append(refreshCmds, pc)
			}
			if sc := m.cmdRefreshSkills(); sc != nil {
				refreshCmds = append(refreshCmds, sc)
			}
			if len(refreshCmds) > 0 {
				return m, tea.Batch(refreshCmds...)
			}
			return m, nil
		case msg.String() == "esc":
			// Esc priority is handled earlier in the chain: rail panel (top
			// of KeyPressMsg), help overlay, sessions dropdown, model panel,
			// suggestions. Reaching here means nothing was open.
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
				// Close suggestions on send (normally already dismissed by the
				// suggestion intercept's exact-match path; safety net).
				if m.suggestionsVisible {
					m.dismissSuggestions()
					m.relayout()
				}
				text := strings.TrimSpace(m.input.Value())
				if text == "" {
					return m, nil
				}
				// Slash command parsing.
				if strings.HasPrefix(text, "/") {
					if done, cmd := m.handleSlash(text); done {
						m.input.Reset()
						m.dismissSuggestions()
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
			// The echo carries the Working marker so the pending message is
			// visibly marked for the whole turn (finalized on turn end).
			m.entries = append(m.entries, components.Entry{Role: "user", Content: text, Local: true, Meta: workingMarker})
			m.pendingUserText = text
			m.lastWorkingElapsed = ""
				m.rebuildTranscriptForceBottom()
				m.input.Reset()
				m.suggestionsVisible = false
				m.spinner = true
				if m.clock == nil {
					m.clock = realClock{}
				}
			m.turnStart = m.clock.Now()
			m.touchDaemon()
			m.toast = ""
			// Animated spinner: single driver. tickImmediate primes the
			// first frame; bubbles re-arms itself per tick (tag-guarded),
			// so no second re-arm here (that forked exponentially).
			tickImmediate := func() tea.Msg { return m.spinnerModel.Tick() }
			return m, tea.Batch(m.cmdExecuteTurn(text), tickImmediate)
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
	// M2 Status Rail: ctrl+o toggles the rail. Hidden = full-width transcript (M1-style).
	// showSidebar now means rail visible.
	m.showSidebar = !m.showSidebar
	m.config.Sidebar = m.showSidebar
	if m.showSidebar {
		m.toast = "rail on"
	} else {
		m.toast = "rail off"
	}
	// relayout recomputes effective width (rail steals 33 cols from the
	// transcript) and re-measures the footer at the current size.
	m.relayout()
	m.rebuildTranscript()
}

func (m *Model) cycleLayout() {
	// M2: ctrl+l is an alias of ctrl+o (rail toggle). The 3-layout cycle is
	// removed; the footer layout label reports "rail on"/"rail off".
	m.toggleSidebar()
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
		// M2: the 3-layout system is gone — /layout is a keyboard alias for
		// the rail toggle (same as ctrl+o / ctrl+l). Arguments are accepted
		// and ignored so stale muscle memory ("/layout minimal") still does
		// something sensible instead of failing.
		m.toggleSidebar()
		if err := m.persistConfig(); err != nil {
			m.toast = err.Error()
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
	case "/session":
		// Opens the floating sessions panel (same UX as /model): arrows
		// navigate, enter switches, esc closes. ctrl+g only enters sidebar
		// focus mode (no visible list) — this command is the discoverable
		// path to the panel. Closes the model panel: exclusive.
		m.modelPanelVisible = false
		if len(m.sessions) == 0 {
			m.toast = "no sessions"
			return true, nil
		}
		m.sessionsDropdownVisible = true
		m.sessionFocus = true
		idx := 0
		for i, s := range m.sessions {
			if s.ID == m.sessionID {
				idx = i
				break
			}
		}
		m.sessionsDropdownIdx = idx
		m.sessionFocusIdx = idx
		m.input.Blur()
		m.relayout()
		return true, nil
	case "/copy":
		// Copies the last assistant response to the OS clipboard (local
		// operation: no client/session needed).
		m.copyLastResponse()
		return true, nil
	case "/model":
		if len(parts) < 2 {
			// TUI-6: /model with no argument opens floating selection panel listing available models
			// Model list source: READ .forge/config.json providers.*.models directly
			// (the TUI already reads that file for the tui section — reuse same read) + current model first
			// Documented limitation: plugin-providers are not listed yet (deferred).
			list := m.loadAvailableModels()
			if len(list) == 0 {
				m.toast = "usage: /model <name> — no models configured"
				return true, nil
			}
			m.modelPanelList = list
			m.modelPanelIdx = 0
			m.modelPanelVisible = true
			m.relayout() // model select hint adds a row above the input
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

// lastAssistantText returns the latest assistant reply body for /copy and
// the footer [copiar] hotspot. Skips tool entries, streaming previews,
// blanks and "(empty)"/"(tool calls)" placeholders (copying those is noise).
func (m Model) lastAssistantText() string {
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		if e.Role != "assistant" || e.IsTool || e.Streaming {
			continue
		}
		if text := strings.TrimSpace(e.Content); text != "" && text != "(empty)" && text != "(tool calls)" {
			return e.Content
		}
	}
	return ""
}

// copyLastResponse copies the last assistant reply to the OS clipboard,
// reporting the outcome as a toast.
func (m *Model) copyLastResponse() {
	text := m.lastAssistantText()
	if text == "" {
		m.toast = "no response to copy yet"
		return
	}
	if err := copyText(text); err != nil {
		m.toast = "copy failed: " + truncateError(err.Error(), 80)
		return
	}
	m.toast = fmt.Sprintf("copied %d chars", len([]rune(text)))
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

// workingMarker is the static in-transcript marker shown under the user's
// pending message for the whole turn ("◌ Working…"). Unlike the animated
// pending spinner line below the transcript (dynamic, composed at View time),
// the marker lives in the entry Meta so it needs no per-tick rebuild and is
// visible even while streaming (caret phase) or mid-turn tool iterations.
// Set on send, cleared on turn end (executeTurnMsg success/error, halt).
const workingMarker = "◌ Working…"

// finalizeWorkingMarkers stamps the turn's final stats ("34,4s :: 1,345
// tokens", or just elapsed when no token count is known) onto its user
// message: the LAST entry matching the sent text (confirmed copy wins over
// the local echo) or carrying the Working marker. Single match keeps
// duplicate echo+confirmed pairs from showing the line twice; any other
// leftover marker is cleared. An empty meta simply clears the marker.
func (m *Model) finalizeWorkingMarkers(meta string) bool {
	target := -1
	for i := len(m.entries) - 1; i >= 0; i-- {
		e := m.entries[i]
		if e.Role != "user" {
			continue
		}
		// Match the bare marker or a tick-stamped one ("◌ Working… (34,4s)").
		if strings.HasPrefix(e.Meta, workingMarker) || (m.pendingUserText != "" && strings.TrimSpace(e.Content) == m.pendingUserText) {
			target = i
			break
		}
	}
	changed := false
	for i := range m.entries {
		if m.entries[i].Role != "user" {
			continue
		}
		if i == target {
			if m.entries[i].Meta != meta {
				m.entries[i].Meta = meta
				changed = true
			}
		} else if strings.HasPrefix(m.entries[i].Meta, workingMarker) {
			m.entries[i].Meta = ""
			changed = true
		}
	}
	m.pendingUserText = ""
	m.lastWorkingElapsed = ""
	return changed
}

// formatWorkingElapsed renders a duration for the Working marker:
// "0,3s", "34,4s" (comma decimal, owner's locale), "2m05s" past a minute.
func formatWorkingElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return strings.Replace(fmt.Sprintf("%.1f", d.Seconds()), ".", ",", 1) + "s"
}

// touchDaemon records successful daemon contact for the ghost-turn
// watchdog. No clock (tests constructing Model bare) means no tracking.
func (m *Model) touchDaemon() {
	if m.clock == nil {
		return
	}
	m.lastDaemonMsg = m.clock.Now()
}

// watchdogSilence bounds silent turns: spinner on, no tool running, and no
// daemon traffic for this long means the turn is a ghost (finished or dead
// daemon-side, lost in delivery). Kept comfortably below the daemon-side
// turn timeout so the UI recovers first with a visible halt + refetch.
const watchdogSilence = 240 * time.Second

// checkWatchdog halts a ghost turn visibly and refetches anything missed.
// Returns nil unless all hold: spinner on, no outstanding tool call (a
// running tool legitimately goes silent), tracked clock, and silence past
// the bound. Long tool runs are never halted by this path; the daemon-side
// turn timeout owns execution hangs.
func (m *Model) checkWatchdog() tea.Cmd {
	if !m.spinner || len(m.pendingTools) > 0 {
		return nil
	}
	if m.turnStart.IsZero() || m.clock == nil || m.lastDaemonMsg.IsZero() {
		return nil
	}
	if m.clock.Now().Sub(m.lastDaemonMsg) < watchdogSilence {
		return nil
	}
	m.toast = "no daemon activity for 4m — halting ghost turn"
	return tea.Batch(m.cmdHalt(), m.cmdGetMessagesSince(m.lastSeq))
}

// stampWorkingElapsed refreshes the in-bubble Working elapsed ("◌ Working…
// (34,4s)") at most once per displayed second. The entry Meta is
// width-wrapped at build time, so the bubble box never breaks. Returns true
// if the transcript changed (caller rebuilds). Ticks that land inside the
// same displayed second are no-ops, keeping the TUI-6 no-rebuild-per-tick
// invariant for steady state.
func (m *Model) stampWorkingElapsed() bool {
	if m.turnStart.IsZero() || m.clock == nil {
		return false
	}
	s := formatWorkingElapsed(m.clock.Now().Sub(m.turnStart))
	if s == m.lastWorkingElapsed {
		return false
	}
	m.lastWorkingElapsed = s
	stamped := false
	for i := range m.entries {
		if m.entries[i].Role == "user" && strings.HasPrefix(m.entries[i].Meta, workingMarker) {
			m.entries[i].Meta = workingMarker + " (" + s + ")"
			stamped = true
		}
	}
	return stamped
}

func (m *Model) rebuildTranscriptInternal(forceBottom bool) {
	wasAtBottom := m.viewport.AtBottom()
	w := m.effectiveTranscriptWidth()
	content := components.BuildContent(m.entries, toCompPalette(m.palette), w)
	m.viewport.SetContent(content)
	m.rebuildCount++
	if forceBottom || wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// ---- TUI-4 helpers ----

func (m *Model) updateSuggestions() {
	val := m.input.Value()
	wasVisible := m.suggestionsVisible
	wasCount := len(m.suggestions)
	if !strings.HasPrefix(val, "/") {
		m.dismissSuggestions()
		if wasVisible {
			m.relayout() // suggestion box rows leave the input area
		}
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
		m.dismissSuggestions()
		if wasVisible {
			m.relayout()
		}
		return
	}
	m.suggestions = filtered
	m.suggestionsVisible = true
	if m.suggestionIdx >= len(filtered) {
		m.suggestionIdx = 0
	}
	if !wasVisible || len(filtered) != wasCount {
		m.relayout() // suggestion box rows enter/change the input area
	}
}

// highlightedSuggestion returns the currently highlighted suggestion, if any.
func (m Model) highlightedSuggestion() (slashSuggestion, bool) {
	if !m.suggestionsVisible || len(m.suggestions) == 0 {
		return slashSuggestion{}, false
	}
	if m.suggestionIdx < 0 || m.suggestionIdx >= len(m.suggestions) {
		return slashSuggestion{}, false
	}
	return m.suggestions[m.suggestionIdx], true
}

// dismissSuggestions hides the suggestion list without completing.
func (m *Model) dismissSuggestions() {
	m.suggestionsVisible = false
	m.suggestions = nil
	m.suggestionIdx = 0
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
	m.dismissSuggestions()
	m.relayout() // suggestion box rows leave the input area
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
	sb.WriteString(dimStyle.Render("  / prefix      slash suggestions: ↑/↓ navigate, tab/enter complete, esc dismiss; enter sends when input equals suggestion"))
	sb.WriteString("\n\n")
	sb.WriteString(textStyle.Render("M2 Status Rail:"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  ctrl+o / ctrl+l  toggle rail (full-width when hidden)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  ctrl+1        Context & tokens panel"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  ctrl+2        Plugins & skills panel (enable/disable via forge plugin/skill CLI)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  ctrl+3        Turn stats panel (avg latency, last 5 errors)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  click rail card  open detail panel; esc closes"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  click session id in footer  open sessions dropdown; click model name  open /model panel"))
	sb.WriteString("\n\n")
	sb.WriteString(textStyle.Render("Mouse & Selection:"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  mouse capture on by default (cell motion for wheel/click)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  shift+drag    bypass capture for text selection (Windows Terminal honors it)"))
	sb.WriteString("\n")
	sb.WriteString(dimStyle.Render("  ctrl+m        toggle mouse capture on/off (footer toast)"))
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
	m.pendingUserText = ""
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
	m.pendingUserText = ""
	m.rebuildTranscriptForceBottom()
	m.toast = fmt.Sprintf("session → %s", sel.ID[:8])
	m.suggestionsVisible = false
	return true, m.cmdGetMessagesSince(0)
}

// View renders M2 "Status Rail" layout: title bar (full-width, top) +
// transcript (left) + rail (right, 32 cols, three cards) + accent separator
// (full-width) above the input + input + footer.
//
// Mouse hit-testing (TUI-7): zones are derived in handleMouseClick from the
// SAME constants this function renders with (title row 1, viewport height,
// input 4, measured footer height). Documented fragility: rail card zones are
// equal thirds of the rail column and footer hotspots are fixed right-edge
// bands, so both drift if card content or footer composition changes; the
// pure zone functions (HitTestRail, HitTestFooter) are covered by tests.
func (m Model) View() tea.View {
	// Title bar (full-width, top)
	titleBar := m.renderTitleBar()
	// Transcript as rendered (the animated spinner lives only in the
	// footer bar; the pending message itself carries the Working marker
	// with its tick-stamped elapsed, wrapped safely inside its bubble).
	transcriptView := m.viewport.View()
	inputView := m.input.View()
	if m.suggestionsVisible {
		suggView := m.renderSuggestions()
		if suggView != "" {
			inputView = suggView + "\n" + inputView
		}
	}
	if m.sessionsDropdownVisible {
		focusHint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render("▸ session focus: ↑/↓ navigate · enter select · esc/ctrl+g close")
		inputView = focusHint + "\n" + inputView
	} else if m.modelPanelVisible {
		mpHint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render("▸ model select: ↑/↓ navigate · enter select · esc close")
		inputView = mpHint + "\n" + inputView
	} else if m.sessionFocus {
		focusHint := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render("▸ session focus: ↑/↓ navigate · enter switch · esc/ctrl+g back")
		inputView = focusHint + "\n" + inputView
	}
	compPal := toCompPalette(m.palette)
	showMoreBelow := false
	if len(m.entries) > 0 && !m.viewport.AtBottom() {
		showMoreBelow = true
	}
	footerHint := ""
	if m.sessionsDropdownVisible || m.sessionFocus {
		footerHint = "session focus"
	} else if m.modelPanelVisible {
		footerHint = "model select"
	} else if m.railPanel != "" {
		footerHint = m.railPanel + " panel"
	}
	if m.railPanel != "" && footerHint == "" {
		footerHint = m.railPanel
	}
	if !m.mouseCapture {
		if footerHint != "" {
			footerHint += " · mouse off"
		} else {
			footerHint = "mouse off"
		}
	}
	footer := components.FooterModel{
		Palette:       compPal,
		Width:         m.width,
		Cwd:           m.cwd,
		SessionID:     m.sessionID,
		DaemonAddr:    m.daemonAddr,
		Toast:         m.toast,
		DaemonErr:     m.daemonErr,
		ShowSpinner:   m.spinner,
		SpinnerView:   m.spinnerModel.View(),
		Layout:        m.footerLayoutLabel(),
		ModelName:     m.currentModel,
		Tokens:        m.totalTokens,
		ShowMoreBelow: showMoreBelow,
		FocusHint:     footerHint,
	}.Render()
	// Separator above input (accent)
	separator := m.renderSeparator()
	// Sidebar / rail data (M2: same three cards as TUI-6 sidebar, now in permanent rail)
	avgLatencyStr := ""
	if m.latencyCount > 0 {
		avgMs := m.latencyTotalMs / int64(m.latencyCount)
		if avgMs < 1000 {
			avgLatencyStr = fmt.Sprintf("%dms", avgMs)
		} else {
			avgLatencyStr = fmt.Sprintf("%.1fs", float64(avgMs)/1000)
		}
	}
	turnsInWindow := len(m.entries)
	sidebarData := components.SidebarData{
		Palette:       compPal,
		TotalTokens:   m.totalTokens,
		TurnsInWindow: turnsInWindow,
		TurnCount:     m.turnCount,
		AvgLatency:    avgLatencyStr,
		LastError:     m.lastError,
		Plugins:       m.plugins,
		Skills:        m.skills,
	}
	railHeight := m.viewport.Height()
	if railHeight < 5 {
		railHeight = 5
	}
	sidebar := components.SidebarModel{
		Data:   sidebarData,
		Width:  railWidth,
		Height: railHeight,
	}
	// Main row: transcript + optional rail. RenderColumnCapped hard-caps the
	// rail box to EXACTLY railHeight rows (lipgloss Height is only a minimum,
	// so uncapped card content would overflow and clip the footer).
	var mainRow string
	if m.showSidebar {
		railView := sidebar.RenderColumnCapped()
		mainRow = lipgloss.JoinHorizontal(lipgloss.Top, transcriptView, railView)
	} else {
		mainRow = transcriptView
	}
	content := strings.Join([]string{titleBar, mainRow, separator, inputView, footer}, "\n")
	// Floating overlays render OVER the bottom rows of the main row so they
	// stay on screen: anything appended below the frame is cut off live and
	// invisible. The frame keeps exactly terminal height; the overlay covers
	// transcript rows (keyboard-driven; esc closes).
	maxOverlayRows := len(strings.Split(mainRow, "\n"))
	if maxOverlayRows < 3 {
		maxOverlayRows = 3
	}
	float := func(box string) {
		mainRow = floatOverlay(mainRow, box, maxOverlayRows)
		content = strings.Join([]string{titleBar, mainRow, separator, inputView, footer}, "\n")
	}
	if m.helpVisible {
		helpLines := strings.Split(m.helpViewport.View(), "\n")
		helpCap := maxOverlayRows - 4 // borders + padding
		if helpCap < 1 {
			helpCap = 1
		}
		if len(helpLines) > helpCap {
			helpLines = helpLines[:helpCap]
		}
		helpStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(m.palette.Border)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(1, 1)
		float(helpStyle.Render(strings.Join(helpLines, "\n")))
	}
	if m.sessionsDropdownVisible {
		dropdown := m.renderSessionsDropdown(maxOverlayRows)
		boxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(m.palette.Accent)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(0, 1)
		float(boxStyle.Render(dropdown))
	}
	if m.modelPanelVisible {
		panel := m.renderModelPanel(maxOverlayRows)
		boxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(m.palette.Accent)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(0, 1)
		float(boxStyle.Render(panel))
	}
	if m.railPanel != "" {
		var panel string
		switch m.railPanel {
		case "context":
			panel = m.renderContextPanel()
		case "plugins":
			panel = m.renderPluginsPanel()
		case "turnstats":
			panel = m.renderTurnStatsPanel()
		}
		boxStyle := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(m.palette.Accent)).Background(lipgloss.Color(m.palette.BGElevated)).Width(m.width).Padding(0, 1)
		float(boxStyle.Render(capLines(panel, maxOverlayRows-2)))
	}
	bg := lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BG)).Foreground(lipgloss.Color(m.palette.Text)).Width(m.width).Height(m.height).Render(content)
	v := tea.NewView(bg)
	v.AltScreen = true
	if m.mouseCapture {
		v.MouseMode = tea.MouseModeCellMotion
	} else {
		v.MouseMode = 0
	}
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

func truncateError(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

// loadAvailableModels reads .forge/config.json providers.*.models directly.
// The TUI already reads that file for the tui section — reuse the same read path.
// Returns current model first, then unique models from all providers. Plugin-providers are not listed yet (deferred).
func (m Model) loadAvailableModels() []string {
	path := m.configPath
	if path == "" {
		path = ".forge/config.json"
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		if m.currentModel != "" {
			return []string{m.currentModel}
		}
		return nil
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		if m.currentModel != "" {
			return []string{m.currentModel}
		}
		return nil
	}
	rawProv, ok := doc["providers"]
	if !ok {
		if m.currentModel != "" {
			return []string{m.currentModel}
		}
		return nil
	}
	var providers map[string]struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rawProv, &providers); err != nil {
		if m.currentModel != "" {
			return []string{m.currentModel}
		}
		return nil
	}
	seen := map[string]bool{}
	var out []string
	if m.currentModel != "" {
		out = append(out, m.currentModel)
		seen[m.currentModel] = true
	}
	for _, p := range providers {
		for _, mdl := range p.Models {
			if mdl == "" || seen[mdl] {
				continue
			}
			seen[mdl] = true
			out = append(out, mdl)
		}
	}
	return out
}

// floatOverlay places a pre-built overlay box over the bottom rows of the
// main row (transcript area), keeping the frame exactly terminal height.
// boxLines longer than maxRows fall back to replacing the whole area
// (shouldn't happen: callers cap content before boxing so borders survive).
func floatOverlay(mainRow, box string, maxRows int) string {
	bl := strings.Split(box, "\n")
	if len(bl) > maxRows {
		bl = bl[:maxRows]
	}
	ml := strings.Split(mainRow, "\n")
	if len(bl) >= len(ml) {
		return strings.Join(bl, "\n")
	}
	copy(ml[len(ml)-len(bl):], bl)
	return strings.Join(ml, "\n")
}

// windowRange returns the [start,end) slice of an n-item list to show around
// idx within budget rows.
func windowRange(n, idx, budget int) (start, end int) {
	if budget < 1 {
		budget = 1
	}
	if n <= 0 {
		return 0, 0
	}
	if n <= budget {
		return 0, n
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	start = idx - budget/2
	if start < 0 {
		start = 0
	}
	end = start + budget
	if end > n {
		end = n
		start = end - budget
	}
	return start, end
}

// capLines hard-caps text to max rows so a later box keeps intact borders.
func capLines(s string, max int) string {
	if max < 1 {
		max = 1
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= max {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:max-1], "\n") + "\n" + "…"
}

func (m Model) renderSessionsDropdown(maxRows int) string {
	var sb strings.Builder
	// Keep "Sessions ● focus" for backward compat with TUI-5 tests that assert this substring
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Sessions ● focus")
	// Window the list so the panel fits the transcript area: title + hint
	// take 2 rows, the caller box adds 2 border rows.
	budget := maxRows - 4
	if budget < 1 {
		budget = 1
	}
	n := len(m.sessions)
	start, end := windowRange(n, m.sessionsDropdownIdx, budget)
	if n > budget {
		title += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(
			fmt.Sprintf(" (%d/%d)", m.sessionsDropdownIdx+1, n))
	}
	sb.WriteString(title + "\n")
	if n == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("(no sessions)") + "\n")
	}
	for i := start; i < end; i++ {
		s := m.sessions[i]
		id := s.ID
		if len(id) > 12 {
			id = id[:12]
		}
		marker := "  "
		if s.ID == m.sessionID {
			marker = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Render("▶ ")
		}
		line := marker + lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(id)
		if s.MessageCount > 0 {
			line += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf(" (%d)", s.MessageCount))
		}
		isMarked := m.markedIDs != nil && m.markedIDs[s.ID]
		if !isMarked && s.Metadata != nil {
			if v, ok := s.Metadata["success"]; ok {
				if b, ok := v.(bool); ok && b {
					isMarked = true
				}
			}
		}
		if isMarked {
			line += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Success)).Render(" ✓")
		}
		// Highlight selected index via background
		if i == m.sessionsDropdownIdx || (!m.sessionsDropdownVisible && i == m.sessionFocusIdx) {
			line = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BGElevated)).Foreground(lipgloss.Color(m.palette.Warning)).Render(marker+id) + lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf(" (%d)", s.MessageCount))
			if isMarked {
				line += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Success)).Render(" ✓")
			}
			// Wrap with background again for full line
			line = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BGElevated)).Render(line)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("↑/↓ navigate · enter select · esc close"))
	return sb.String()
}

func (m Model) renderModelPanel(maxRows int) string {
	if len(m.modelPanelList) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("(no models)")
	}
	var sb strings.Builder
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Select Model")
	// Window the list so the panel fits the transcript area: title +
	// deferred note + hint take 3 rows, the caller box adds 2 border rows.
	budget := maxRows - 5
	if budget < 1 {
		budget = 1
	}
	n := len(m.modelPanelList)
	start, end := windowRange(n, m.modelPanelIdx, budget)
	if n > budget {
		title += lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(
			fmt.Sprintf(" (%d/%d)", m.modelPanelIdx+1, n))
	}
	sb.WriteString(title + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("plugin-providers not listed yet (deferred)") + "\n")
	for i := start; i < end; i++ {
		mdl := m.modelPanelList[i]
		line := "  " + mdl
		if mdl == m.currentModel {
			line = "● " + mdl
		}
		if i == m.modelPanelIdx {
			line = lipgloss.NewStyle().Background(lipgloss.Color(m.palette.BGElevated)).Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("▶ " + mdl)
		} else {
			line = lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(line)
		}
		sb.WriteString(line + "\n")
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("↑/↓ navigate · enter select · esc close"))
	return sb.String()
}

// --- TUI-7 rail panels (floating detail panels) ---

func (m Model) renderContextPanel() string {
	var sb strings.Builder
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Context & tokens")
	sb.WriteString(title + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(fmt.Sprintf("session tokens: %s", formatTokens(m.totalTokens))) + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf("turns in window: %d", len(m.entries))) + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf("current model: %s", m.currentModel)) + "\n")
	if m.currentModel == "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("(model not set)") + "\n")
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("window % unavailable via RPC — shows local turns") + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("esc to close") + "\n")
	return sb.String()
}

func (m Model) renderPluginsPanel() string {
	var sb strings.Builder
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text))
	dimStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim))
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Plugins & skills")
	sb.WriteString(title + "\n")
	if len(m.plugins) == 0 && len(m.skills) == 0 {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("(no plugins/skills)") + "\n")
	} else {
		for _, p := range m.plugins {
			status := "disabled"
			if p.Enabled {
				status = "enabled"
			}
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(p.Name) + " " + lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("["+status+"]") + "\n")
		}
		for _, s := range m.skills {
			status := "disabled"
			if s.Enabled {
				status = "enabled"
			}
			line := textStyle.Render(s.Name) + " " + dimStyle.Render("["+status+"]")
			if s.Category != "" {
				line += " " + dimStyle.Render("("+s.Category+")")
			}
			sb.WriteString(line)
			sb.WriteString("\n")
		}
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("enable/disable via: forge plugin/skill CLI") + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("esc to close") + "\n")
	return sb.String()
}

func (m Model) renderTurnStatsPanel() string {
	var sb strings.Builder
	title := lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Accent)).Bold(true).Render("Turn stats")
	sb.WriteString(title + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render(fmt.Sprintf("turns: %d", m.turnCount)) + "\n")
	avg := "—"
	if m.latencyCount > 0 {
		avgMs := m.latencyTotalMs / int64(m.latencyCount)
		if avgMs < 1000 {
			avg = fmt.Sprintf("%dms", avgMs)
		} else {
			avg = fmt.Sprintf("%.1fs", float64(avgMs)/1000)
		}
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("avg latency: ") + lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Text)).Render(avg) + "\n")
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("last errors (last 5):") + "\n")
	// Show lastError (single truncated last error) and toast history? For now show lastError only, plus count.
	if m.lastError != "" {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("  • "+truncateError(m.lastError, 60)) + "\n")
	} else {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("  —") + "\n")
	}
	if m.toast != "" && m.toast != m.lastError {
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Faint)).Render("  • "+truncateError(m.toast, 60)) + "\n")
	}
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color(m.palette.Dim)).Render("esc to close") + "\n")
	return sb.String()
}

// --- Hit testing (pure functions for tests; documented fragility: Y mapping assumes fixed heights) ---

// HitTestRail maps a click Y to a rail card identifier. Pure function for tests.
// railTop is the Y where the rail column starts (after title bar), railHeight is its height.
// Returns "" if outside rail.
func HitTestRail(y, railTop, railHeight int) string {
	if y < railTop || y >= railTop+railHeight {
		return ""
	}
	rel := y - railTop
	third := railHeight / 3
	if third == 0 {
		third = 1
	}
	if rel < third {
		return "context"
	}
	if rel < 2*third {
		return "plugins"
	}
	return "turnstats"
}

// HitTestFooter maps a click in the footer to a hotspot. Pure function.
// footerTop is Y where footer starts, footerHeight is its height, width is terminal width.
// X positions from the right edge: [copiar] is right-aligned (last ~8 cols
// plus slack), then session ~20, then model ~20.
// Returns "copy", "session", "model", or "".
func HitTestFooter(x, y, footerTop, footerHeight, width int) string {
	if y < footerTop || y >= footerTop+footerHeight {
		return ""
	}
	// Approximate zones: rightmost 10 cols = copy, next 20 = session, next 20 = model
	if width <= 0 {
		return ""
	}
	if x >= width-10 {
		return "copy"
	}
	if x >= width-30 {
		return "session"
	}
	if x >= width-50 {
		return "model"
	}
	return ""
}

func (m *Model) handleMouseClick(mouse tea.Mouse) {
	x, y := mouse.X, mouse.Y
	// Compute geometry: titleBar titleHeightRows, then main rail area, then separator, input, footer
	titleH := titleHeightRows
	transH := m.viewport.Height()
	if transH <= 0 {
		transH = m.height - titleHeightRows - 1 - 4 - m.measureFooterHeight(m.width)
		if transH < 5 {
			transH = 5
		}
	}
	footerH := m.measureFooterHeight(m.width)
	separatorY := titleH + transH
	inputY := separatorY + 1
	footerTop := inputY + 4
	// Rail hit: only if visible and click in rail X region and Y in transcript range
	if m.showSidebar {
		railXStart := m.width - railWidth
		if x >= railXStart && y >= titleH && y < titleH+transH {
			card := HitTestRail(y, titleH, transH)
			if card != "" {
				if m.railPanel == card {
					m.railPanel = ""
				} else {
					m.railPanel = card
				}
				return
			}
		}
	}
	// Footer hotspots
	if y >= footerTop && y < footerTop+footerH {
		hotspot := HitTestFooter(x, y, footerTop, footerH, m.width)
		switch hotspot {
		case "session":
			// Clicking session id opens sessions dropdown (closes model panel: exclusive)
			m.modelPanelVisible = false
			if len(m.sessions) > 0 {
				if m.sessionsDropdownVisible {
					m.sessionsDropdownVisible = false
					m.sessionFocus = false
					m.input.Focus()
				} else {
					m.sessionsDropdownVisible = true
					m.sessionFocus = true
					idx := 0
					for i, s := range m.sessions {
						if s.ID == m.sessionID {
							idx = i
							break
						}
					}
					m.sessionsDropdownIdx = idx
					m.sessionFocusIdx = idx
					m.input.Blur()
				}
			} else {
				m.toast = "no sessions"
			}
			m.relayout()
			return
		case "model":
			// Clicking model name opens /model panel (closes dropdown: exclusive)
			m.sessionsDropdownVisible = false
			m.sessionFocus = false
			list := m.loadAvailableModels()
			if len(list) == 0 {
				m.toast = "no models configured"
				m.relayout()
				return
			}
			m.modelPanelList = list
			m.modelPanelIdx = 0
			m.modelPanelVisible = true
			m.relayout()
			return
		case "copy":
			// Clicking [copiar] copies the last assistant response.
			// Non-modal: panels and focus stay as they are.
			m.copyLastResponse()
			return
		}
	}
}

// mergeSidecarDurations merges persisted per-turn durations from the sidecar
// into reloaded assistant entries: Meta becomes "<model> · <elapsed>" when
// empty, or appends " · <elapsed>" when Meta already exists (e.g. "tokens N").
// Pure function: no model state mutation, safe to test directly.
func mergeSidecarDurations(entries []components.Entry, sidecar map[string]int64, sessionID, currentModel string) []components.Entry {
	if sessionID == "" || len(sidecar) == 0 || len(entries) == 0 {
		return entries
	}
	for i, e := range entries {
		if e.Role != "assistant" || e.IsTool || e.Seq == 0 {
			continue
		}
		durMs, ok := sidecar[sidecarKey(sessionID, e.Seq)]
		if !ok {
			continue
		}
		elapsedStr := formatDurationMs(durMs)
		switch {
		case e.Meta == "":
			if currentModel != "" {
				entries[i].Meta = currentModel + " · " + elapsedStr
			} else {
				entries[i].Meta = elapsedStr
			}
		case !strings.Contains(e.Meta, elapsedStr):
			entries[i].Meta = e.Meta + " · " + elapsedStr
		}
	}
	return entries
}

// formatDurationMs renders a duration in ms for Meta lines ("123ms" under a
// second, "1.2s" above). Shared by live elapsed injection and sidecar merge.
func formatDurationMs(durMs int64) string {
	if durMs < 1000 {
		return fmt.Sprintf("%dms", durMs)
	}
	return fmt.Sprintf("%.1fs", float64(durMs)/1000)
}



