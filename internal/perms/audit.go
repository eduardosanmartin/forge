package perms

import (
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/eduardosanmartin/forge/internal/logging"
)

// This file implements the audit side of every permission decision
// (RNF-4.1 in concert with RNF-4.4): each Check emits exactly one
// "perm.check" record when a logger is attached. Free-text values pass
// through logging.Redact BEFORE logging, so secrets embedded in commands,
// args, or paths never reach a sink — even when the injected handler is a
// plain slog handler without forge's redacting wrapper.

const (
	// auditMsg is the fixed message of every permission audit record.
	auditMsg = "perm.check"
	// maxAuditDetailRunes caps joined argument strings in audit records.
	maxAuditDetailRunes = 200
)

// audit emits the single structured record describing req and its outcome.
func (e *Engine) audit(req Request, d Decision) {
	if e.logger == nil {
		return
	}
	attrs := []any{
		slog.String("kind", string(req.Kind)),
		slog.Bool("allowed", d.Allowed),
		slog.String("rule", d.Rule),
	}
	switch req.Kind {
	case KindFsRead, KindFsWrite:
		attrs = append(attrs, slog.String("path", logging.Redact(e.auditPath(req.Path))))
	case KindShell:
		attrs = append(attrs,
			slog.String("command", logging.Redact(commandBase(req.Command))),
			slog.String("args", logging.Redact(truncate(strings.Join(req.Args, " ")))),
		)
	case KindGit:
		attrs = append(attrs,
			slog.String("subcommand", logging.Redact(req.Subcommand)),
			slog.String("args", logging.Redact(truncate(strings.Join(req.GitArgs, " ")))),
		)
	case KindCustom:
		// Command carries the internal tool name (tools.BuildPermsRequest
		// keeps it there precisely so the audit trail is readable).
		attrs = append(attrs, slog.String("tool", logging.Redact(req.Command)))
	}
	e.logger.Info(auditMsg, attrs...)
}

// auditPath returns the path form recorded for fs requests: the forward-
// slashed workspace-relative form when the resolved path lies inside the
// workspace, otherwise the absolute form (escaped requests must remain
// identifiable).
func (e *Engine) auditPath(reqPath string) string {
	abs, err := filepath.Abs(reqPath)
	if err != nil {
		return filepath.ToSlash(reqPath)
	}
	if rel, inside := e.workspaceRel(abs); inside {
		return rel
	}
	return filepath.ToSlash(abs)
}

// truncate cuts s to at most maxAuditDetailRunes runes, never splitting a
// multi-byte character.
func truncate(s string) string {
	runes := []rune(s)
	if len(runes) <= maxAuditDetailRunes {
		return s
	}
	return string(runes[:maxAuditDetailRunes])
}
