// Package tools implements forge's native tool layer with an MCP-shaped
// interface, backed by the deny-by-default permission engine.
package tools

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/eduardosanmartin/forge/internal/perms"
)

// SchemaValidationError represents a schema validation failure.
type SchemaValidationError struct {
	Field string
	Msg   string
}

func (e *SchemaValidationError) Error() string {
	if e.Field != "" {
		return "schema validation: " + e.Field + ": " + e.Msg
	}
	return "schema validation: " + e.Msg
}

// ValidateArgs validates args against the tool's JSON Schema (draft-07 subset).
// Supported: type (string, number, boolean, array, object), required, properties.
// Returns nil on success, or a SchemaValidationError on failure.
func ValidateArgs(schema map[string]any, args map[string]any) error {
	if schema == nil {
		return nil // no schema = no validation
	}

	// Check required fields - handle both []string and []any
	requiredRaw := schema["required"]
	var required []any
	switch v := requiredRaw.(type) {
	case []any:
		required = v
	case []string:
		required = make([]any, len(v))
		for i, s := range v {
			required[i] = s
		}
	default:
		required = nil
	}
	for _, req := range required {
		field, ok := req.(string)
		if !ok {
			continue
		}
		if _, exists := args[field]; !exists {
			return &SchemaValidationError{Field: field, Msg: "required field is missing"}
		}
	}

	// Validate properties
	props, _ := schema["properties"].(map[string]any)
	for field, value := range args {
		propSchema, ok := props[field].(map[string]any)
		if !ok {
			continue // no schema for this field = skip validation
		}
		if err := validateValue(field, value, propSchema); err != nil {
			return err
		}
	}

	return nil
}

func validateValue(field string, value any, schema map[string]any) error {
	expectedType, _ := schema["type"].(string)
	if expectedType == "" {
		return nil // no type specified = skip
	}

	switch expectedType {
	case "string":
		if _, ok := value.(string); !ok {
			return &SchemaValidationError{Field: field, Msg: "expected string, got " + typeName(value)}
		}
	case "number":
		if !isNumber(value) {
			return &SchemaValidationError{Field: field, Msg: "expected number, got " + typeName(value)}
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return &SchemaValidationError{Field: field, Msg: "expected boolean, got " + typeName(value)}
		}
	case "array":
		if reflect.TypeOf(value) == nil || reflect.TypeOf(value).Kind() != reflect.Slice {
			return &SchemaValidationError{Field: field, Msg: "expected array, got " + typeName(value)}
		}
		// Validate items if items schema present
		if itemsSchema, ok := schema["items"].(map[string]any); ok {
			arr := reflect.ValueOf(value)
			for i := 0; i < arr.Len(); i++ {
				if err := validateValue(field+"["+fmt.Sprint(i)+"]", arr.Index(i).Interface(), itemsSchema); err != nil {
					return err
				}
			}
		}
	case "object":
		if reflect.TypeOf(value) == nil || reflect.TypeOf(value).Kind() != reflect.Map {
			return &SchemaValidationError{Field: field, Msg: "expected object, got " + typeName(value)}
		}
		// Validate nested properties if present
		if props, ok := schema["properties"].(map[string]any); ok {
			obj := value.(map[string]any)
			for nestedField, nestedValue := range obj {
				if nestedSchema, ok := props[nestedField].(map[string]any); ok {
					if err := validateValue(field+"."+nestedField, nestedValue, nestedSchema); err != nil {
						return err
					}
				}
			}
		}
	default:
		return &SchemaValidationError{Field: field, Msg: "unsupported schema type: " + expectedType}
	}
	return nil
}

func isNumber(v any) bool {
	switch v.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return true
	default:
		return false
	}
}

func typeName(v any) string {
	if v == nil {
		return "nil"
	}
	return reflect.TypeOf(v).String()
}

// BuildPermsRequest converts tool args into a perms.Request based on the tool name.
func BuildPermsRequest(toolName string, args map[string]any) (perms.Request, error) {
	var req perms.Request
	switch toolName {
	case "fs_read":
		req.Kind = perms.KindFsRead
		path, _ := args["path"].(string)
		req.Path = path
		if path == "" {
			return req, errors.New("fs_read: path is required")
		}
		if offset, ok := args["offset"].(float64); ok {
			req.Offset = int64(offset)
		}
		if limit, ok := args["limit"].(float64); ok {
			req.Limit = int64(limit)
		}
	case "fs_write":
		req.Kind = perms.KindFsWrite
		path, _ := args["path"].(string)
		req.Path = path
		if path == "" {
			return req, errors.New("fs_write: path is required")
		}
		req.Content, _ = args["content"].(string)
		req.Encoding, _ = args["encoding"].(string)
		if req.Encoding == "" {
			req.Encoding = "utf8"
		}
		req.CreateDirs, _ = args["create_dirs"].(bool)
	case "fs_list":
		req.Kind = perms.KindFsRead // list is a read operation
		path, _ := args["path"].(string)
		req.Path = path
		if path == "" {
			return req, errors.New("fs_list: path is required")
		}
		req.Recursive, _ = args["recursive"].(bool)
		req.Pattern, _ = args["pattern"].(string)
	case "shell_exec":
		req.Kind = perms.KindShell
		cmd, _ := args["command"].(string)
		req.Command = cmd
		if cmd == "" {
			return req, errors.New("shell_exec: command is required")
		}
		if argsList, ok := args["args"].([]any); ok {
			req.Args = make([]string, len(argsList))
			for i, a := range argsList {
				req.Args[i], _ = a.(string)
			}
		}
		if timeout, ok := args["timeout_sec"].(float64); ok {
			req.TimeoutSec = int(timeout)
		}
		req.Workdir, _ = args["workdir"].(string)
	case "git":
		req.Kind = perms.KindGit
		subcmd, _ := args["subcommand"].(string)
		req.Subcommand = subcmd
		if subcmd == "" {
			return req, errors.New("git: subcommand is required")
		}
		if argsList, ok := args["args"].([]any); ok {
			req.GitArgs = make([]string, len(argsList))
			for i, a := range argsList {
				req.GitArgs[i], _ = a.(string)
			}
		}
		req.Workdir, _ = args["workdir"].(string)
	case "retrieval_search", "compaction_summarize", "anchoring_store", "anchoring_list", "anchoring_get", "anchoring_delete":
		// Internal harness tools (v1 features): they operate only on forge's
		// own SQLite store and forge's own LLM client, never on the host OS,
		// so the request carries just the kind plus the tool name (kept in
		// Command so the audit trail records a readable identifier). Tool
		// arguments are not permission-relevant.
		req.Kind = perms.KindCustom
		req.Command = toolName
	default:
		return req, fmt.Errorf("unknown tool: %s", toolName)
	}
	return req, nil
}
