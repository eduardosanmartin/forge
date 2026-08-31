// Package tools implements the forge tool system.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eduardosanmartin/forge/internal/compaction"
	"github.com/eduardosanmartin/forge/internal/perms"
)

// CompactionSummarizeTool implements the compaction.summarize tool.
type CompactionSummarizeTool struct {
	compactor *compaction.Compactor
}

// NewCompactionSummarizeTool creates a new compaction summarize tool.
func NewCompactionSummarizeTool(compactor *compaction.Compactor) *CompactionSummarizeTool {
	return &CompactionSummarizeTool{compactor: compactor}
}

func (t *CompactionSummarizeTool) Name() string {
	return "compaction.summarize"
}

func (t *CompactionSummarizeTool) Description() string {
	return "Summarize conversation history hierarchically, preserving anchored facts. Returns compacted turns with summary statistics."
}

func (t *CompactionSummarizeTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"turns": map[string]any{
				"type":        "array",
				"description": "Turns to compact (optional, uses session history if omitted)",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"role": map[string]any{
							"type":        "string",
							"description": "Role of the message (user/assistant/system)",
						},
						"content": map[string]any{
							"type":        "string",
							"description": "Message content",
						},
						"tokens": map[string]any{
							"type":        "integer",
							"description": "Approximate token count",
						},
						"anchor": map[string]any{
							"type":        "boolean",
							"description": "Whether this turn is an anchor (never compressed)",
						},
					},
					"required": []string{"role", "content"},
				},
			},
			"keep_recent": map[string]any{
				"type":        "integer",
				"description": "Number of recent turns to keep verbatim (default: 10)",
				"default":     10,
				"minimum":     0,
			},
		},
		"required": []string{},
	}
}

func (t *CompactionSummarizeTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	// Extract args from the request (stored as JSON in Args[0])
	var args map[string]any
	if len(req.Args) > 0 {
		if err := json.Unmarshal([]byte(req.Args[0]), &args); err != nil {
			return Result{Content: fmt.Sprintf("ERROR: failed to parse args: %v", err)}, nil
		}
	}
	if args == nil {
		args = make(map[string]any)
	}

	var turns []compaction.Turn

	// If turns provided, use them; otherwise would need session context
	if turnsArg, ok := args["turns"]; ok {
		turnsList, ok := turnsArg.([]interface{})
		if !ok {
			return Result{Content: "ERROR: turns must be an array"}, nil
		}
		turns = make([]compaction.Turn, 0, len(turnsList))
		for _, t := range turnsList {
			m, ok := t.(map[string]interface{})
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			content, _ := m["content"].(string)
			tokens := 0
			if t, ok := m["tokens"]; ok {
				switch v := t.(type) {
				case float64:
					tokens = int(v)
				case int:
					tokens = v
				}
			}
			anchor := false
			if a, ok := m["anchor"]; ok {
				if b, ok := a.(bool); ok {
					anchor = b
				}
			}
			turns = append(turns, compaction.Turn{
				Role:    role,
				Content: content,
				Tokens:  tokens,
				Anchor:  anchor,
			})
		}
	}

	compacted, stats, err := t.compactor.Compact(turns)
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: %v", err)}, nil
	}

	// Format results
	type resultItem struct {
		Role    string `json:"role"`
		Content string `json:"content"`
		Tokens  int    `json:"tokens"`
		Anchor  bool   `json:"anchor"`
		Summary string `json:"summary,omitempty"`
	}

	items := make([]resultItem, len(compacted))
	for i, t := range compacted {
		items[i] = resultItem{
			Role:    t.Role,
			Content: t.Content,
			Tokens:  t.Tokens,
			Anchor:  t.Anchor,
			Summary: t.Summary,
		}
	}

	content, err := json.Marshal(map[string]any{
		"compacted":       items,
		"stats":           stats,
		"original_turns":  len(turns),
		"compacted_turns": len(compacted),
	})
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: failed to marshal results: %v", err)}, nil
	}

	return Result{Content: string(content)}, nil
}

// newCompactionSummarizeTool creates a new compaction summarize tool (unexported constructor).
func newCompactionSummarizeTool(compactor *compaction.Compactor) *CompactionSummarizeTool {
	return NewCompactionSummarizeTool(compactor)
}
