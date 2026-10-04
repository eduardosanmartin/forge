package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// A long request (an agent turn waiting for a permission answer) must not
// block later requests on the same connection: the TUI sends
// permission.respond over the connection whose execute_turn is waiting for
// it. With requests handled one at a time per connection the answer sat
// behind the turn until the 5-minute ask timeout denied it (found testing
// an ask rule from the TUI, 2026-10-04). Same for an emergency halt sent
// from the client whose own turn is running (RNF-4.8).
func TestSlowRequestDoesNotBlockLaterOnesOnSameConnection(t *testing.T) {
	release := make(chan struct{})
	tx := NewTransport("127.0.0.1:0", nil, slog.New(slog.DiscardHandler))
	tx.handle = func(ctx context.Context, req *JSONRPCRequest) *JSONRPCResponse {
		switch req.Method {
		case "slow":
			select {
			case <-release:
			case <-ctx.Done():
			}
		case "release":
			close(release)
		}
		return &JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`"` + req.Method + `"`)}
	}
	if err := tx.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer tx.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://"+tx.Addr()+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	for i, method := range []string{"slow", "release"} {
		id := json.RawMessage(`"` + string(rune('1'+i)) + `"`)
		b, _ := json.Marshal(JSONRPCRequest{JSONRPC: "2.0", ID: &id, Method: method})
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			t.Fatal(err)
		}
	}

	got := map[string]bool{}
	for len(got) < 2 {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("responses so far %v: %v (the slow request blocked the connection)", got, err)
		}
		var resp JSONRPCResponse
		if err := json.Unmarshal(data, &resp); err != nil {
			t.Fatal(err)
		}
		var method string
		_ = json.Unmarshal(resp.Result, &method)
		got[method] = true
	}
}
