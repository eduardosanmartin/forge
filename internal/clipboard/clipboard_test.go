package clipboard

import (
	"testing"
)

func TestCandidateCommands(t *testing.T) {
	cases := []struct {
		goos      string
		wantFirst []string
		wantN     int
		wantOK    bool
	}{
		{"windows", []string{"powershell", "-NoProfile", "-NonInteractive", "-Command", "Set-Clipboard"}, 1, true},
		{"darwin", []string{"pbcopy"}, 1, true},
		{"linux", []string{"xclip", "-selection", "clipboard"}, 3, true},
		{"plan9", nil, 0, false},
		{"", nil, 0, false},
	}
	for _, tc := range cases {
		cands := candidateCommands(tc.goos)
		if (len(cands) > 0) != tc.wantOK {
			t.Fatalf("%q: ok=%v want %v", tc.goos, len(cands) > 0, tc.wantOK)
		}
		if !tc.wantOK {
			continue
		}
		if len(cands) != tc.wantN {
			t.Fatalf("%q: %d candidates, want %d", tc.goos, len(cands), tc.wantN)
		}
		got := cands[0]
		if len(got) != len(tc.wantFirst) {
			t.Fatalf("%q: first %v, want %v", tc.goos, got, tc.wantFirst)
		}
		for i := range got {
			if got[i] != tc.wantFirst[i] {
				t.Fatalf("%q: first %v, want %v", tc.goos, got, tc.wantFirst)
			}
		}
	}
}

func TestWriteEmptyIsNoop(t *testing.T) {
	// Must not touch the real clipboard (side-effect free by contract).
	if err := Write(""); err != nil {
		t.Fatalf("empty write should be a no-op nil, got %v", err)
	}
}
