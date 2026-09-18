package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eduardosanmartin/forge/internal/daemon"
)

func TestSetDaemonAuthTokenHashPreservesUnrelatedKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	original := `{
  "default_provider": "openrouter",
  "providers": {
    "openrouter": {"kind": "openai-compatible", "base_url": "https://openrouter.ai/api/v1", "api_key": "sk-should-not-move"}
  },
  "schema_version": 4
}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	hash := daemon.HashToken("s3cret")
	if err := setDaemonAuthTokenHash(path, hash); err != nil {
		t.Fatalf("setDaemonAuthTokenHash: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !strings.Contains(string(data), "sk-should-not-move") {
		t.Error("unrelated api_key should be preserved untouched")
	}
	if doc["default_provider"] != "openrouter" {
		t.Errorf("default_provider changed: %v", doc["default_provider"])
	}
	dsec, ok := doc["daemon"].(map[string]any)
	if !ok {
		t.Fatalf("expected a daemon section, got %+v", doc["daemon"])
	}
	if dsec["auth_token_hash"] != hash {
		t.Errorf("auth_token_hash = %v, want %v", dsec["auth_token_hash"], hash)
	}

	// Clearing removes the key (and the whole section, since it's now empty)
	// without touching anything else.
	if err := setDaemonAuthTokenHash(path, ""); err != nil {
		t.Fatalf("clear: %v", err)
	}
	data2, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back after clear: %v", err)
	}
	var doc2 map[string]any
	if err := json.Unmarshal(data2, &doc2); err != nil {
		t.Fatalf("unmarshal after clear: %v", err)
	}
	if _, present := doc2["daemon"]; present {
		t.Errorf("expected the now-empty daemon section to be removed, got %v", doc2["daemon"])
	}
	if !strings.Contains(string(data2), "sk-should-not-move") {
		t.Error("unrelated api_key should still be preserved after clearing the token")
	}
}

func TestSetDaemonAuthTokenHashCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.json")
	hash := daemon.HashToken("s3cret")
	if err := setDaemonAuthTokenHash(path, hash); err != nil {
		t.Fatalf("setDaemonAuthTokenHash on missing file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	dsec, ok := doc["daemon"].(map[string]any)
	if !ok || dsec["auth_token_hash"] != hash {
		t.Fatalf("unexpected doc: %+v", doc)
	}
}

func TestReadPasswordLine(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{"with trailing newline", "s3cret\n", "s3cret"},
		{"without trailing newline (printf -n style)", "s3cret", "s3cret"},
		{"crlf", "s3cret\r\n", "s3cret"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readPasswordLine(strings.NewReader(c.input))
			if err != nil {
				t.Fatalf("readPasswordLine: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
