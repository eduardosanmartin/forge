package plugin

import (
	"strings"
	"testing"
)

// validBase returns a minimal valid Manifest for mutation in tests.
func validBase() Manifest {
	return Manifest{
		Name:        "my_plugin",
		Version:     "0.1.0",
		Description: "A plugin.",
		Source:      SourceLocal,
		Entrypoint:  "plugin.wasm",
		Permissions: []string{"fs.read"},
		Tools: []ToolExport{
			{Name: "my_plugin_greet", Description: "Greets", Permission: "fs.read"},
		},
	}
}

func TestValidateManifest(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr bool
		wantSub []string // substrings that must appear in error when wantErr
	}{
		{
			name:    "valid manifest",
			mutate:  func(m *Manifest) {},
			wantErr: false,
		},
		// Name rule
		{
			name: "name empty",
			mutate: func(m *Manifest) { m.Name = "" },
			wantErr: true, wantSub: []string{"name"},
		},
		{
			name: "name uppercase invalid",
			mutate: func(m *Manifest) { m.Name = "MyPlugin" },
			wantErr: true, wantSub: []string{"name"},
		},
		{
			name: "name with dot",
			mutate: func(m *Manifest) { m.Name = "my.plugin" },
			wantErr: true, wantSub: []string{"name"},
		},
		{
			name: "name too short (1 char)",
			mutate: func(m *Manifest) { m.Name = "a" },
			wantErr: true, wantSub: []string{"name"},
		},
		// Version rule
		{
			name: "version empty",
			mutate: func(m *Manifest) { m.Version = "" },
			wantErr: true, wantSub: []string{"version"},
		},
		{
			name: "version missing patch",
			mutate: func(m *Manifest) { m.Version = "0.1" },
			wantErr: true, wantSub: []string{"version"},
		},
		{
			name: "version leading zero",
			mutate: func(m *Manifest) { m.Version = "01.0.0" },
			wantErr: true, wantSub: []string{"leading zeros"},
		},
		{
			name: "version invalid prerelease chars",
			mutate: func(m *Manifest) { m.Version = "0.1.0-!!!" },
			wantErr: true, wantSub: []string{"version"},
		},
		{
			name: "version valid prerelease",
			mutate: func(m *Manifest) { m.Version = "1.0.0-alpha.1" },
			wantErr: false,
		},
		// Description
		{
			name: "description empty",
			mutate: func(m *Manifest) { m.Description = "" },
			wantErr: true, wantSub: []string{"description"},
		},
		{
			name: "description whitespace only",
			mutate: func(m *Manifest) { m.Description = "   " },
			wantErr: true, wantSub: []string{"description"},
		},
		// Source
		{
			name: "source invalid",
			mutate: func(m *Manifest) { m.Source = "remote" },
			wantErr: true, wantSub: []string{"source"},
		},
		{
			name: "source external valid with checksum",
			mutate: func(m *Manifest) {
				m.Source = SourceExternal
				m.Checksum = "sha256:" + strings.Repeat("a", 64)
			},
			wantErr: false,
		},
		// Entrypoint
		{
			name: "entrypoint empty",
			mutate: func(m *Manifest) { m.Entrypoint = "" },
			wantErr: true, wantSub: []string{"entrypoint"},
		},
		{
			name: "entrypoint not wasm",
			mutate: func(m *Manifest) { m.Entrypoint = "plugin.wasi" },
			wantErr: true, wantSub: []string{"entrypoint"},
		},
		{
			name: "entrypoint absolute posix",
			mutate: func(m *Manifest) { m.Entrypoint = "/abs/plugin.wasm" },
			wantErr: true, wantSub: []string{"absolute"},
		},
		{
			name: "entrypoint absolute windows",
			mutate: func(m *Manifest) { m.Entrypoint = "C:/plugin.wasm" },
			wantErr: true, wantSub: []string{"absolute"},
		},
		{
			name: "entrypoint with dotdot",
			mutate: func(m *Manifest) { m.Entrypoint = "../plugin.wasm" },
			wantErr: true, wantSub: []string{"\"..\""},
		},
		{
			name: "entrypoint relative with dot slash allowed",
			mutate: func(m *Manifest) { m.Entrypoint = "./dir/plugin.wasm" },
			wantErr: false,
		},
		{
			name: "entrypoint nested relative allowed",
			mutate: func(m *Manifest) { m.Entrypoint = "dir/sub/plugin.wasm" },
			wantErr: false,
		},
		// Permissions
		{
			name: "permission invalid vocabulary",
			mutate: func(m *Manifest) { m.Permissions = []string{"fs.read", "admin"} },
			wantErr: true, wantSub: []string{"permissions[1]"},
		},
		{
			name: "permission duplicate",
			mutate: func(m *Manifest) { m.Permissions = []string{"fs.read", "fs.read"} },
			wantErr: true, wantSub: []string{"duplicate permission"},
		},
		{
			name: "permissions empty but tools present",
			mutate: func(m *Manifest) { m.Permissions = []string{} },
			wantErr: true, wantSub: []string{"permissions"},
		},
		{
			name: "permissions net valid",
			mutate: func(m *Manifest) { m.Permissions = []string{"net"}; m.Tools[0].Permission = "net" },
			wantErr: false,
		},
		// Tools: name prefix and charset
		{
			name: "tool name missing prefix",
			mutate: func(m *Manifest) { m.Tools[0].Name = "other_greet" },
			wantErr: true, wantSub: []string{"prefixed"},
		},
		{
			name: "tool name with dot",
			mutate: func(m *Manifest) { m.Tools[0].Name = "my_plugin_greet.tool" },
			wantErr: true, wantSub: []string{"must match"},
		},
		{
			name: "tool name uppercase",
			mutate: func(m *Manifest) { m.Tools[0].Name = "my_plugin_Greet" },
			wantErr: true, wantSub: []string{"must match"},
		},
		{
			name: "tool name duplicate",
			mutate: func(m *Manifest) {
				m.Tools = append(m.Tools, ToolExport{Name: "my_plugin_greet", Description: "dup", Permission: "fs.read"})
			},
			wantErr: true, wantSub: []string{"duplicate"},
		},
		{
			name: "tool name suffix empty after prefix",
			mutate: func(m *Manifest) { m.Tools[0].Name = "my_plugin_" },
			wantErr: true, wantSub: []string{"suffix after prefix"},
		},
		{
			name: "tool description empty",
			mutate: func(m *Manifest) { m.Tools[0].Description = "" },
			wantErr: true, wantSub: []string{"description must not be empty"},
		},
		{
			name: "tool permission not in manifest",
			mutate: func(m *Manifest) { m.Tools[0].Permission = "net" },
			wantErr: true, wantSub: []string{"must be declared"},
		},
		{
			name: "tool permission empty",
			mutate: func(m *Manifest) { m.Tools[0].Permission = "" },
			wantErr: true, wantSub: []string{"permission must not be empty"},
		},
		// Dependencies
		{
			name: "dependency invalid name",
			mutate: func(m *Manifest) { m.Dependencies = []string{"Bad-Name"} },
			wantErr: true, wantSub: []string{"dependencies[0]"},
		},
		{
			name: "dependency self",
			mutate: func(m *Manifest) { m.Dependencies = []string{"my_plugin"} },
			wantErr: true, wantSub: []string{"self-dependency"},
		},
		{
			name: "dependency duplicate",
			mutate: func(m *Manifest) { m.Dependencies = []string{"other_plugin", "other_plugin"} },
			wantErr: true, wantSub: []string{"duplicate dependency"},
		},
		{
			name: "dependency valid",
			mutate: func(m *Manifest) { m.Dependencies = []string{"other_plugin"} },
			wantErr: false,
		},
		// Checksum
		{
			name: "checksum required for external",
			mutate: func(m *Manifest) { m.Source = SourceExternal; m.Checksum = "" },
			wantErr: true, wantSub: []string{"checksum: required"},
		},
		{
			name: "checksum bad format missing prefix",
			mutate: func(m *Manifest) { m.Source = SourceExternal; m.Checksum = strings.Repeat("a", 64) },
			wantErr: true, wantSub: []string{"checksum"},
		},
		{
			name: "checksum bad uppercase hex",
			mutate: func(m *Manifest) { m.Source = SourceExternal; m.Checksum = "sha256:" + strings.Repeat("A", 64) },
			wantErr: true, wantSub: []string{"checksum"},
		},
		{
			name: "checksum present for local rejected",
			mutate: func(m *Manifest) { m.Source = SourceLocal; m.Checksum = "sha256:" + strings.Repeat("a", 64) },
			wantErr: true, wantSub: []string{"checksum: must not be set"},
		},
		{
			name: "checksum valid external",
			mutate: func(m *Manifest) { m.Source = SourceExternal; m.Checksum = "sha256:" + strings.Repeat("b", 64) },
			wantErr: false,
		},
		// Multiple violations aggregated
		{
			name: "multiple violations aggregated",
			mutate: func(m *Manifest) {
				m.Name = "Bad.Name"
				m.Version = "01.0.0"
				m.Description = ""
			},
			wantErr: true,
			wantSub: []string{"name", "leading zeros", "description"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := validBase()
			tc.mutate(&m)
			err := validateManifest(&m)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("validateManifest failed; want success: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateManifest succeeded; want error")
			}
			for _, sub := range tc.wantSub {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q does not contain %q", err.Error(), sub)
				}
			}
		})
	}

	t.Run("errors.Join aggregates multiple violations mentioned", func(t *testing.T) {
		m := validBase()
		m.Name = "Bad!"
		m.Version = "bad"
		err := validateManifest(&m)
		if err == nil {
			t.Fatal("want error")
		}
		if !strings.Contains(err.Error(), "name") {
			t.Errorf("aggregated error missing name violation: %q", err.Error())
		}
		if !strings.Contains(err.Error(), "version") {
			t.Errorf("aggregated error missing version violation: %q", err.Error())
		}
	})
}
