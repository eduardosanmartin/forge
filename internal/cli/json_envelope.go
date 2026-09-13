package cli

import (
	"encoding/json"
	"io"
)

// JSONEnvelope is the stable per-command machine-readable envelope mandated by
// RF-6.3. Every command that supports --json emits exactly one envelope on
// stdout; human output is untouched when --json is false.
//
// Shape:
//
//	{
//	  "ok": true,
//	  "command": "run",
//	  "result": { ... command-specific payload ... },
//	  "metadata": { "command": "run" }
//	}
//
// On error (transport/run failure rendered as JSON) ok is false and error is set:
//
//	{
//	  "ok": false,
//	  "command": "run",
//	  "error": "message",
//	  "metadata": { "command": "run" }
//	}
type JSONEnvelope struct {
	OK       bool           `json:"ok"`
	Command  string         `json:"command"`
	Result   any            `json:"result,omitempty"`
	Error    *string        `json:"error,omitempty"`
	Metadata map[string]any `json:"metadata,omitempty"`
}

func successEnvelope(command string, result any) *JSONEnvelope {
	return &JSONEnvelope{
		OK:       true,
		Command:  command,
		Result:   result,
		Metadata: map[string]any{"command": command},
	}
}

func errorEnvelope(command, msg string) *JSONEnvelope {
	return &JSONEnvelope{
		OK:       false,
		Command:  command,
		Error:    &msg,
		Metadata: map[string]any{"command": command},
	}
}

func writeEnvelope(out io.Writer, env *JSONEnvelope) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// writeJSONResultEnvelope writes a success envelope for command with result.
func writeJSONResultEnvelope(out io.Writer, command string, result any) error {
	return writeEnvelope(out, successEnvelope(command, result))
}

// writeJSONErrorEnvelope writes an error envelope for command.
func writeJSONErrorEnvelope(out io.Writer, command, msg string) error {
	return writeEnvelope(out, errorEnvelope(command, msg))
}
