package tui

import (
	"context"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"
	"github.com/eduardosanmartin/forge/internal/client"
	"github.com/eduardosanmartin/forge/internal/daemon"
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
	if err := a.c.Call(ctx, daemon.MethodExecuteTurn, daemon.ExecuteTurnParams{SessionID: sessionID, UserMessage: message}, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
func (a *ClientAdapter) GetMessagesSince(sessionID string, sinceSeq int) (*daemon.GetMessagesResult, error) {
	ctx := context.Background()
	return a.c.GetMessagesSince(ctx, sessionID, sinceSeq)
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
