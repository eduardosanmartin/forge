package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/embedding"
)

// embeddingsHealthTimeout bounds how long startEmbeddingsBackend waits for
// a freshly spawned llama-server to finish loading its model and start
// answering real requests. Loading bge-m3 (Q4_K_M) measured ~3s live; 20s
// leaves real headroom for a slower machine or a larger model without
// making a misconfigured/never-coming-up backend hang daemon startup
// indefinitely.
const embeddingsHealthTimeout = 20 * time.Second

// embeddingsHealthPollInterval is the gap between health check attempts
// while waiting for the model to finish loading.
const embeddingsHealthPollInterval = 500 * time.Millisecond

// startEmbeddingsBackend spawns and health-checks a llama-server process
// per cfg, for the real embedding backend behind skills.lazy_load's
// semantic matching and retrieval (RF-3.2) — see
// hojaDeRuta-embeddings-skills.md Fase 4. It NEVER returns an error and
// never fails daemon startup: every failure mode (disabled, unconfigured,
// binary/model missing, spawn failure, health check timeout) logs at Info
// or Warn and returns a nil client, which the caller uses to mean "fall
// back to the hash" (embedding.NewStore instead of NewStoreWithBackend).
//
// The returned cleanup func kills the spawned process and closes its log
// file; it is always safe to call (a no-op when no process was started).
// Callers must defer it unconditionally, the same as every other resource
// in runServe.
func startEmbeddingsBackend(ctx context.Context, cfg config.EmbeddingsConfig, logger *slog.Logger) (client *embedding.LlamaClient, dim int, cleanup func()) {
	noop := func() {}

	if !cfg.Enabled {
		return nil, 0, noop
	}
	if cfg.LlamaServerPath == "" || cfg.ModelPath == "" {
		logger.Info("embeddings backend: llama_server_path/model_path not set, using hash fallback")
		return nil, 0, noop
	}
	if _, err := os.Stat(cfg.LlamaServerPath); err != nil {
		logger.Info("embeddings backend: binary not found, using hash fallback", "path", cfg.LlamaServerPath)
		return nil, 0, noop
	}
	if _, err := os.Stat(cfg.ModelPath); err != nil {
		logger.Info("embeddings backend: model file not found, using hash fallback", "path", cfg.ModelPath)
		return nil, 0, noop
	}

	port := cfg.Port
	if port == 0 {
		p, err := freeTCPPort()
		if err != nil {
			logger.Warn("embeddings backend: could not find a free port, using hash fallback", "error", err)
			return nil, 0, noop
		}
		port = p
	}

	cmd := exec.Command(cfg.LlamaServerPath,
		"-m", cfg.ModelPath,
		"--embeddings",
		"--pooling", "mean",
		"--port", strconv.Itoa(port),
	)
	// llama-server logs its own startup/request activity — captured to a
	// file next to the model rather than piped through slog, since it's
	// not structured and mostly only useful when diagnosing a startup
	// failure, matching how forge-start.ps1 already logs the forge daemon
	// itself for the same reason (this session's QA harness work).
	logPath := filepath.Join(filepath.Dir(filepath.Dir(cfg.ModelPath)), "llama-server.log")
	logFile, logErr := os.Create(logPath)
	if logErr != nil {
		logger.Warn("embeddings backend: could not create log file, discarding llama-server output", "path", logPath, "error", logErr)
	} else {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}

	if err := cmd.Start(); err != nil {
		logger.Warn("embeddings backend: failed to start llama-server, using hash fallback", "path", cfg.LlamaServerPath, "error", err)
		if logFile != nil {
			logFile.Close()
		}
		return nil, 0, noop
	}

	addr := fmt.Sprintf("http://127.0.0.1:%d", port)
	c := embedding.NewLlamaClient(addr, filepath.Base(cfg.ModelPath))

	killAndCleanup := func() {
		killProcessCleanly(logger, cmd)
		if logFile != nil {
			logFile.Close()
		}
	}

	probedDim, err := waitForEmbeddingsHealthy(ctx, c)
	if err != nil {
		logger.Warn("embeddings backend: health check did not succeed in time, using hash fallback",
			"addr", addr, "pid", cmd.Process.Pid, "error", err, "log", logPath)
		killAndCleanup()
		return nil, 0, noop
	}

	logger.Info("embeddings backend: ready", "addr", addr, "model", cfg.ModelPath, "dim", probedDim, "pid", cmd.Process.Pid)
	return c, probedDim, killAndCleanup
}

// waitForEmbeddingsHealthy polls c.ProbeDimension until it succeeds, the
// parent ctx is cancelled, or embeddingsHealthTimeout elapses — whichever
// comes first. A plain retry loop rather than a ticker/select: this only
// ever runs once at daemon startup, not on a hot path, so the extra
// complexity of select-based cancellation isn't worth it — checking
// ctx.Err() once per iteration is enough to bail out promptly on Ctrl-C
// during startup.
func waitForEmbeddingsHealthy(ctx context.Context, c *embedding.LlamaClient) (dim int, err error) {
	deadline := time.Now().Add(embeddingsHealthTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		d, probeErr := c.ProbeDimension(ctx)
		if probeErr == nil {
			return d, nil
		}
		lastErr = probeErr
		time.Sleep(embeddingsHealthPollInterval)
	}
	return 0, fmt.Errorf("timed out after %s: %w", embeddingsHealthTimeout, lastErr)
}

// killProcessCleanly kills cmd's process and waits for it to be reaped —
// same rigor as the QA harness's process management this session
// (hojaDeRuta-qa-autonomo-tui.md): never assume a spawned process died
// just because Kill() was called, and never leave a zombie/leaked handle
// behind. Safe to call on a cmd whose process never started.
func killProcessCleanly(logger *slog.Logger, cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	if err := cmd.Process.Kill(); err != nil {
		logger.Warn("embeddings backend: failed to kill llama-server", "pid", cmd.Process.Pid, "error", err)
	}
	_ = cmd.Wait() // reap regardless — Wait after a successful Kill mainly clears the process table entry
}

// freeTCPPort asks the OS for an ephemeral port by binding to :0 and
// immediately releasing it — the same trick used to let net.Listen itself
// pick a port, needed here because llama-server takes its port as a CLI
// flag rather than accepting an OS-assigned one directly.
func freeTCPPort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("unexpected listener address type %T", l.Addr())
	}
	return addr.Port, nil
}
