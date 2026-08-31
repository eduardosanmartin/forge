// Package daemon implements the forge daemon process with JSON-RPC 2.0 over WebSocket.
package daemon

import (
	"encoding/json"
	"testing"
)

func TestJSONRPCRequestMarshal(t *testing.T) {
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		Method:  "test.method",
		Params:  json.RawMessage(`{"key":"value"}`),
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var parsed JSONRPCRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.JSONRPC != "2.0" || parsed.Method != "test.method" {
		t.Errorf("unexpected parsed request: %+v", parsed)
	}
}

func TestJSONRPCRequestWithID(t *testing.T) {
	id := json.RawMessage(`123`)
	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  "test.method",
	}
	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var parsed JSONRPCRequest
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.ID == nil || string(*parsed.ID) != "123" {
		t.Errorf("expected ID 123, got %v", parsed.ID)
	}
}

func TestJSONRPCNotification(t *testing.T) {
	notif := JSONRPCNotification{
		JSONRPC: "2.0",
		Method:  "session.event",
		Params:  json.RawMessage(`{"session_id":"abc"}`),
	}
	data, err := json.Marshal(notif)
	if err != nil {
		t.Fatal(err)
	}
	var parsed JSONRPCNotification
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.JSONRPC != "2.0" || parsed.Method != "session.event" {
		t.Errorf("unexpected parsed notification: %+v", parsed)
	}
}

func TestJSONRPCResponseSuccess(t *testing.T) {
	id := json.RawMessage(`1`)
	resp, err := NewResultResponse(&id, map[string]string{"result": "ok"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Error != nil {
		t.Errorf("unexpected error: %+v", resp.Error)
	}
	if string(resp.Result) != `{"result":"ok"}` {
		t.Errorf("unexpected result: %s", resp.Result)
	}
}

func TestJSONRPCResponseError(t *testing.T) {
	id := json.RawMessage(`1`)
	resp := NewErrorResponse(&id, ErrCodeInvalidParams, "bad params", map[string]string{"detail": "missing field"})
	if resp.Error == nil {
		t.Fatal("expected error")
	}
	if resp.Error.Code != ErrCodeInvalidParams {
		t.Errorf("expected code %d, got %d", ErrCodeInvalidParams, resp.Error.Code)
	}
	if resp.Error.Message != "bad params" {
		t.Errorf("unexpected message: %s", resp.Error.Message)
	}
}

func TestNewNotification(t *testing.T) {
	notif, err := NewNotification("test.event", map[string]string{"key": "value"})
	if err != nil {
		t.Fatal(err)
	}
	if notif.Method != "test.event" {
		t.Errorf("unexpected method: %s", notif.Method)
	}
	if notif.JSONRPC != "2.0" {
		t.Errorf("unexpected jsonrpc: %s", notif.JSONRPC)
	}
}

func TestSessionEventPayload(t *testing.T) {
	payload := SessionEventPayload{
		Action:    "created",
		SessionID: "abc123",
		Metadata:  map[string]any{"key": "value"},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var parsed SessionEventPayload
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Action != "created" || parsed.SessionID != "abc123" {
		t.Errorf("unexpected parsed payload: %+v", parsed)
	}
}

func TestMessageEventPayload(t *testing.T) {
	payload := MessageEventPayload{
		SessionID: "abc123",
	}
	payload.Message.ID = 1
	payload.Message.Seq = 5
	payload.Message.Role = "assistant"
	payload.Message.Content = "Hello"
	payload.Message.CreatedAt = 1234567890
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var parsed MessageEventPayload
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Message.Role != "assistant" {
		t.Errorf("unexpected role: %s", parsed.Message.Role)
	}
}

func TestToolCallEventPayload(t *testing.T) {
	payload := ToolCallEventPayload{
		SessionID:  "abc123",
		ToolCallID: "call_123",
		Name:       "fs_read",
		Status:     "finished",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var parsed ToolCallEventPayload
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Status != "finished" {
		t.Errorf("unexpected status: %s", parsed.Status)
	}
}

func TestEmergencyHaltPayload(t *testing.T) {
	payload := EmergencyHaltPayload{
		SessionID: "abc123",
		Reason:    "emergency",
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var parsed EmergencyHaltPayload
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Reason != "emergency" {
		t.Errorf("unexpected reason: %s", parsed.Reason)
	}
}

func TestErrorCodes(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{"ParseError", ErrCodeParseError},
		{"InvalidRequest", ErrCodeInvalidRequest},
		{"MethodNotFound", ErrCodeMethodNotFound},
		{"InvalidParams", ErrCodeInvalidParams},
		{"InternalError", ErrCodeInternalError},
		{"SessionNotFound", ErrCodeSessionNotFound},
		{"SessionHalted", ErrCodeSessionHalted},
		{"ToolError", ErrCodeToolError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.code == 0 {
				t.Errorf("error code %s should not be zero", tt.name)
			}
		})
	}
}

func TestMethodConstants(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		expected string
	}{
		{"SessionEvent", MethodSessionEvent, "session.event"},
		{"MessageEvent", MethodMessageEvent, "message.event"},
		{"ToolCallEvent", MethodToolCallEvent, "tool.call.event"},
		{"EmergencyHalt", MethodEmergencyHalt, "emergency.halt"},
		{"CreateSession", MethodCreateSession, "session.create"},
		{"GetSession", MethodGetSession, "session.get"},
		{"ListSessions", MethodListSessions, "session.list"},
		{"DeleteSession", MethodDeleteSession, "session.delete"},
		{"ExecuteTurn", MethodExecuteTurn, "session.execute_turn"},
		{"GetMessages", MethodGetMessages, "session.get_messages"},
		{"GetMessagesSince", MethodGetMessagesSince, "session.get_messages_since"},
		{"HaltSession", MethodHaltSession, "session.halt"},
		{"ResumeSession", MethodResumeSession, "session.resume"},
		{"HaltAll", MethodHaltAll, "emergency.halt_all"},
		{"Status", MethodStatus, "daemon.status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.method != tt.expected {
				t.Errorf("expected %s, got %s", tt.expected, tt.method)
			}
		})
	}
}
