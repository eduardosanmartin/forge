package skill

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateSkill(t *testing.T) {
	// Helper to create temp skill dir with optional script file.
	makeDir := func(t *testing.T, name string, withScript string) string {
		t.Helper()
		dir := filepath.Join(t.TempDir(), name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if withScript != "" {
			scriptPath := filepath.Join(dir, filepath.FromSlash(withScript))
			if err := os.MkdirAll(filepath.Dir(scriptPath), 0o755); err != nil {
				t.Fatalf("mkdir script dir: %v", err)
			}
			if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho hi"), 0o644); err != nil {
				t.Fatalf("write script: %v", err)
			}
		}
		return dir
	}

	tests := []struct {
		name    string
		skill   func(dir string) Skill
		dirName string
		withScript string
		wantErr bool
		errSub  string
	}{
		{
			name: "valid local with script exists",
			dirName: "my-skill",
			withScript: "scripts/check.sh",
			skill: func(dir string) Skill {
				return Skill{
					Name: "my-skill", Description: "desc", Source: SourceLocal,
					Scripts: []string{"scripts/check.sh"}, DirPath: dir,
				}
			},
			wantErr: false,
		},
		{
			name: "bad name uppercase",
			dirName: "my-skill",
			skill: func(dir string) Skill {
				return Skill{Name: "My-Skill", Description: "desc", Source: SourceLocal, DirPath: dir}
			},
			wantErr: true, errSub: "name",
		},
		{
			name: "bad name too short",
			dirName: "x",
			skill: func(dir string) Skill {
				return Skill{Name: "a", Description: "desc", Source: SourceLocal, DirPath: dir}
			},
			wantErr: true, errSub: "name",
		},
		{
			name: "missing description",
			dirName: "my-skill",
			skill: func(dir string) Skill {
				return Skill{Name: "my-skill", Description: "", Source: SourceLocal, DirPath: dir}
			},
			wantErr: true, errSub: "description",
		},
		{
			name: "invalid source",
			dirName: "my-skill",
			skill: func(dir string) Skill {
				return Skill{Name: "my-skill", Description: "desc", Source: "unknown", DirPath: dir}
			},
			wantErr: true, errSub: "source",
		},
		{
			name: "checksum on local",
			dirName: "my-skill",
			skill: func(dir string) Skill {
				return Skill{Name: "my-skill", Description: "desc", Source: SourceLocal, Checksum: "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", DirPath: dir}
			},
			wantErr: true, errSub: "checksum",
		},
		{
			name: "external missing checksum",
			dirName: "my-skill",
			skill: func(dir string) Skill {
				return Skill{Name: "my-skill", Description: "desc", Source: SourceExternal, DirPath: dir}
			},
			wantErr: true, errSub: "checksum",
		},
		{
			name: "external bad checksum format",
			dirName: "my-skill",
			skill: func(dir string) Skill {
				return Skill{Name: "my-skill", Description: "desc", Source: SourceExternal, Checksum: "sha256:zzzz", DirPath: dir}
			},
			wantErr: true, errSub: "checksum",
		},
		{
			name: "script missing on disk",
			dirName: "my-skill",
			skill: func(dir string) Skill {
				return Skill{Name: "my-skill", Description: "desc", Source: SourceLocal, Scripts: []string{"scripts/missing.sh"}, DirPath: dir}
			},
			wantErr: true, errSub: "does not exist",
		},
		{
			name: "script absolute path",
			dirName: "my-skill",
			skill: func(dir string) Skill {
				return Skill{Name: "my-skill", Description: "desc", Source: SourceLocal, Scripts: []string{"/abs/path.sh"}, DirPath: dir}
			},
			wantErr: true, errSub: "absolute",
		},
		{
			name: "script with ..",
			dirName: "my-skill",
			skill: func(dir string) Skill {
				return Skill{Name: "my-skill", Description: "desc", Source: SourceLocal, Scripts: []string{"../escape.sh"}, DirPath: dir}
			},
			wantErr: true, errSub: "..",
		},
		{
			name: "dir name mismatch",
			dirName: "real-dir",
			skill: func(dir string) Skill {
				return Skill{Name: "other-name", Description: "desc", Source: SourceLocal, DirPath: dir}
			},
			wantErr: true, errSub: "directory name",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := makeDir(t, tc.dirName, tc.withScript)
			sk := tc.skill(dir)
			// Override DirPath to the temp dir (makeDir already gives correct path)
			// For dir mismatch case, DirPath is the temp dir with different basename.
			err := validateSkill(&sk)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantErr && tc.errSub != "" && !strings.Contains(err.Error(), tc.errSub) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.errSub)
			}
			if tc.wantErr && !errors.Is(err, ErrInvalidSkill) {
				t.Errorf("error should wrap ErrInvalidSkill, got %v", err)
			}
		})
	}
}

func TestValidateSkill_ErrorsJoinAggregation(t *testing.T) {
	dir := t.TempDir()
	// Need dir basename matches name if we want both errors reported.
	// Use name "ab" (valid) but make dir mismatch plus missing description.
	// Better: use invalid name and missing description.
	sk := Skill{
		Name: "BadName", Description: "", Source: SourceLocal, DirPath: filepath.Join(dir, "bad"),
	}
	if err := os.MkdirAll(sk.DirPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	err := validateSkill(&sk)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "name") {
		t.Errorf("should contain name error: %s", msg)
	}
	if !strings.Contains(msg, "description") {
		t.Errorf("should contain description error: %s", msg)
	}
	// Also directory mismatch should be reported because base "bad" != "BadName"
	if !strings.Contains(msg, "directory") {
		t.Errorf("should contain directory mismatch: %s", msg)
	}
}
