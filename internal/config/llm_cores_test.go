package config

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestDefaultInferenceCores pins the RNF-1.6 default formula
// max(1, numCPU-2): local inference leaves a 2-core margin for the OS and
// the user by default, and small machines clamp at 1 rather than producing
// a zero or negative budget.
func TestDefaultInferenceCores(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		numCPU int
		want   int
	}{
		{name: "zero cores clamps to one", numCPU: 0, want: 1},
		{name: "single core clamps to one", numCPU: 1, want: 1},
		{name: "two cores keeps one for OS", numCPU: 2, want: 1},
		{name: "three cores leaves two margin", numCPU: 3, want: 1},
		{name: "four cores leaves two margin", numCPU: 4, want: 2},
		{name: "eight cores leaves two margin", numCPU: 8, want: 6},
		{name: "sixteen cores leaves two margin", numCPU: 16, want: 14},
		{name: "negative input clamps to one", numCPU: -4, want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := DefaultInferenceCores(tt.numCPU); got != tt.want {
				t.Errorf("DefaultInferenceCores(%d) = %d, want %d", tt.numCPU, got, tt.want)
			}
		})
	}
}

// TestValidate_LLMCores covers the llm.cores validation rules: 0/unset is
// always the valid "use the default budget" sentinel; negative values are
// rejected; a value equal to the machine's logical CPU count is allowed on
// purpose (explicit user override per RNF-1.6); anything above NumCPU is
// rejected because it cannot be honored.
func TestValidate_LLMCores(t *testing.T) {
	t.Parallel()
	numCPU := runtime.NumCPU()
	aboveNumCPU := numCPU + 1

	tests := []struct {
		name    string
		cores   int
		wantErr bool
	}{
		{name: "unset selects default", cores: 0, wantErr: false},
		{name: "one core below existing", cores: 1, wantErr: false},
		{name: "all cores explicitly allowed", cores: numCPU, wantErr: false},
		{name: "more cores than machine rejected", cores: aboveNumCPU, wantErr: true},
		{name: "negative rejected", cores: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := Defaults()
			cfg.LLM.Cores = tt.cores
			err := cfg.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() with llm.cores=%d error = %v, wantErr %v", tt.cores, err, tt.wantErr)
			}
		})
	}
}

// TestLoad_LLMCoresField pins the JSON path: llm.cores survives decode and
// merges into the config, and documents-with-cores parse under the current
// schema (unknown-field rejection would fail a typo'd key, not this one).
func TestLoad_LLMCoresField(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, path, `{
		"schema_version": 4,
		"llm": {"cores": 4}
	}`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.LLM.Cores != 4 {
		t.Errorf("llm.cores = %d, want 4", cfg.LLM.Cores)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate on loaded config: %v", err)
	}
}
