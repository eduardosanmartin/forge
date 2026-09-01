package plugin

import "testing"

func TestABIVersion(t *testing.T) {
	if ABIVersion != 1 {
		t.Fatalf("ABIVersion = %d, want 1", ABIVersion)
	}
}

func TestPluginPermissionKinds(t *testing.T) {
	want := []string{"fs.read", "fs.write", "shell.exec", "git", "net"}
	if len(PluginPermissionKinds) != len(want) {
		t.Fatalf("PluginPermissionKinds length = %d, want %d: %v", len(PluginPermissionKinds), len(want), PluginPermissionKinds)
	}
	gotSet := make(map[string]bool, len(PluginPermissionKinds))
	for _, p := range PluginPermissionKinds {
		gotSet[p] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("PluginPermissionKinds missing %q: got %v", w, PluginPermissionKinds)
		}
	}
	// Ensure no extras.
	for _, g := range PluginPermissionKinds {
		found := false
		for _, w := range want {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("PluginPermissionKinds has extra %q beyond expected vocabulary", g)
		}
	}
}

func TestWASMExportNames(t *testing.T) {
	if ExportABIVersion == "" {
		t.Error("ExportABIVersion must not be empty")
	}
	if ExportToolList == "" {
		t.Error("ExportToolList must not be empty")
	}
	if ExportToolInvoke == "" {
		t.Error("ExportToolInvoke must not be empty")
	}
	// Ensure distinct.
	if ExportABIVersion == ExportToolList || ExportABIVersion == ExportToolInvoke || ExportToolList == ExportToolInvoke {
		t.Errorf("export names must be distinct: %q %q %q", ExportABIVersion, ExportToolList, ExportToolInvoke)
	}
	// Ensure snake_case (no dots, no uppercase, only lowercase digits underscore)
	for _, name := range []string{ExportABIVersion, ExportToolList, ExportToolInvoke} {
		for _, r := range name {
			if r >= 'A' && r <= 'Z' {
				t.Errorf("export name %q must not contain uppercase", name)
			}
			if r == '.' {
				t.Errorf("export name %q must not contain '.'", name)
			}
		}
	}
}
