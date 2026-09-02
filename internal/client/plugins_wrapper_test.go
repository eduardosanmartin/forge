package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/eduardosanmartin/forge/internal/daemon"
)

func startPluginSkillServer(t *testing.T) (string, func()) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		for {
			_, data, err := conn.Read(context.Background())
			if err != nil {
				return
			}
			var req daemon.JSONRPCRequest
			if err := json.Unmarshal(data, &req); err != nil {
				continue
			}
			var result json.RawMessage
			switch req.Method {
			case daemon.MethodPluginList:
				result, _ = json.Marshal(daemon.PluginListResult{
					Plugins: []daemon.PluginInfoResult{
						{Name: "plug-a", Enabled: true, Version: "1.0", ToolCount: 2},
						{Name: "plug-b", Enabled: false, Version: "0.2"},
					},
				})
			case daemon.MethodSkillList:
				result, _ = json.Marshal(daemon.SkillListResult{
					Skills: []daemon.SkillInfoResult{
						{Name: "skill-x", Enabled: true, Category: "code"},
						{Name: "skill-y", Enabled: false},
					},
				})
			default:
				result = json.RawMessage(`{}`)
			}
			resp := daemon.JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
			frame, _ := json.Marshal(resp)
			_ = conn.Write(context.Background(), websocket.MessageText, frame)
		}
	})
	srv := httptest.NewServer(mux)
	addr := strings.TrimPrefix(srv.URL, "http://")
	return addr, func() { srv.Close() }
}

func TestPluginListWrapper(t *testing.T) {
	addr, closeFn := startPluginSkillServer(t)
	defer closeFn()
	cl, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cl.Close()
	res, err := cl.PluginList(context.Background())
	if err != nil {
		t.Fatalf("PluginList: %v", err)
	}
	if len(res.Plugins) != 2 {
		t.Fatalf("plugins len %d want 2", len(res.Plugins))
	}
	if res.Plugins[0].Name != "plug-a" || !res.Plugins[0].Enabled {
		t.Fatalf("first plugin wrong %+v", res.Plugins[0])
	}
	if res.Plugins[1].Enabled {
		t.Fatalf("second plugin should be disabled")
	}
}

func TestSkillListWrapper(t *testing.T) {
	addr, closeFn := startPluginSkillServer(t)
	defer closeFn()
	cl, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer cl.Close()
	res, err := cl.SkillList(context.Background())
	if err != nil {
		t.Fatalf("SkillList: %v", err)
	}
	if len(res.Skills) != 2 {
		t.Fatalf("skills len %d want 2", len(res.Skills))
	}
	if res.Skills[0].Name != "skill-x" || !res.Skills[0].Enabled {
		t.Fatalf("first skill wrong %+v", res.Skills[0])
	}
}
