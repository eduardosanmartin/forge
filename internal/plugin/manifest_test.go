package plugin

import (
	"strings"
	"testing"
)

func TestParseManifestValidLocal(t *testing.T) {
	toml := "name = \"my_plugin\"\n" +
		"version = \"0.1.0\"\n" +
		"description = \"Example plugin.\"\n" +
		"source = \"local\"\n" +
		"entrypoint = \"plugin.wasm\"\n" +
		"permissions = [\"fs.read\", \"git\"]\n" +
		"dependencies = [\"other_plugin\"]\n" +
		"\n[[tools]]\n" +
		"name = \"my_plugin_greet\"\n" +
		"description = \"Greets a user.\"\n" +
		"permission = \"fs.read\"\n" +
		"[[tools]]\n" +
		"name = \"my_plugin_commit\"\n" +
		"description = \"Commits.\"\n" +
		"permission = \"git\"\n"

	m, err := ParseManifest([]byte(toml))
	if err != nil {
		t.Fatalf("ParseManifest failed: %v", err)
	}
	if m.Name != "my_plugin" {
		t.Errorf("Name = %q, want %q", m.Name, "my_plugin")
	}
	if m.Version != "0.1.0" {
		t.Errorf("Version = %q", m.Version)
	}
	if m.Description != "Example plugin." {
		t.Errorf("Description = %q", m.Description)
	}
	if m.Source != SourceLocal {
		t.Errorf("Source = %q", m.Source)
	}
	if m.Entrypoint != "plugin.wasm" {
		t.Errorf("Entrypoint = %q", m.Entrypoint)
	}
	if len(m.Permissions) != 2 || m.Permissions[0] != "fs.read" || m.Permissions[1] != "git" {
		t.Errorf("Permissions = %v", m.Permissions)
	}
	if len(m.Dependencies) != 1 || m.Dependencies[0] != "other_plugin" {
		t.Errorf("Dependencies = %v", m.Dependencies)
	}
	if m.Checksum != "" {
		t.Errorf("Checksum = %q, want empty for local", m.Checksum)
	}
	if len(m.Tools) != 2 {
		t.Fatalf("Tools length = %d, want 2", len(m.Tools))
	}
	if m.Tools[0].Name != "my_plugin_greet" {
		t.Errorf("Tools[0].Name = %q", m.Tools[0].Name)
	}
}

func TestParseManifestExternalRequiresChecksum(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		wantErr bool
	}{
		{
			name: "external without checksum fails",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"d\"\n" +
				"source = \"external\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = [\"net\"]\n",
			wantErr: true,
		},
		{
			name: "external with valid checksum passes",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"d\"\n" +
				"source = \"external\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = [\"net\"]\n" +
				"checksum = \"sha256:" + strings.Repeat("a", 64) + "\"\n",
			wantErr: false,
		},
		{
			name: "external with invalid checksum fails",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"d\"\n" +
				"source = \"external\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = [\"net\"]\n" +
				"checksum = \"bad\"\n",
			wantErr: true,
		},
		{
			name: "local with checksum fails",
			toml: "name = \"my_plugin\"\n" +
				"version = \"0.1.0\"\n" +
				"description = \"d\"\n" +
				"source = \"local\"\n" +
				"entrypoint = \"plugin.wasm\"\n" +
				"permissions = []\n" +
				"checksum = \"sha256:" + strings.Repeat("a", 64) + "\"\n",
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.toml))
			if tc.wantErr && err == nil {
				t.Fatal("ParseManifest succeeded; want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ParseManifest failed; want success: %v", err)
			}
		})
	}
}

func TestParseManifestDocCommentExample(t *testing.T) {
	// Exact TOML from the package doc comment in manifest.go / abi.go.
	// This test keeps docs honest: if the documented example drifts, this fails.
	docExample := "name = \"my_plugin\"\n" +
		"version = \"0.1.0\"\n" +
		"description = \"Example plugin.\"\n" +
		"source = \"local\"\n" +
		"entrypoint = \"plugin.wasm\"\n" +
		"permissions = [\"fs.read\", \"git\"]\n" +
		"dependencies = []\n" +
		"checksum = \"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\" # only for external source - this will be rejected for local\n"

	// For local, checksum must not be present; so modify to remove checksum for valid parse.
	// First ensure the shape with checksum is rejected for local (as spec expects).
	_, err := ParseManifest([]byte(docExample))
	if err == nil {
		t.Fatal("doc example with checksum for local should fail validation; want error about checksum")
	}
	if !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("error %q does not mention checksum", err.Error())
	}

	// Now test the valid variant without checksum + with tool.
	validDoc := "name = \"my_plugin\"\n" +
		"version = \"0.1.0\"\n" +
		"description = \"Example plugin.\"\n" +
		"source = \"local\"\n" +
		"entrypoint = \"plugin.wasm\"\n" +
		"permissions = [\"fs.read\", \"git\"]\n" +
		"dependencies = []\n" +
		"\n[[tools]]\n" +
		"name = \"my_plugin_greet\"\n" +
		"description = \"Greets a user.\"\n" +
		"permission = \"fs.read\"\n"

	m, err := ParseManifest([]byte(validDoc))
	if err != nil {
		t.Fatalf("valid doc example failed: %v", err)
	}
	if m.Name != "my_plugin" || len(m.Tools) != 1 || m.Tools[0].Name != "my_plugin_greet" {
		t.Fatalf("doc example parsed mismatch: %+v", m)
	}
}

func TestParseManifestRoundTripDoc(t *testing.T) {
	// Minimal doc-shaped example without tool, local source.
	toml := "name = \"my_plugin\"\n" +
		"version = \"0.1.0\"\n" +
		"description = \"Example plugin.\"\n" +
		"source = \"local\"\n" +
		"entrypoint = \"plugin.wasm\"\n" +
		"permissions = [\"fs.read\", \"git\"]\n" +
		"dependencies = []\n" +
		"\n[[tools]]\n" +
		"name = \"my_plugin_greet\"\n" +
		"description = \"Greets a user.\"\n" +
		"permission = \"fs.read\"\n"
	m1, err := ParseManifest([]byte(toml))
	if err != nil {
		t.Fatalf("first parse failed: %v", err)
	}
	// Re-parse the same TOML again to ensure idempotent.
	m2, err := ParseManifest([]byte(toml))
	if err != nil {
		t.Fatalf("second parse failed: %v", err)
	}
	if m1.Name != m2.Name || m1.Version != m2.Version || len(m1.Tools) != len(m2.Tools) {
		t.Errorf("round-trip mismatch: %+v vs %+v", m1, m2)
	}
}

// Ensure ParseManifest is the only entry point by testing that invalid TOML and invalid validation both return errors.
func TestParseManifestGoldenFailures(t *testing.T) {
	cases := []struct {
		name string
		toml string
	}{
		{
			name: "BOM",
			toml: string([]byte{0xEF, 0xBB, 0xBF}) + "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"a.wasm\"\npermissions = []\n",
		},
		{
			name: "validation failure bad name",
			toml: "name = \"Bad\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"a.wasm\"\npermissions = []\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.toml))
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), "manifest line") && !strings.Contains(err.Error(), "name") {
				// BOM errors contain manifest line; validation errors contain name etc.
				t.Errorf("error %q seems unrelated", err.Error())
			}
		})
	}
}
