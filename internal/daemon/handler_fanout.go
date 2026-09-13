package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/eduardosanmartin/forge/internal/store"
)

// isFanoutValidationError reports client-side mistakes (bad task/model
// entries, unknown provider names) that map to ErrCodeInvalidParams;
// anything else is a daemon-side failure.
func isFanoutValidationError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "models[") ||
		strings.HasPrefix(msg, "models is required") ||
		strings.HasPrefix(msg, "unknown provider") ||
		strings.HasPrefix(msg, "task is required")
}

func (h *Handler) handleFanout(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
	var params FanoutParams
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "invalid params", err.Error())
	}
	if params.Task == "" {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "task is required", nil)
	}
	if len(params.Models) == 0 {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "models is required (one \"provider/model\" entry per child)", nil)
	}
	if params.MaxIterations < 0 || params.TokenBudget < 0 {
		return NewErrorResponse(req.ID, ErrCodeInvalidParams, "max_iter and token_budget must be >= 0", nil)
	}

	res, err := h.mgr.Fanout(ctx, params)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSessionNotFound):
			return NewErrorResponse(req.ID, ErrCodeSessionNotFound, "parent session not found", params.SessionID)
		case isFanoutValidationError(err):
			return NewErrorResponse(req.ID, ErrCodeInvalidParams, err.Error(), nil)
		default:
			return NewErrorResponse(req.ID, ErrCodeInternalError, "fanout failed", err.Error())
		}
	}
	return h.resultResponse(req.ID, res)
}
