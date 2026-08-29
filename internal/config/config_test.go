package config

import (
	"bytes"
	"encoding/json"
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
		{name: "four above current", doc: `{"schema_version": 5}`, version: 5},
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

func TestMigrateCurrentVersionIsIdentity(t *testing.T) {
	data := []byte(`{"schema_version": 2}`)

	got, err := Migrate(data, CurrentSchemaVersion)
	if err != nil {
		t.Fatalf("Migrate(current): %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Migrate(current) altered data: %q", got)
	}
}

func TestMigrateUnknownVersionsError(t *testing.T) {
	for _, from := range []int{0, CurrentSchemaVersion + 1} {
		_, err := Migrate([]byte(`{}`), from)
		if err == nil || !strings.Contains(err.Error(), "no migration path") {
			t.Errorf("Migrate(from=%d) = %v, want \"no migration path\" error", from, err)
		}
	}
}

func TestMigrateV1ToV2InjectsDefaultPermissions(t *testing.T) {
	v1 := []byte(`{
		"schema_version": 1,
		"default_provider": "ollama",
		"storage": {"path": "~/.forge/custom.db"}
	}`)

	migrated, err := Migrate(v1, 1)
	if err != nil {
		t.Fatalf("Migrate(v1): %v", err)
	}

	var cfg Config
	if err := json.Unmarshal(migrated, &cfg); err != nil {
		t.Fatalf("unmarshal migrated document: %v", err)
	}

	want := defaultPermissionsPolicy()
	if !reflect.DeepEqual(cfg.Permissions, want) {
		t.Errorf("migrated permissions = %+v, want default policy %+v", cfg.Permissions, want)
	}

	t.Run("preserves other v1 fields", func(t *testing.T) {
		if cfg.DefaultProvider != "ollama" {
			t.Errorf("DefaultProvider = %q, want %q", cfg.DefaultProvider, "ollama")
		}
		if cfg.Storage.Path != "~/.forge/custom.db" {
			t.Errorf("Storage.Path = %q, want preserved v1 value", cfg.Storage.Path)
		}
	})

	t.Run("document already carrying permissions is untouched", func(t *testing.T) {
		custom := []byte(`{"schema_version": 1, "permissions": {"shell": {"allow": ["go"]}}}`)
		got, err := Migrate(custom, 1)
		if err != nil {
			t.Fatalf("Migrate: %v", err)
		}
		var doc map[string]any
		if err := json.Unmarshal(got, &doc); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		perms, ok := doc["permissions"].(map[string]any)
		if !ok {
			t.Fatalf("permissions missing or wrong shape: %v", doc["permissions"])
		}
		shell, _ := perms["shell"].(map[string]any)
		allow, _ := shell["allow"].([]any)
		if len(allow) != 1 || allow[0] != "go" {
			t.Errorf("custom permissions clobbered by migration: %v", perms)
		}
	})
}

func TestLoadMigratesV1DocumentToCurrentSemantics(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v1.json")
	writeConfigFile(t, path, `{
		"schema_version": 1,
		"default_provider": "ollama"
	}`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(v1 document): %v", err)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d after successful migration", got.SchemaVersion, CurrentSchemaVersion)
	}
	if want := defaultPermissionsPolicy(); !reflect.DeepEqual(got.Permissions, want) {
		t.Errorf("Permissions = %+v, want injected defaults %+v", got.Permissions, want)
	}
}

func TestDefaultsPermissionsDenyByDefault(t *testing.T) {
	perms := Defaults().Permissions
	if len(perms.Shell.Allow) != 0 {
		t.Errorf("default shell.allow = %v, want EMPTY list (deny-by-default, RNF-4.1)", perms.Shell.Allow)
	}
	if len(perms.FS.Read) != 1 || perms.FS.Read[0] != "./**" {
		t.Errorf("default fs.read = %v, want [./**]", perms.FS.Read)
	}
	if len(perms.FS.Write) != 1 || perms.FS.Write[0] != "./**" {
		t.Errorf("default fs.write = %v, want [./**]", perms.FS.Write)
	}
	wantGit := []string{
		"status", "add", "commit", "log", "diff", "branch",
		"switch", "stash", "restore", "show", "remote", "fetch",
	}
	if !reflect.DeepEqual(perms.Git.Allow, wantGit) {
		t.Errorf("default git.allow = %v, want %v", perms.Git.Allow, wantGit)
	}
}

func TestPermissionsMergeGroupWise(t *testing.T) {
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "global.json")
	projectPath := filepath.Join(dir, "project.json")

	writeConfigFile(t, globalPath, `{
		"permissions": {
			"fs": {"read": ["src/**"], "write": ["build/"]},
			"shell": {"allow": ["go"]},
			"git": {"allow": ["status"]}
		}
	}`)
	// Project overrides ONLY the shell group: fs and git must survive from
	// the global file untouched.
	writeConfigFile(t, projectPath, `{
		"permissions": {"shell": {"allow": ["npm", "make"]}}
	}`)

	got, err := Load(globalPath, projectPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantFS := FSPermissions{Read: []string{"src/**"}, Write: []string{"build/"}}
	if !reflect.DeepEqual(got.Permissions.FS, wantFS) {
		t.Errorf("merged fs = %+v, want global value %+v", got.Permissions.FS, wantFS)
	}
	wantShell := ShellPermissions{Allow: []string{"npm", "make"}}
	if !reflect.DeepEqual(got.Permissions.Shell, wantShell) {
		t.Errorf("merged shell = %+v, want project replacement %+v", got.Permissions.Shell, wantShell)
	}
	wantGit := GitPermissions{Allow: []string{"status"}}
	if !reflect.DeepEqual(got.Permissions.Git, wantGit) {
		t.Errorf("merged git = %+v, want global value %+v", got.Permissions.Git, wantGit)
	}
}

func TestPermissionsDefaultsSurvivePartialProjectOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "project.json")
	writeConfigFile(t, path, `{"permissions": {"fs": {"read": ["./**"], "write": ["out/"]}}}`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	wantFS := FSPermissions{Read: []string{"./**"}, Write: []string{"out/"}}
	if !reflect.DeepEqual(got.Permissions.FS, wantFS) {
		t.Errorf("fs = %+v, want project replacement %+v", got.Permissions.FS, wantFS)
	}
	if want := defaultPermissionsPolicy().Git; !reflect.DeepEqual(got.Permissions.Git, want) {
		t.Errorf("git = %+v, want untouched default %+v", got.Permissions.Git, want)
	}
	if len(got.Permissions.Shell.Allow) != 0 {
		t.Errorf("shell = %v, want untouched empty default", got.Permissions.Shell.Allow)
	}
}

func TestValidateRejectsInvalidPermissionPatterns(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			name:    "backslash in fs.read",
			mutate:  func(c *Config) { c.Permissions.FS.Read = []string{`a\b`} },
			wantErr: `permissions.fs.read[0]`,
		},
		{
			name:    "dotdot in fs.write",
			mutate:  func(c *Config) { c.Permissions.FS.Write = []string{"ok", ".."} },
			wantErr: `permissions.fs.write[1]`,
		},
		{
			name:    "empty shell allow entry",
			mutate:  func(c *Config) { c.Permissions.Shell.Allow = []string{"go", ""} },
			wantErr: `permissions.shell.allow[1]`,
		},
		{
			name:    "empty git allow entry",
			mutate:  func(c *Config) { c.Permissions.Git.Allow = []string{""} },
			wantErr: `permissions.git.allow[0]`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := mutate(tc.mutate)
			err := cfg.Validate()
			if err == nil {
				t.Fatal("Validate() = nil, want violation")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErr)
			}
		})
	}

	t.Run("invalid pattern in project config surfaces via Load+Validate", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "project.json")
		writeConfigFile(t, path, `{
			"permissions": {"fs": {"read": ["src//**"], "write": ["./**"]}}
		}`)

		cfg, err := Load(path)
		if err != nil {
			t.Fatalf("Load (validation is a separate step): %v", err)
		}
		err = cfg.Validate()
		if err == nil {
			t.Fatal("Validate() = nil for config with invalid pattern; want clear error")
		}
		for _, want := range []string{"permissions.fs.read[0]", `"src//**"`} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not mention %q", err.Error(), want)
			}
		}
	})

	t.Run("defaults still validate cleanly with permissions", func(t *testing.T) {
		if err := Defaults().Validate(); err != nil {
			t.Errorf("Defaults().Validate() = %v, want nil", err)
		}
	})
}

func TestSaveLoadRoundTripsPermissions(t *testing.T) {
	cfg := Defaults()
	cfg.Permissions.FS.Write = []string{"build/"}
	cfg.Permissions.Shell.Allow = []string{"go", "npm"}
	cfg.Permissions.Git.Allow = []string{"status"}

	path := filepath.Join(t.TempDir(), "config.json")
	if err := cfg.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(cfg.Permissions, got.Permissions) {
		t.Errorf("permissions round trip mismatch:\nsaved:  %+v\nloaded: %+v", cfg.Permissions, got.Permissions)
	}
}

// --- Schema v3: permissions.shell.require_isolation (RNF-4.7) --------------

func TestDefaultsRequireIsolationTrue(t *testing.T) {
	shell := Defaults().Permissions.Shell
	if !shell.RequireIsolation {
		t.Error("Defaults() must require shell isolation on capable platforms (RNF-4.7)")
	}
}

func TestMigrateV2ToV3InjectsRequireIsolation(t *testing.T) {
	v2 := []byte(`{
		"schema_version": 2,
		"default_provider": "ollama",
		"providers": {"ollama": {"kind": "openai-compatible", "base_url": "http://127.0.0.1:11434/v1", "models": ["m"]}},
		"storage": {"path": "~/.forge/custom.db"},
		"permissions": {
			"fs": {"read": ["src/**"], "write": ["build/"]},
			"shell": {"allow": ["go", "make"]},
			"git": {"allow": ["status"]}
		}
	}`)

	migrated, err := Migrate(v2, 2)
	if err != nil {
		t.Fatalf("Migrate(v2): %v", err)
	}

	var cfg Config
	if err := json.Unmarshal(migrated, &cfg); err != nil {
		t.Fatalf("unmarshal migrated document: %v", err)
	}

	shell := cfg.Permissions.Shell
	if !shell.RequireIsolation {
		t.Error("migration must inject require_isolation=true for pre-v3 documents")
	}
	if !reflect.DeepEqual(shell.Allow, []string{"go", "make"}) {
		t.Errorf("allow = %v, want preserved [go make]", shell.Allow)
	}
	wantFS := FSPermissions{Read: []string{"src/**"}, Write: []string{"build/"}}
	if !reflect.DeepEqual(cfg.Permissions.FS, wantFS) {
		t.Errorf("fs = %+v, want untouched %+v", cfg.Permissions.FS, wantFS)
	}
	if !reflect.DeepEqual(cfg.Permissions.Git.Allow, []string{"status"}) {
		t.Errorf("git.allow = %v, want untouched [status]", cfg.Permissions.Git.Allow)
	}
	if cfg.DefaultProvider != "ollama" || cfg.Storage.Path != "~/.forge/custom.db" {
		t.Errorf("other fields not preserved verbatim: provider=%q storage=%q",
			cfg.DefaultProvider, cfg.Storage.Path)
	}
}

func TestMigrateV2ToV3PreservesExplicitFalse(t *testing.T) {
	v2 := []byte(`{"schema_version": 2, "permissions": {"shell": {"allow": [], "require_isolation": false}}}`)

	migrated, err := Migrate(v2, 2)
	if err != nil {
		t.Fatalf("Migrate(v2): %v", err)
	}

	var cfg Config
	if err := json.Unmarshal(migrated, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.Permissions.Shell.RequireIsolation {
		t.Error("explicit require_isolation=false was clobbered by migration")
	}
}

func TestMigrateV2ToV3WithoutPermissionsUntouched(t *testing.T) {
	v2 := []byte(`{"schema_version": 2, "default_provider": "ollama"}`)

	migrated, err := Migrate(v2, 2)
	if err != nil {
		t.Fatalf("Migrate(v2): %v", err)
	}
	if string(migrated) != string(v2) {
		t.Errorf("document without permissions section changed: %s", migrated)
	}

	// Merging still yields the secure default.
	var fc fileConfig
	dec := json.NewDecoder(bytes.NewReader(migrated))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&fc); err != nil {
		t.Fatalf("decode migrated doc: %v", err)
	}
	dst := Defaults()
	mergeInto(dst, &fc)
	if !dst.Permissions.Shell.RequireIsolation {
		t.Error("merged config lost the default require_isolation=true")
	}
}

func TestMigrateV1ToCurrentChainsToRequireIsolation(t *testing.T) {
	v1 := []byte(`{"schema_version": 1, "default_provider": "ollama"}`)

	migrated, err := Migrate(v1, 1)
	if err != nil {
		t.Fatalf("Migrate(v1): %v", err)
	}

	var cfg Config
	if err := json.Unmarshal(migrated, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !cfg.Permissions.Shell.RequireIsolation {
		t.Error("v1 -> current chain must end with require_isolation=true")
	}
	if len(cfg.Permissions.FS.Read) != 1 || cfg.Permissions.FS.Read[0] != "./**" {
		t.Errorf("chain did not inject default fs.read: %+v", cfg.Permissions.FS)
	}
}

func TestLoadUpgradesV2DocumentInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "v2.json")
	writeConfigFile(t, path, `{
		"schema_version": 2,
		"default_provider": "ollama",
		"permissions": {"shell": {"allow": ["go"]}}
	}`)

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load(v2 document): %v", err)
	}
	if got.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d after migration", got.SchemaVersion, CurrentSchemaVersion)
	}
	if !got.Permissions.Shell.RequireIsolation {
		t.Error("loaded v2 document did not gain require_isolation=true")
	}
	if want := []string{"go"}; !reflect.DeepEqual(got.Permissions.Shell.Allow, want) {
		t.Errorf("allow = %v, want %v", got.Permissions.Shell.Allow, want)
	}
}

func TestLoadRejectsUnknownFieldOnCurrentSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	writeConfigFile(t, path, `{"schema_version": 3, "permissions": {"shell": {"require_isolaton": true}}}`)

	_, err := Load(path)
	if err == nil {
		t.Fatal("typo'd field accepted; unknown-field rejection regressed in v3")
	}
	if !strings.Contains(err.Error(), "require_isolaton") {
		t.Errorf("error %q does not name the typo'd field", err.Error())
	}
}
