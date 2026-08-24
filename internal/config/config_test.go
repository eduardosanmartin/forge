package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// writeConfigFile writes content to path with restrictive permissions,
// failing the test on any error.
func writeConfigFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config file %s: %v", path, err)
	}
}

func TestDefaultsPassValidation(t *testing.T) {
	cfg := Defaults()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Defaults() failed validation: %v", err)
	}
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", cfg.SchemaVersion, CurrentSchemaVersion)
	}
}

func TestLoadMissingFilesAreSkippedSilently(t *testing.T) {
	dir := t.TempDir()
	got, err := Load(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("Load with missing file returned error: %v", err)
	}
	want, err := ExpandPath("~/.forge/forge.db")
	if err != nil {
		t.Fatalf("ExpandPath: %v", err)
	}
	if got.Storage.Path != want {
		t.Errorf("Storage.Path = %q, want expanded default %q", got.Storage.Path, want)
	}
}

func TestLoadPrecedenceProjectOverGlobalOverDefaults(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global.json")
	projectPath := filepath.Join(dir, "project.json")

	writeConfigFile(t, globalPath, `{
		"logging": {"level": "debug", "file": "~/logs/global.log"},
		"providers": {
			"ollama": {"kind": "openai-compatible", "base_url": "https://global.example/v1", "models": ["g1"]},
			"team": {"kind": "openai-compatible", "base_url": "https://team.example/v1", "models": ["t1"]}
		}
	}`)
	writeConfigFile(t, projectPath, `{
		"default_provider": "ollama",
		"providers": {
			"ollama": {"kind": "openai-compatible", "base_url": "https://project.example/v1", "models": ["p1"]}
		},
		"logging": {"level": "error"}
	}`)

	got, err := Load(globalPath, projectPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.DefaultProvider != "ollama" {
		t.Errorf("DefaultProvider = %q, want %q", got.DefaultProvider, "ollama")
	}
	// Project logging section replaces the global one wholesale.
	if got.Logging.Level != "error" || got.Logging.File != "" {
		t.Errorf("Logging = %+v, want wholesale replacement {error \"\"}", got.Logging)
	}
	// Named provider replaced wholesale by the project file.
	p := got.Providers["ollama"]
	if p.BaseURL != "https://project.example/v1" || len(p.Models) != 1 || p.Models[0] != "p1" {
		t.Errorf("Providers[ollama] = %+v, want project entry wholesale", p)
	}
	// Global-only provider survives.
	if _, ok := got.Providers["team"]; !ok {
		t.Errorf("Providers[team] missing; global-only provider must survive")
	}
	// Defaults survive underneath both files.
	wantPath, err := ExpandPath("~/.forge/forge.db")
	if err != nil {
		t.Fatalf("ExpandPath: %v", err)
	}
	if got.Storage.Path != wantPath {
		t.Errorf("Storage.Path = %q, want default %q", got.Storage.Path, wantPath)
	}
}

func TestLoadUnknownFieldRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeConfigFile(t, path, `{"no_such_field": true}`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted an unknown field; want rejection")
	}
	for _, want := range []string{"no_such_field", "config.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
}

func TestLoadMalformedJSONIsHardError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.json")
	writeConfigFile(t, path, `{not json`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load accepted malformed JSON; want hard error")
	}
	if !strings.Contains(err.Error(), "broken.json") {
		t.Errorf("error %q does not name the offending path", err.Error())
	}
}

func TestLoadSchemaVersionRangeEnforced(t *testing.T) {
	cases := []struct {
		name    string
		doc     string
		version int
	}{
		{name: "zero below range", doc: `{"schema_version": 0}`, version: 0},
		{name: "two above current", doc: `{"schema_version": 2}`, version: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeConfigFile(t, path, tc.doc)

			_, err := Load(path)
			if err == nil {
				t.Fatalf("schema_version %d accepted; want rejection", tc.version)
			}
			msg := err.Error()
			for _, want := range []string{"config.json", "schema_version", strconv.Itoa(tc.version), "supported range"} {
				if !strings.Contains(msg, want) {
					t.Errorf("error %q does not mention %q", msg, want)
				}
			}
		})
	}

	t.Run("absent treated as current", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "config.json")
		writeConfigFile(t, path, `{}`)
		got, err := Load(path)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if got.SchemaVersion != CurrentSchemaVersion {
			t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, CurrentSchemaVersion)
		}
	})
}

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestExpandPathExpandsTilde(t *testing.T) {
	home := t.TempDir()
	setTestHome(t, home)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "tilde slash prefix", in: "~/.forge/forge.db", want: filepath.Join(home, ".forge", "forge.db")},
		{name: "bare tilde", in: "~", want: home},
		{name: "relative untouched", in: ".forge/config.json", want: ".forge/config.json"},
		{name: "empty untouched", in: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandPath(tc.in)
			if err != nil {
				t.Fatalf("ExpandPath(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExpandPathEmptyHomeErrorsCleanly(t *testing.T) {
	setTestHome(t, "")

	if _, err := ExpandPath("~/x"); err == nil {
		t.Fatal("ExpandPath succeeded without a home directory; want clean error")
	} else if !strings.Contains(err.Error(), "home directory") {
		t.Errorf("error %q should explain the missing home directory", err.Error())
	}
}

func TestLoadEmptyHomeErrorsCleanly(t *testing.T) {
	setTestHome(t, "")
	path := filepath.Join(t.TempDir(), "config.json")
	writeConfigFile(t, path, `{"storage": {"path": "~/.forge/forge.db"}}`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("Load succeeded without a home directory; want error")
	}
	if !strings.Contains(err.Error(), "storage.path") {
		t.Errorf("error %q does not name the failing field storage.path", err.Error())
	}
}

func TestSaveLoadRoundTripEquality(t *testing.T) {
	cfg := Defaults()
	cfg.Logging.Level = "debug"
	cfg.Logging.File = "~/logs/forge.log"
	cfg.Storage.Path = "~/.forge/custom.db"
	cfg.Network.AllowedHosts = []string{"127.0.0.1"}
	cfg.Providers["remote"] = Provider{
		Kind:    "openai-compatible",
		BaseURL: "https://api.example.com/v1",
		Models:  []string{"m1", "m2"},
	}

	path := filepath.Join(t.TempDir(), "nested", "dir", "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save into non-existent parent dirs: %v", err)
	}

	// Normalize the in-memory side the same way Load normalizes documents:
	// expand "~" in path fields so both structs are directly comparable.
	if err := cfg.expandPaths(); err != nil {
		t.Fatalf("normalize saved config paths: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg, got) {
		t.Errorf("round trip mismatch:\nsaved:  %+v\nloaded: %+v", cfg, got)
	}
}

func TestSaveAtomicNoTempResidue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")

	if err := Defaults().Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	residue, err := filepath.Glob(filepath.Join(dir, "*.tmp*"))
	if err != nil {
		t.Fatalf("glob residue: %v", err)
	}
	if len(residue) != 0 {
		t.Errorf("temp residue left behind: %v", residue)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("final config file missing after Save: %v", err)
	}
}

// mutate applies f to a fresh copy of Defaults.
func mutate(f func(*Config)) *Config {
	c := Defaults()
	f(c)
	return c
}

func TestValidateViolations(t *testing.T) {
	cases := []struct {
		name    string
		cfg     *Config
		wantErr string
		wantOK  bool
	}{
		{
			name:   "defaults valid",
			cfg:    Defaults(),
			wantOK: true,
		},
		{
			name:    "empty default provider",
			cfg:     mutate(func(c *Config) { c.DefaultProvider = "" }),
			wantErr: "default_provider",
		},
		{
			name:    "default provider not in providers",
			cfg:     mutate(func(c *Config) { c.DefaultProvider = "missing" }),
			wantErr: "default_provider",
		},
		{
			name: "wrong provider kind",
			cfg: mutate(func(c *Config) {
				p := c.Providers["ollama"]
				p.Kind = "grpc"
				c.Providers["ollama"] = p
			}),
			wantErr: "kind",
		},
		{
			name: "non-http base url scheme",
			cfg: mutate(func(c *Config) {
				p := c.Providers["ollama"]
				p.BaseURL = "ftp://127.0.0.1:11434/v1"
				c.Providers["ollama"] = p
			}),
			wantErr: "base_url",
		},
		{
			name: "base url missing host",
			cfg: mutate(func(c *Config) {
				p := c.Providers["ollama"]
				p.BaseURL = "http:///v1"
				c.Providers["ollama"] = p
			}),
			wantErr: "host",
		},
		{
			name: "provider without models",
			cfg: mutate(func(c *Config) {
				p := c.Providers["ollama"]
				p.Models = nil
				c.Providers["ollama"] = p
			}),
			wantErr: "model",
		},
		{
			name:    "empty storage path",
			cfg:     mutate(func(c *Config) { c.Storage.Path = "" }),
			wantErr: "storage.path",
		},
		{
			name:    "invalid log level",
			cfg:     mutate(func(c *Config) { c.Logging.Level = "verbose" }),
			wantErr: "logging.level",
		},
		{
			name:    "blank allowed host entry",
			cfg:     mutate(func(c *Config) { c.Network.AllowedHosts = append(c.Network.AllowedHosts, "") }),
			wantErr: "allowed_hosts",
		},
		{
			name:    "whitespace allowed host entry",
			cfg:     mutate(func(c *Config) { c.Network.AllowedHosts = []string{"  "} }),
			wantErr: "allowed_hosts",
		},
		{
			name: "aggregates multiple violations",
			cfg: mutate(func(c *Config) {
				c.DefaultProvider = ""
				c.Storage.Path = ""
			}),
			wantErr: "default_provider",
		},
		{
			name:   "empty allowed hosts denies all egress legally",
			cfg:    mutate(func(c *Config) { c.Network.AllowedHosts = []string{} }),
			wantOK: true,
		},
		{
			name:   "empty log level normalized to info",
			cfg:    mutate(func(c *Config) { c.Logging.Level = "" }),
			wantOK: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if tc.wantOK {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want violation")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}

	t.Run("aggregation lists all violations", func(t *testing.T) {
		cfg := mutate(func(c *Config) {
			c.DefaultProvider = ""
			c.Storage.Path = ""
			c.Logging.Level = "verbose"
		})
		err := cfg.Validate()
		if err == nil {
			t.Fatal("Validate() = nil, want aggregated violations")
		}
		for _, want := range []string{"default_provider", "storage.path", "logging.level"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("aggregated error %q missing violation %q", err.Error(), want)
			}
		}
	})

	t.Run("normalization mutates level in place", func(t *testing.T) {
		cfg := mutate(func(c *Config) { c.Logging.Level = "" })
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate(): %v", err)
		}
		if cfg.Logging.Level != "info" {
			t.Errorf("Logging.Level = %q, want normalized \"info\"", cfg.Logging.Level)
		}
	})
}

func TestMigrateScaffold(t *testing.T) {
	data := []byte(`{}`)

	got, err := Migrate(data, CurrentSchemaVersion)
	if err != nil {
		t.Fatalf("Migrate(current): %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Migrate(current) altered data: %q", got)
	}

	_, err = Migrate(data, CurrentSchemaVersion+1)
	if err == nil || !strings.Contains(err.Error(), "no migration path") {
		t.Errorf("Migrate(unknown future version) = %v, want \"no migration path\" error", err)
	}

	_, err = Migrate(data, 0)
	if err == nil || !strings.Contains(err.Error(), "no migration path") {
		t.Errorf("Migrate(0) = %v, want \"no migration path\" error", err)
	}
}
