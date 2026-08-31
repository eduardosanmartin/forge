// Package tools implements the forge tool system.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eduardosanmartin/forge/internal/anchor"
	"github.com/eduardosanmartin/forge/internal/perms"
)

// AnchoringStoreTool implements the anchoring.store tool.
type AnchoringStoreTool struct {
	anchorStore *anchor.AnchorStoreSQL
}

// NewAnchoringStoreTool creates a new anchoring store tool.
func NewAnchoringStoreTool(anchorStore *anchor.AnchorStoreSQL) *AnchoringStoreTool {
	return &AnchoringStoreTool{anchorStore: anchorStore}
}

func (t *AnchoringStoreTool) Name() string {
	return "anchoring.store"
}

func (t *AnchoringStoreTool) Description() string {
	return "Store an anchored fact/decision permanently. Anchors are preserved during compaction and can be retrieved later."
}

func (t *AnchoringStoreTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"content": map[string]any{
				"type":        "string",
				"description": "The fact/decision to anchor",
			},
			"session_id": map[string]any{
				"type":        "string",
				"description": "Session ID to associate with this anchor",
			},
			"source": map[string]any{
				"type":        "string",
				"description": "Source of the anchor (user/assistant/auto)",
				"default":     "user",
			},
			"tags": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Tags for categorization",
			},
		},
		"required": []string{"content", "session_id"},
	}
}

func (t *AnchoringStoreTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	// v1 custom tools receive their schema-validated arguments via
	// req.Input, populated by Registry.Execute. req.Args is the shell-only
	// argv channel and never carries tool JSON.
	args := req.Input
	if args == nil {
		args = make(map[string]any)
	}

	content, ok := args["content"].(string)
	if !ok || content == "" {
		return Result{Content: "ERROR: content parameter is required and must be a string"}, nil
	}

	sessionID, ok := args["session_id"].(string)
	if !ok || sessionID == "" {
		return Result{Content: "ERROR: session_id parameter is required and must be a string"}, nil
	}

	source := "user"
	if src, ok := args["source"].(string); ok {
		source = src
	}

	var tags []string
	if tagsArg, ok := args["tags"]; ok {
		if tagsList, ok := tagsArg.([]interface{}); ok {
			for _, t := range tagsList {
				if s, ok := t.(string); ok {
					tags = append(tags, s)
				}
			}
		}
	}

	anchor := anchor.Anchor{
		SessionID: sessionID,
		Content:   content,
		Source:    source,
		Tags:      tags,
	}

	created, err := t.anchorStore.Create(ctx, anchor)
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: %v", err)}, nil
	}

	return Result{Content: fmt.Sprintf("Anchor created with ID %d", created.ID)}, nil
}

// AnchoringListTool implements the anchoring.list tool.
type AnchoringListTool struct {
	anchorStore *anchor.AnchorStoreSQL
}

func NewAnchoringListTool(anchorStore *anchor.AnchorStoreSQL) *AnchoringListTool {
	return &AnchoringListTool{anchorStore: anchorStore}
}

func (t *AnchoringListTool) Name() string {
	return "anchoring.list"
}

func (t *AnchoringListTool) Description() string {
	return "List all anchored facts for a session or globally."
}

func (t *AnchoringListTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id": map[string]any{
				"type":        "string",
				"description": "Session ID to filter anchors (optional, omit for all sessions)",
			},
		},
		"required": []string{},
	}
}

func (t *AnchoringListTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	// v1 custom tools receive their schema-validated arguments via
	// req.Input, populated by Registry.Execute. req.Args is the shell-only
	// argv channel and never carries tool JSON.
	args := req.Input
	if args == nil {
		args = make(map[string]any)
	}

	var anchors []anchor.Anchor
	var err error

	if sessionID, ok := args["session_id"].(string); ok && sessionID != "" {
		anchors, err = t.anchorStore.List(ctx, args["session_id"].(string))
	} else {
		anchors, err = t.anchorStore.ListAll(ctx)
	}

	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: %v", err)}, nil
	}

	type resultItem struct {
		ID        int64    `json:"id"`
		SessionID string   `json:"session_id"`
		Content   string   `json:"content"`
		Source    string   `json:"source"`
		Tags      []string `json:"tags"`
		CreatedAt int64    `json:"created_at"`
	}

	items := make([]resultItem, len(anchors))
	for i, a := range anchors {
		items[i] = resultItem{
			ID:        a.ID,
			SessionID: a.SessionID,
			Content:   a.Content,
			Source:    a.Source,
			Tags:      a.Tags,
			CreatedAt: a.CreatedAt.Unix(),
		}
	}

	content, err := json.Marshal(items)
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: failed to marshal results: %v", err)}, nil
	}

	return Result{Content: string(content)}, nil
}

// AnchoringGetTool implements the anchoring.get tool.
type AnchoringGetTool struct {
	anchorStore *anchor.AnchorStoreSQL
}

func NewAnchoringGetTool(anchorStore *anchor.AnchorStoreSQL) *AnchoringGetTool {
	return &AnchoringGetTool{anchorStore: anchorStore}
}

func (t *AnchoringGetTool) Name() string {
	return "anchoring.get"
}

func (t *AnchoringGetTool) Description() string {
	return "Retrieve a specific anchored fact by ID."
}

func (t *AnchoringGetTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				// "number" rather than "integer": forge's schema validator
				// supports the number/string/boolean/array/object subset,
				// and JSON integers arrive as float64 anyway.
				"type":        "number",
				"description": "Anchor ID to retrieve",
			},
		},
		"required": []string{"id"},
	}
}

func (t *AnchoringGetTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	// v1 custom tools receive their schema-validated arguments via
	// req.Input, populated by Registry.Execute. req.Args is the shell-only
	// argv channel and never carries tool JSON.
	args := req.Input
	if args == nil {
		args = make(map[string]any)
	}

	idVal, ok := args["id"]
	if !ok {
		return Result{Content: "ERROR: id parameter is required"}, nil
	}

	var id int64
	switch v := idVal.(type) {
	case float64:
		id = int64(v)
	case int:
		id = int64(v)
	default:
		return Result{Content: "ERROR: id must be an integer"}, nil
	}

	anchor, err := t.anchorStore.Get(ctx, id)
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: %v", err)}, nil
	}

	content, err := json.Marshal(anchor)
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: failed to marshal anchor: %v", err)}, nil
	}

	return Result{Content: string(content)}, nil
}

// AnchoringDeleteTool implements the anchoring.delete tool.
type AnchoringDeleteTool struct {
	anchorStore *anchor.AnchorStoreSQL
}

func NewAnchoringDeleteTool(anchorStore *anchor.AnchorStoreSQL) *AnchoringDeleteTool {
	return &AnchoringDeleteTool{anchorStore: anchorStore}
}

func (t *AnchoringDeleteTool) Name() string {
	return "anchoring.delete"
}

func (t *AnchoringDeleteTool) Description() string {
	return "Delete an anchored fact by ID."
}

func (t *AnchoringDeleteTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{
				// "number" rather than "integer": forge's schema validator
				// supports the number/string/boolean/array/object subset,
				// and JSON integers arrive as float64 anyway.
				"type":        "number",
				"description": "Anchor ID to delete",
			},
		},
		"required": []string{"id"},
	}
}

func (t *AnchoringDeleteTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	// v1 custom tools receive their schema-validated arguments via
	// req.Input, populated by Registry.Execute. req.Args is the shell-only
	// argv channel and never carries tool JSON.
	args := req.Input
	if args == nil {
		args = make(map[string]any)
	}

	idVal, ok := args["id"]
	if !ok {
		return Result{Content: "ERROR: id parameter is required"}, nil
	}

	var id int64
	switch v := idVal.(type) {
	case float64:
		id = int64(v)
	case int:
		id = int64(v)
	default:
		return Result{Content: "ERROR: id must be an integer"}, nil
	}

	if err := t.anchorStore.Delete(ctx, id); err != nil {
		return Result{Content: fmt.Sprintf("ERROR: %v", err)}, nil
	}

	return Result{Content: fmt.Sprintf("Anchor %d deleted", id)}, nil
}

// newAnchoringStoreTool creates a new anchoring store tool (unexported constructor).
func newAnchoringStoreTool(anchorStore *anchor.AnchorStoreSQL) *AnchoringStoreTool {
	return NewAnchoringStoreTool(anchorStore)
}

// newAnchoringListTool creates a new anchoring list tool (unexported constructor).
func newAnchoringListTool(anchorStore *anchor.AnchorStoreSQL) *AnchoringListTool {
	return NewAnchoringListTool(anchorStore)
}

// newAnchoringGetTool creates a new anchoring get tool (unexported constructor).
func newAnchoringGetTool(anchorStore *anchor.AnchorStoreSQL) *AnchoringGetTool {
	return NewAnchoringGetTool(anchorStore)
}

// newAnchoringDeleteTool creates a new anchoring delete tool (unexported constructor).
func newAnchoringDeleteTool(anchorStore *anchor.AnchorStoreSQL) *AnchoringDeleteTool {
	return NewAnchoringDeleteTool(anchorStore)
}
