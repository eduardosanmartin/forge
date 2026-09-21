package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
	"github.com/eduardosanmartin/forge/internal/run"
)

// ClientAdapter adapts internal/client.Client to TUIClient interface.
type ClientAdapter struct {
	c *client.Client
}

// NewClientAdapter wraps a live client.
func NewClientAdapter(c *client.Client) *ClientAdapter { return &ClientAdapter{c: c} }

func (a *ClientAdapter) Status() (*daemon.StatusResult, error) {
	var res daemon.StatusResult
	ctx := context.Background()
	if err := a.c.Call(ctx, daemon.MethodStatus, nil, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
func (a *ClientAdapter) ListSessions(limit int) (*daemon.ListSessionsResult, error) {
	ctx := context.Background()
	return a.c.ListSessions(ctx, limit, 0)
}
func (a *ClientAdapter) CreateSession() (*daemon.SessionResult, error) {
	var res daemon.SessionResult
	ctx := context.Background()
	if err := a.c.Call(ctx, daemon.MethodCreateSession, daemon.CreateSessionParams{Metadata: map[string]any{"source": "tui"}}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
func (a *ClientAdapter) ExecuteTurn(sessionID, message string) (*daemon.ExecuteTurnResult, error) {
	var res daemon.ExecuteTurnResult
	ctx := context.Background()
	// EnableSkills: true — the TUI never sent any v1 flag before this (found
	// live via hojaDeRuta-embeddings-skills.md investigation: skills could be
	// installed and enabled yet never fire from the TUI). Only skills is
	// turned on here, not retrieval/compaction/anchoring/routing — those
	// remain CLI-only (forge chat --retrieval etc.) until a real need to
	// extend this beyond skills.
	if err := a.c.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{SessionID: sessionID, UserMessage: message, EnableSkills: true}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
func (a *ClientAdapter) GetMessagesSince(sessionID string, sinceSeq int) (*daemon.GetMessagesResult, error) {
	ctx := context.Background()
	return a.c.GetMessagesSince(ctx, sessionID, sinceSeq)
}
func (a *ClientAdapter) HaltSession(sessionID, reason string) error {
	ctx := context.Background()
	return a.c.HaltSession(ctx, sessionID, reason)
}

// HaltAll triggers the daemon's global emergency stop (emergency.halt_all —
// every session, not just one). The RPC itself takes no params (same call
// shape the REPL's /halt with no target uses); reason is accepted for
// interface symmetry with HaltSession and isn't transmitted today.
func (a *ClientAdapter) HaltAll(reason string) error {
	ctx := context.Background()
	return a.c.Call(ctx, daemon.MethodHaltAll, nil, nil)
}
func (a *ClientAdapter) ResumeSession(sessionID string) error {
	ctx := context.Background()
	return a.c.ResumeSession(ctx, sessionID)
}
func (a *ClientAdapter) SwitchModel(sessionID, model string) error {
	ctx := context.Background()
	return a.c.SwitchModel(ctx, sessionID, model)
}
func (a *ClientAdapter) MarkSuccess(sessionID string) error {
	ctx := context.Background()
	return a.c.MarkSuccess(ctx, sessionID)
}
func (a *ClientAdapter) PluginList() (*daemon.PluginListResult, error) {
	ctx := context.Background()
	return a.c.PluginList(ctx)
}
func (a *ClientAdapter) SkillList() (*daemon.SkillListResult, error) {
	ctx := context.Background()
	return a.c.SkillList(ctx)
}
func (a *ClientAdapter) Events(ctx context.Context) (<-chan daemon.JSONRPCNotification, error) {
	return a.c.Events(ctx)
}

// Run.* methods (Fase 4, hojaDeRuta-multiagente.md): thin wrappers over the
// RPCs added in Fase 3, same generic a.c.Call shape Status/CreateSession
// already use above — no bespoke client.Client helper needed.
func (a *ClientAdapter) RunStart(mani run.Manifest, stateDir string, decompose bool) (*daemon.RunResult, error) {
	var res daemon.RunResult
	ctx := context.Background()
	if err := a.c.Call(ctx, daemon.MethodRunStart, daemon.RunStartParams{Manifest: mani, StateDir: stateDir, Decompose: decompose}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
func (a *ClientAdapter) RunStatus(runID string) (*daemon.RunResult, error) {
	var res daemon.RunResult
	ctx := context.Background()
	if err := a.c.Call(ctx, daemon.MethodRunStatus, daemon.RunStatusParams{RunID: runID}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
func (a *ClientAdapter) RunApproveCheckpoint(runID string, approved bool) (*daemon.RunResult, error) {
	var res daemon.RunResult
	ctx := context.Background()
	if err := a.c.Call(ctx, daemon.MethodRunApproveCheckpoint, daemon.RunApproveCheckpointParams{RunID: runID, Approved: approved}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
func (a *ClientAdapter) RunCancel(runID string) (*daemon.RunResult, error) {
	var res daemon.RunResult
	ctx := context.Background()
	if err := a.c.Call(ctx, daemon.MethodRunCancel, daemon.RunCancelParams{RunID: runID}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// resolveDefaultModel reads the project config JSON at configPath and returns
// providers[default_provider].models[0]. Returns "" on any error — the footer
// already hides an empty model name.
func resolveDefaultModel(configPath string) string {
	if configPath == "" {
		configPath = ".forge/config.json"
	}
	data, err := os.ReadFile(configPath)
	if err != nil || len(data) == 0 {
		return ""
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		return ""
	}
	var dp string
	if raw, ok := doc["default_provider"]; ok {
		if err := json.Unmarshal(raw, &dp); err != nil {
			return ""
		}
	}
	if dp == "" {
		return ""
	}
	rawProv, ok := doc["providers"]
	if !ok {
		return ""
	}
	var providers map[string]struct {
		Models []string `json:"models"`
	}
	if err := json.Unmarshal(rawProv, &providers); err != nil {
		return ""
	}
	if p, ok := providers[dp]; ok && len(p.Models) > 0 && p.Models[0] != "" {
		return p.Models[0]
	}
	return ""
}

// Run launches the TUI program. It dials the daemon via internal/client and
// builds the model.
func Run(ctx context.Context, addr string) error {
	cfgPath, _ := TUIConfigPath()
	// Resolve full path via current working dir
	// Keep relative as ".forge/config.json" — Load/Save handle it.
	cfg := LoadTUIConfig(cfgPath)
	pal := MustGetPalette(cfg.Palette)

	// Dial daemon.
	cl, err := client.Connect(ctx, addr)
	var tuiClient TUIClient
	var daemonErr string
	if err != nil {
		daemonErr = err.Error()
	} else {
		tuiClient = NewClientAdapter(cl)
		defer cl.Close()
	}

	m := NewModel(cfg, pal, cfg.Palette, cfgPath, tuiClient)
	if m.currentModel == "" {
		m.currentModel = resolveDefaultModel(cfgPath)
	}
	if daemonErr != "" {
		m.daemonErr = daemonErr
	}
	// Ensure size is handled via WindowSizeMsg; set initial via tea.WithWindowSize in program.

	p := tea.NewProgram(m)
	final, perr := p.Run()
	if perr != nil {
		return fmt.Errorf("tui run: %w", perr)
	}
	_ = final
	// Ensure config persisted on exit (if changed during session, already persisted per change).
	_ = os.Stdout
	return nil
}
