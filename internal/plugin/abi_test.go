package plugin

import "testing"

func TestABIVersion(t *testing.T) {
	if ABIVersion != 1 {
		t.Fatalf("ABIVersion = %d, want 1", ABIVersion)
	}
	if ABIVersionV2 != 2 {
		t.Fatalf("ABIVersionV2 = %d, want 2", ABIVersionV2)
	}
	if len(SupportedABIVersions) != 2 || SupportedABIVersions[0] != 1 || SupportedABIVersions[1] != 2 {
		t.Fatalf("SupportedABIVersions = %v, want [1 2]", SupportedABIVersions)
	}
}

func TestPluginPermissionKinds(t *testing.T) {
	want := []string{"fs.read", "fs.write", "shell.exec", "git", "net", "llm"}
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
	if ExportAlloc == "" {
		t.Error("ExportAlloc must not be empty")
	}
	if ExportLLMStreamStart == "" {
		t.Error("ExportLLMStreamStart must not be empty")
	}
	if ExportLLMNextChunk == "" {
		t.Error("ExportLLMNextChunk must not be empty")
	}
	if ExportLLMCancel == "" {
		t.Error("ExportLLMCancel must not be empty")
	}
	// Ensure distinct across ABI v1+v2 (RNF-3.3).
	all := []string{ExportABIVersion, ExportToolList, ExportToolInvoke, ExportAlloc, ExportLLMStreamStart, ExportLLMNextChunk, ExportLLMCancel}
	seen := map[string]int{}
	for _, n := range all {
		if prev, ok := seen[n]; ok {
			t.Errorf("export name %q duplicated at index %d and %d", n, prev, len(seen))
		}
		seen[n] = len(seen)
	}
	// Ensure snake_case (no dots, no uppercase, only lowercase digits underscore)
	for _, name := range all {
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

func TestLLMErrorCodes(t *testing.T) {
	// Uniqueness pin (RNF-3.3): each code must be distinct.
	seen := map[int]bool{}
	for _, c := range AllLLMErrorCodes {
		if seen[c] {
			t.Errorf("LLM error code %d duplicated", c)
		}
		seen[c] = true
	}
	// Explicit values frozen (wizard generates against them).
	if LLMErrorCodeOK != 0 {
		t.Errorf("LLMErrorCodeOK = %d, want 0", LLMErrorCodeOK)
	}
	if LLMErrorCodeInvalidRequest != 1 {
		t.Errorf("LLMErrorCodeInvalidRequest = %d, want 1", LLMErrorCodeInvalidRequest)
	}
	if LLMErrorCodeInternal != 2 {
		t.Errorf("LLMErrorCodeInternal = %d, want 2", LLMErrorCodeInternal)
	}
	if LLMErrorCodeCancelled != 3 {
		t.Errorf("LLMErrorCodeCancelled = %d, want 3", LLMErrorCodeCancelled)
	}
	if LLMErrorCodeTimeout != 4 {
		t.Errorf("LLMErrorCodeTimeout = %d, want 4", LLMErrorCodeTimeout)
	}
	if len(AllLLMErrorCodes) != 5 {
		t.Errorf("AllLLMErrorCodes length = %d, want 5", len(AllLLMErrorCodes))
	}
}

func TestManifestKindValidation(t *testing.T) {
	cases := []struct {
		name    string
		toml    string
		wantErr string
		ok      bool
	}{
		{
			name: "provider without llm permission rejected",
			toml: "name = \"mock_provider\"\nversion = \"0.1.0\"\ndescription = \"p\"\nsource = \"local\"\nentrypoint = \"plugin.wasm\"\nkind = \"provider\"\npermissions = []\n",
			wantErr: "provider plugins must declare",
		},
		{
			name: "llm permission on tool kind rejected",
			toml: "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"plugin.wasm\"\npermissions = [\"llm\"]\n\n[[tools]]\nname = \"my_plugin_greet\"\ndescription = \"g\"\npermission = \"llm\"\n",
			wantErr: "\"llm\" is only allowed",
		},
		{
			name: "provider with tools rejected",
			toml: "name = \"mock_provider\"\nversion = \"0.1.0\"\ndescription = \"p\"\nsource = \"local\"\nentrypoint = \"plugin.wasm\"\nkind = \"provider\"\npermissions = [\"llm\"]\n\n[[tools]]\nname = \"mock_provider_tool\"\ndescription = \"t\"\npermission = \"llm\"\n",
			wantErr: "must not declare",
		},
		{
			name: "valid provider",
			toml: "name = \"mock_provider\"\nversion = \"0.1.0\"\ndescription = \"mock provider\"\nsource = \"local\"\nentrypoint = \"plugin.wasm\"\nkind = \"provider\"\npermissions = [\"llm\"]\n",
			ok: true,
		},
		{
			name: "valid tool without kind (backward compat)",
			toml: "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"plugin.wasm\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"my_plugin_greet\"\ndescription = \"g\"\npermission = \"fs.read\"\n",
			ok: true,
		},
		{
			name: "explicit tool kind",
			toml: "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"plugin.wasm\"\nkind = \"tool\"\npermissions = [\"fs.read\"]\n\n[[tools]]\nname = \"my_plugin_greet\"\ndescription = \"g\"\npermission = \"fs.read\"\n",
			ok: true,
		},
		{
			name: "invalid kind",
			toml: "name = \"my_plugin\"\nversion = \"0.1.0\"\ndescription = \"d\"\nsource = \"local\"\nentrypoint = \"plugin.wasm\"\nkind = \"wizard\"\npermissions = [\"fs.read\"]\n",
			wantErr: "kind",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseManifest([]byte(tc.toml))
			if tc.ok && err != nil {
				t.Fatalf("expected ok, got err: %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}

func contains(s, sub string) bool { return len(sub) == 0 || len(s) >= len(sub) && search(s, sub) }
func search(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
