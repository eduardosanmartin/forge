// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/eduardosanmartin/forge/internal/agent"
	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/llm"
	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/tools"
)

// Daemon is the main daemon process.
type Daemon struct {
	addr      string
	cfg       *config.Config
	store     *store.Store
	llmReg    *llm.Registry
	toolsReg  *tools.Registry
	permsEng  *perms.Engine
	emergency *EmergencyState
	mgr       *SessionManager
	transport *Transport
	handler   *Handler
	logger    *slog.Logger
	addrFile  string
	mu        sync.Mutex
	running   bool
}

// New creates a new Daemon instance.
func New(
	cfg *config.Config,
	store *store.Store,
	llmReg *llm.Registry,
	toolsReg *tools.Registry,
	permsEng *perms.Engine,
	logger *slog.Logger,
	addr string,
	v1Deps agent.V1Deps,
) (*Daemon, error) {
	if addr == "" {
		addr = "127.0.0.1:0"
	}

	// Daemon address file for client discovery
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	addrFile := filepath.Join(home, ".forge", "daemon.addr")

	emergency := NewEmergencyState(logger)
	mgr := NewSessionManager(store, llmReg, toolsReg, emergency, logger, cfg, permsEng, store, WithV1Deps(v1Deps))
	handler := NewHandler(mgr, logger)
	transport := NewTransport(addr, handler, logger)

	d := &Daemon{
		addr:      addr,
		cfg:       cfg,
		store:     store,
		llmReg:    llmReg,
		toolsReg:  toolsReg,
		permsEng:  permsEng,
		emergency: emergency,
		mgr:       mgr,
		transport: transport,
		handler:   handler,
		logger:    logger,
		addrFile:  addrFile,
	}
	return d, nil
}

// Start starts the daemon and blocks until context is cancelled.
func (d *Daemon) Start(ctx context.Context) error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return fmt.Errorf("daemon already running")
	}
	d.running = true
	d.mu.Unlock()

	// Start transport
	if err := d.transport.Start(ctx); err != nil {
		d.mu.Lock()
		d.running = false
		d.mu.Unlock()
		return fmt.Errorf("start transport: %w", err)
	}

	// Write address file for client discovery
	if err := d.writeAddrFile(); err != nil {
		d.logger.Warn("failed to write addr file", "error", err)
	}

	d.logger.Info("daemon started", "addr", d.transport.Addr())

	// Wait for context cancellation
	<-ctx.Done()

	return d.Stop()
}

// Stop stops the daemon gracefully.
func (d *Daemon) Stop() error {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return nil
	}
	d.running = false
	d.mu.Unlock()

	d.logger.Info("daemon stopping")

	// Stop transport
	if err := d.transport.Stop(); err != nil {
		d.logger.Error("transport stop error", "error", err)
	}

	// Close store
	if err := d.store.Close(); err != nil {
		d.logger.Error("store close error", "error", err)
	}

	// Close LLM registry
	if err := d.llmReg.Close(); err != nil {
		d.logger.Error("llm registry close error", "error", err)
	}

	// Remove address file
	d.removeAddrFile()

	d.logger.Info("daemon stopped")
	return nil
}

// Addr returns the listening address.
func (d *Daemon) Addr() string {
	return d.transport.Addr()
}

// writeAddrFile writes the listening address to a file for client discovery.
func (d *Daemon) writeAddrFile() error {
	dir := filepath.Dir(d.addrFile)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	return os.WriteFile(d.addrFile, []byte(d.transport.Addr()), 0o644)
}

// removeAddrFile removes the address file.
func (d *Daemon) removeAddrFile() {
	_ = os.Remove(d.addrFile)
}

// IsRunning returns true if the daemon is running.
func (d *Daemon) IsRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// GetSessionManager returns the session manager for CLI commands.
func (d *Daemon) GetSessionManager() *SessionManager {
	return d.mgr
}

// GetEmergencyState returns the emergency state for CLI commands.
func (d *Daemon) GetEmergencyState() *EmergencyState {
	return d.emergency
}

// GetTransport returns the transport for CLI commands.
func (d *Daemon) GetTransport() *Transport {
	return d.transport
}
