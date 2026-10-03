package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/eduardosanmartin/forge/internal/config"
	"github.com/eduardosanmartin/forge/internal/store"
	"github.com/eduardosanmartin/forge/internal/version"
)

// N4 (review 2026-10-03), reproduced with the REAL store (the mock ignores
// limits, which once hid the bug): status reported sessions:1 with 3
// sessions, an empty addr and a hardcoded "0.0.0-dev" version.
func TestStatusReportsRealCountsAddrAndVersion(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	logger := slog.New(slog.DiscardHandler)
	m := NewSessionManager(st, newTestLLMRegistry(), newTestToolsRegistry(), NewEmergencyState(logger), logger, config.Defaults(), newTestPermsEngine(), st)
	for i := 0; i < 3; i++ {
		if _, err := m.CreateSession(context.Background(), nil); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandler(m, logger, nil, nil)
	h.SetAddr("127.0.0.1:8765")
	resp := h.handleStatus(context.Background(), &JSONRPCRequest{})
	var got StatusResult
	if err := json.Unmarshal(resp.Result, &got); err != nil {
		t.Fatal(err)
	}
	if got.Sessions != 3 || got.Addr != "127.0.0.1:8765" || got.Version != version.Version || !got.Running {
		t.Fatalf("status = %+v, want 3 sessions, addr 127.0.0.1:8765, version %q", got, version.Version)
	}
}
