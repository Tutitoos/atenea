package core

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/Tutitoos/atenea/internal/passthrough"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type deviceStateBackend struct {
	passthrough.Backend
	response string
}

func (b deviceStateBackend) Call(_ context.Context, tool string, args map[string]any) (json.RawMessage, error) {
	if tool != "session" || args["action"] != "list" || args["session"] == nil || args["session"] == "" {
		panic("preflight attempted a device action")
	}
	return json.RawMessage(b.response), nil
}

func TestDeviceOwnershipAndConcurrentReservation(t *testing.T) {
	core := &Core{}
	a := &conversation{core: core, session: &Session{id: "flow-a"}}
	b := &conversation{core: core, session: &Session{id: "flow-b"}}
	backend := rawBackend{Backend: deviceStateBackend{response: `{"structuredContent":{"ok":true,"data":{"sessions":[]}}}`}}
	args := func(session, device string) map[string]any {
		return map[string]any{"session": session, "device": device, "cwd": "/fixture"}
	}
	var wait sync.WaitGroup
	errors := make(chan error, 2)
	start := make(chan struct{})
	for _, item := range []struct {
		v       *conversation
		session string
	}{{a, "task-a"}, {b, "task-b"}} {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			release, err := item.v.reserveDeviceCall(t.Context(), backend, "open", args(item.session, "phone"))
			if release != nil {
				release()
			}
			errors <- err
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	successes, busy := 0, 0
	for err := range errors {
		if err == nil {
			successes++
		} else if contract.CodeOf(err) == "DEVICE_BUSY" {
			busy++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || busy != 1 {
		t.Fatalf("success=%d busy=%d", successes, busy)
	}
	// A separate target remains independently usable.
	c := &conversation{core: core, session: &Session{id: "flow-c"}}
	release, err := c.reserveDeviceCall(t.Context(), backend, "open", args("task-c", "other-phone"))
	if err != nil {
		t.Fatal(err)
	}
	release()
	c.rememberDeviceContext("open", args("task-c", "other-phone"))
	if _, err := a.reserveDeviceCall(t.Context(), backend, "click", args("task-c", "other-phone")); contract.CodeOf(err) != "DEVICE_BUSY" {
		t.Fatalf("session theft accepted: %v", err)
	}
	if _, err := c.reserveDeviceCall(t.Context(), backend, "click", args("task-c", "other-phone")); contract.CodeOf(err) != "SESSION_NOT_FOUND" {
		t.Fatalf("missing upstream state accepted: %v", err)
	}
	live := rawBackend{Backend: deviceStateBackend{response: `{"structuredContent":{"ok":true,"data":{"sessions":[{"name":"task-c","device_id":"id-c","device":"other-phone"}]}}}`}}
	release, err = c.reserveDeviceCall(t.Context(), live, "snapshot", args("task-c", "other-phone"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.reserveDeviceCall(t.Context(), live, "click", args("task-c", "other-phone")); contract.CodeOf(err) != "DEVICE_BUSY" {
		t.Fatalf("overlapping actions accepted: %v", err)
	}
	release()
	c.rememberDeviceContext("close", args("task-c", "other-phone"))
	if len(c.deviceContext) != 0 {
		t.Fatal("closed context retained")
	}
}

func TestDeviceUnknownStateFailsBeforeAction(t *testing.T) {
	v := &conversation{core: &Core{}, session: &Session{id: "flow"}}
	args := map[string]any{"session": "flow", "cwd": "/fixture", "udid": "device-id"}
	for _, response := range []string{`{"isError":true,"content":[{"type":"text","text":"unavailable"}]}`, `{"structuredContent":{"sessions":[{"name":"missing-device"}]}}`, `{}`} {
		_, err := v.reserveDeviceCall(t.Context(), rawBackend{Backend: deviceStateBackend{response: response}}, "open", args)
		if err == nil {
			t.Fatal("unknown state accepted")
		}
	}
}

func TestDeviceOfficialSDKSessionListingKeepsExplicitSessionScope(t *testing.T) {
	v := &conversation{core: &Core{}, session: &Session{id: "flow-a"}}
	args := map[string]any{"session": "dedicated", "cwd": "/fixture", "udid": "device-a"}
	backend := rawBackend{Backend: deviceStateBackend{response: `{"structuredContent":{"sessions":[{"name":"dedicated","device":{"id":"device-a","name":"iPhone","platform":"ios"},"identifiers":{"session":"dedicated","udid":"device-a"}}]}}`}}
	v.core.deviceOwners = map[string]*deviceOwner{deviceRealm(args) + "dedicated": {conversation: "flow-a", device: "device-a"}}
	release, err := v.reserveDeviceCall(t.Context(), backend, "snapshot", args)
	if err != nil {
		t.Fatal(err)
	}
	release()
	for _, input := range []map[string]any{nil, args, {"action": "destroy", "session": "dedicated", "cwd": "/fixture"}} {
		request := deviceSessionListArgs(input)
		if request["action"] != "list" || request["session"] == nil || request["session"] == "" {
			t.Fatalf("unsafe or implicitly scoped session list: %#v", request)
		}
	}
}
