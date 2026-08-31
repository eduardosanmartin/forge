// Package tools implements the forge tool system.
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/eduardosanmartin/forge/internal/perms"
	"github.com/eduardosanmartin/forge/internal/retrieval"
)

// RetrievalSearchTool implements the retrieval_search tool.
type RetrievalSearchTool struct {
	retriever *retrieval.Retriever
}

// NewRetrievalSearchTool creates a new retrieval search tool.
func NewRetrievalSearchTool(retriever *retrieval.Retriever) *RetrievalSearchTool {
	return &RetrievalSearchTool{retriever: retriever}
}

func (t *RetrievalSearchTool) Name() string {
	return "retrieval_search"
}

func (t *RetrievalSearchTool) Description() string {
	return "Search for relevant context from conversation history using semantic similarity. Returns top-k most relevant chunks with similarity scores."
}

func (t *RetrievalSearchTool) JSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"query": map[string]any{
				"type":        "string",
				"description": "Search query text",
			},
			"k": map[string]any{
				// "number" rather than "integer": forge's schema validator
				// supports the number/string/boolean/array/object subset,
				// and JSON integers arrive as float64 anyway.
				"type":        "number",
				"description": "Number of results to return (default: 5)",
				"minimum":     1,
				"maximum":     20,
				"default":     5,
			},
		},
		"required": []string{"query"},
	}
}

func (t *RetrievalSearchTool) Execute(ctx context.Context, req perms.Request) (Result, error) {
	// v1 custom tools receive their schema-validated arguments via
	// req.Input, populated by Registry.Execute. req.Args is the shell-only
	// argv channel and never carries tool JSON.
	args := req.Input
	if args == nil {
		args = make(map[string]any)
	}

	query, ok := args["query"].(string)
	if !ok || query == "" {
		return Result{Content: "ERROR: query parameter is required and must be a string"}, nil
	}

	k := 5
	if kVal, ok := args["k"]; ok {
		switch v := kVal.(type) {
		case float64:
			k = int(v)
		case int:
			k = v
		}
	}

	results, err := t.retriever.Search(query, k)
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: %v", err)}, nil
	}

	// Format results as JSON
	type resultItem struct {
		MessageID int64   `json:"message_id"`
		Role      string  `json:"role"`
		Content   string  `json:"content"`
		Score     float32 `json:"score"`
	}

	items := make([]resultItem, len(results))
	for i, r := range results {
		items[i] = resultItem{
			MessageID: r.MessageID,
			Role:      r.Role,
			Content:   r.Content,
			Score:     r.Score,
		}
	}

	content, err := json.Marshal(items)
	if err != nil {
		return Result{Content: fmt.Sprintf("ERROR: failed to marshal results: %v", err)}, nil
	}

	return Result{Content: string(content)}, nil
}

// newRetrievalSearchTool creates a new retrieval search tool (unexported constructor).
func newRetrievalSearchTool(retriever *retrieval.Retriever) *RetrievalSearchTool {
	return NewRetrievalSearchTool(retriever)
}
