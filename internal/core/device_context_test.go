package core

import "testing"

func TestDeviceContextIsConversationLocal(t *testing.T) {
	a, b := &conversation{}, &conversation{}
	a.rememberDeviceContext("open", map[string]any{"session": "task-a", "cwd": "/a", "device": "A"})
	b.rememberDeviceContext("open", map[string]any{"session": "task-b", "cwd": "/b", "device": "B"})
	for _, tc := range []struct {
		v            *conversation
		session, cwd string
	}{{a, "task-a", "/a"}, {b, "task-b", "/b"}} {
		args := map[string]any{}
		if err := tc.v.validateDeviceCall(t.Context(), rawBackend{}, "snapshot", args); err != nil {
			t.Fatal(err)
		}
		if args["session"] != tc.session || args["cwd"] != tc.cwd {
			t.Fatal(args)
		}
	}
	a.rememberDeviceContext("close", map[string]any{"session": "task-a"})
	if err := a.validateDeviceCall(t.Context(), rawBackend{}, "snapshot", map[string]any{}); err == nil {
		t.Fatal("missing session accepted")
	}
	if b.deviceContext["session"] != "task-b" {
		t.Fatal("another session changed")
	}
}
