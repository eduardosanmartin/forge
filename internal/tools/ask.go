package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// AskDecision is a human's answer to a permission request.
type AskDecision string

const (
	AskDeny         AskDecision = "deny"
	AskAllowOnce    AskDecision = "allow_once"
	AskAllowSession AskDecision = "allow_session"
)

// AskRequest describes an operation that matched a permissions "ask" rule.
type AskRequest struct {
	SessionID string `json:"session_id,omitempty"`
	Tool      string `json:"tool"`
	Kind      string `json:"kind"`
	Rule      string `json:"rule"`
	Summary   string `json:"summary"`
}

// Asker asks a human about req and blocks until an answer (or until ctx
// ends — then it must return AskDeny). Wired by the daemon to a
// permission.request.event / permission.respond round trip.
type Asker func(ctx context.Context, req AskRequest) AskDecision

// askState holds the registry's asker and the per-session approvals
// ("allow for this session") keyed by the ask rule that matched.
type askState struct {
	mu     sync.Mutex
	asker  Asker
	grants map[string]map[string]bool // session ID -> ask rule -> granted
}

// SetAsker wires the function that asks a human about "ask" permissions.
// Without one, every ask resolves to deny (deny-by-default).
func (r *Registry) SetAsker(a Asker) {
	r.ask.mu.Lock()
	r.ask.asker = a
	r.ask.mu.Unlock()
}

// resolveAsk turns an "ask" decision into allow or deny: an earlier
// session-wide approval of the same rule allows; otherwise the asker is
// consulted. Allowed outcomes keep the ask rule name in Rule, so the audit
// trail shows the operation ran on a human's approval.
func (r *Registry) resolveAsk(ctx context.Context, tool string, req perms.Request, d perms.Decision) perms.Decision {
	session := SessionIDFromContext(ctx)
	r.ask.mu.Lock()
	granted := session != "" && r.ask.grants[session][d.Rule]
	asker := r.ask.asker
	r.ask.mu.Unlock()
	if granted {
		return perms.Decision{Allowed: true, Rule: d.Rule + " (approved for this session)"}
	}
	if asker == nil {
		return perms.Decision{Rule: d.Rule + " (no client available to approve)"}
	}
	switch asker(ctx, AskRequest{SessionID: session, Tool: tool, Kind: string(req.Kind), Rule: d.Rule, Summary: summarizeRequest(req)}) {
	case AskAllowOnce:
		return perms.Decision{Allowed: true, Rule: d.Rule + " (approved once)"}
	case AskAllowSession:
		if session != "" {
			r.ask.mu.Lock()
			if r.ask.grants == nil {
				r.ask.grants = make(map[string]map[string]bool)
			}
			if r.ask.grants[session] == nil {
				r.ask.grants[session] = make(map[string]bool)
			}
			r.ask.grants[session][d.Rule] = true
			r.ask.mu.Unlock()
		}
		return perms.Decision{Allowed: true, Rule: d.Rule + " (approved for this session)"}
	default:
		return perms.Decision{Rule: d.Rule + " (denied by the user)"}
	}
}

// summarizeRequest renders a one-line human description of req.
func summarizeRequest(req perms.Request) string {
	switch req.Kind {
	case perms.KindShell:
		return strings.TrimSpace(req.Command + " " + strings.Join(req.Args, " "))
	case perms.KindGit:
		return strings.TrimSpace("git " + req.Subcommand + " " + strings.Join(req.GitArgs, " "))
	case perms.KindFsWrite:
		return fmt.Sprintf("write %s (%d bytes)", req.Path, len(req.Content))
	default:
		return fmt.Sprintf("%s %s%s", req.Kind, req.Path, req.Command)
	}
}
