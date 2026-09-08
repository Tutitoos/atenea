package android_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tutitoos/atenea/internal/adapter/android"
	"github.com/Tutitoos/atenea/pkg/contract"
)

type call struct {
	binary string
	args   []string
}

type fakeProcess struct {
	pid    int
	killed bool
}

func (p *fakeProcess) PID() int { return p.pid }
func (p *fakeProcess) Kill() error {
	p.killed = true
	return nil
}

func request(capability, implementation string, payload map[string]any) contract.RunRequest {
	return contract.RunRequest{
		Capability:     contract.Capability{ID: capability},
		Implementation: contract.Implementation{ID: implementation, Capability: capability},
		Payload:        payload,
	}
}

func screenshot(t *testing.T, shade uint8) []byte {
	return screenshotSize(t, 4, 3, shade)
}

func screenshotSize(t *testing.T, width, height int, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{R: shade, A: 255})
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestAdaptiveScreenshotKeepsNativeActionDimensions(t *testing.T) {
	body := screenshotSize(t, 2000, 1000, 1)
	runner, err := android.New(android.Options{AllowedSerials: []string{"phone"}, Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[len(args)-1] == "get-state" {
			return []byte("device"), nil
		}
		return body, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot, map[string]any{"serial": "phone", "resolution": "adaptive"}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Result["width"] != 2000 || out.Result["height"] != 1000 || out.Result["image_width"] != 1280 || out.Result["image_height"] != 640 || out.Result["scaled"] != true {
		t.Fatalf("adaptive screenshot = %#v", out.Result)
	}
	native, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot, map[string]any{"serial": "phone"}))
	if err != nil {
		t.Fatal(err)
	}
	if native.Result["image_width"] != 2000 || native.Result["scaled"] != false {
		t.Fatalf("native screenshot = %#v", native.Result)
	}
}

func TestWildcardDeviceAccessIsRefused(t *testing.T) {
	_, err := android.New(android.Options{AllowedSerials: []string{"*"}})
	if err == nil || contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("New error = %v, want invalid input", err)
	}
}

func TestDevicesAreParsedAndAllowedStateIsExplicit(t *testing.T) {
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"emulator-5554"},
		Command: func(_ context.Context, binary string, args ...string) ([]byte, error) {
			if binary != "adb" || !reflect.DeepEqual(args, []string{"devices", "-l"}) {
				t.Fatalf("command = %s %v", binary, args)
			}
			return []byte("List of devices attached\nemulator-5554 device product:sdk model:Pixel_10 device:emu\nphone offline model:Redmi\n"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(t.Context(), request(android.CapabilityDevices, android.ImplementationDevices, nil))
	if err != nil {
		t.Fatal(err)
	}
	rows := out.Result["devices"].([]map[string]any)
	if len(rows) != 2 || rows[0]["allowed"] != true || rows[0]["kind"] != "emulator" || rows[1]["allowed"] != false {
		t.Fatalf("devices = %#v", rows)
	}
}

func TestFreshScreenshotTokenBuildsOnlyTheExpectedTapCommand(t *testing.T) {
	pngBody := screenshot(t, 20)
	var calls []call
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"emulator-5554"},
		Command: func(_ context.Context, binary string, args ...string) ([]byte, error) {
			calls = append(calls, call{binary: binary, args: append([]string(nil), args...)})
			switch {
			case reflect.DeepEqual(args, []string{"-s", "emulator-5554", "get-state"}):
				return []byte("device\n"), nil
			case reflect.DeepEqual(args, []string{"-s", "emulator-5554", "exec-out", "screencap", "-p"}):
				return pngBody, nil
			case reflect.DeepEqual(args, []string{"-s", "emulator-5554", "shell", "input", "tap", "2", "1"}):
				return nil, nil
			default:
				t.Fatalf("unexpected command: %s %v", binary, args)
				return nil, nil
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": "emulator-5554"}))
	if err != nil {
		t.Fatal(err)
	}
	if shot.Result["width"] != 4 || shot.Result["height"] != 3 || shot.Result["untrusted"] != true {
		t.Fatalf("screenshot = %#v", shot.Result)
	}
	frameID := shot.Result["frame_id"].(string)
	_, err = runner.Run(t.Context(), request(android.CapabilityTap, android.ImplementationTap,
		map[string]any{"serial": "emulator-5554", "frame_id": frameID, "x": 2, "y": 1}))
	if err != nil {
		t.Fatal(err)
	}
	last := calls[len(calls)-1]
	if last.binary != "adb" || !reflect.DeepEqual(last.args,
		[]string{"-s", "emulator-5554", "shell", "input", "tap", "2", "1"}) {
		t.Fatalf("last command = %#v", last)
	}
	_, err = runner.Run(t.Context(), request(android.CapabilityTap, android.ImplementationTap,
		map[string]any{"serial": "emulator-5554", "frame_id": frameID, "x": 2, "y": 1}))
	if err == nil || contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("replayed frame error = %v, want invalid input", err)
	}
}

func TestChangedScreenRefusesAnAction(t *testing.T) {
	first := screenshot(t, 20)
	second := screenshot(t, 40)
	captures := 0
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"device-1"},
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[len(args)-1] == "get-state" {
				return []byte("device"), nil
			}
			if args[len(args)-1] == "-p" {
				captures++
				if captures == 1 {
					return first, nil
				}
				return second, nil
			}
			t.Fatalf("unexpected action command after stale frame: %v", args)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": "device-1"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), request(android.CapabilityTap, android.ImplementationTap,
		map[string]any{"serial": "device-1", "frame_id": shot.Result["frame_id"], "x": 1, "y": 1}))
	if err == nil || contract.KindOf(err) != contract.FailureInvalidInput || !strings.Contains(err.Error(), "screen changed") {
		t.Fatalf("error = %v, want changed-screen refusal", err)
	}
}

func TestSemanticSelectorToleratesUnrelatedPixelChanges(t *testing.T) {
	first, second := screenshot(t, 20), screenshot(t, 40)
	window := []byte("mCurrentFocus=Window{abc u0 app.example/app.example.MainActivity}\n")
	tree := []byte(`<?xml version="1.0"?><hierarchy rotation="0"><node text="Save" resource-id="app:id/save" clickable="true" enabled="true" bounds="[1,1][3,2]"/></hierarchy>`)
	captures := 0
	runner, err := android.New(android.Options{AllowedSerials: []string{"phone"}, Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case args[len(args)-1] == "get-state":
			return []byte("device"), nil
		case reflect.DeepEqual(args[len(args)-3:], []string{"exec-out", "screencap", "-p"}):
			captures++
			if captures == 1 {
				return first, nil
			}
			return second, nil
		case reflect.DeepEqual(args[len(args)-2:], []string{"dumpsys", "window"}):
			return window, nil
		case reflect.DeepEqual(args[len(args)-3:], []string{"uiautomator", "dump", "/dev/tty"}):
			return tree, nil
		case reflect.DeepEqual(args, []string{"-s", "phone", "shell", "input", "tap", "2", "1"}):
			return nil, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": "phone", "semantic": true}))
	if err != nil {
		t.Fatal(err)
	}
	if shot.Result["window"] != "app.example/app.example.MainActivity" || shot.Result["orientation"] != "0" {
		t.Fatalf("semantic screenshot = %#v", shot.Result)
	}
	_, err = runner.Run(t.Context(), request(android.CapabilityTap, android.ImplementationTap, map[string]any{
		"serial": "phone", "frame_id": shot.Result["frame_id"], "selector": map[string]any{"resource_id": "app:id/save"},
	}))
	if err != nil {
		t.Fatal(err)
	}
}

func TestSemanticSelectorRefusesAmbiguousOrChangedWindow(t *testing.T) {
	pngBody := screenshot(t, 20)
	window := []byte("mCurrentFocus=Window{abc u0 app.example/app.example.MainActivity}\n")
	tree := []byte(`<?xml version="1.0"?><hierarchy rotation="0"><node text="Save" enabled="true" bounds="[1,1][3,2]"/><node text="Save" enabled="true" bounds="[1,2][3,3]"/></hierarchy>`)
	changedWindow := false
	runner, err := android.New(android.Options{AllowedSerials: []string{"phone"}, Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		switch {
		case args[len(args)-1] == "get-state":
			return []byte("device"), nil
		case args[len(args)-1] == "-p":
			return pngBody, nil
		case reflect.DeepEqual(args[len(args)-2:], []string{"dumpsys", "window"}):
			if changedWindow {
				return []byte("mCurrentFocus=Window{def u0 other.example/other.example.OtherActivity}\n"), nil
			}
			return window, nil
		case reflect.DeepEqual(args[len(args)-3:], []string{"uiautomator", "dump", "/dev/tty"}):
			return tree, nil
		default:
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": "phone", "semantic": true}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), request(android.CapabilityTap, android.ImplementationTap, map[string]any{
		"serial": "phone", "frame_id": shot.Result["frame_id"], "selector": map[string]any{"text": "Save"},
	}))
	if err == nil || contract.KindOf(err) != contract.FailureInvalidInput || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous selector error = %v", err)
	}
	shot, err = runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": "phone", "semantic": true}))
	if err != nil {
		t.Fatal(err)
	}
	changedWindow = true
	_, err = runner.Run(t.Context(), request(android.CapabilityTap, android.ImplementationTap, map[string]any{
		"serial": "phone", "frame_id": shot.Result["frame_id"], "selector": map[string]any{"text": "Save"},
	}))
	if err == nil || contract.KindOf(err) != contract.FailureInvalidInput || !strings.Contains(err.Error(), "focused window changed") {
		t.Fatalf("changed window error = %v", err)
	}
}

func TestSelectorRequiresSemanticFrame(t *testing.T) {
	pngBody := screenshot(t, 20)
	runner, err := android.New(android.Options{AllowedSerials: []string{"phone"}, Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[len(args)-1] == "get-state" {
			return []byte("device"), nil
		}
		return pngBody, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot, map[string]any{"serial": "phone"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), request(android.CapabilityTap, android.ImplementationTap, map[string]any{
		"serial": "phone", "frame_id": shot.Result["frame_id"], "selector": map[string]any{"text": "Save"},
	}))
	if err == nil || contract.KindOf(err) != contract.FailureInvalidInput || !strings.Contains(err.Error(), "semantic=true") {
		t.Fatalf("non-semantic selector error = %v", err)
	}
}

func TestExpiredFrameIsRefused(t *testing.T) {
	now := time.Unix(10, 0)
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"device-1"}, FrameTTL: time.Second, Now: func() time.Time { return now },
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[len(args)-1] == "get-state" {
				return []byte("device"), nil
			}
			return screenshot(t, 1), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": "device-1"}))
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	_, err = runner.Run(t.Context(), request(android.CapabilityKey, android.ImplementationKey,
		map[string]any{"serial": "device-1", "frame_id": shot.Result["frame_id"], "key": "back"}))
	if err == nil || contract.KindOf(err) != contract.FailureInvalidInput {
		t.Fatalf("error = %v, want expired frame refusal", err)
	}
}

func TestUIAutomatorTreeIsFlattenedWithCentersAndProvenance(t *testing.T) {
	xmlBody := `<?xml version="1.0"?><hierarchy><node text="Settings" class="android.widget.TextView" package="com.android.settings" clickable="true" enabled="true" bounds="[10,20][110,80]"><node content-desc="Child" bounds="[20,30][40,50]"/></node></hierarchy>`
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"emulator-5554"},
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[len(args)-1] == "get-state" {
				return []byte("device"), nil
			}
			return []byte(xmlBody), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(t.Context(), request(android.CapabilityInspect, android.ImplementationInspect,
		map[string]any{"serial": "emulator-5554"}))
	if err != nil {
		t.Fatal(err)
	}
	nodes := out.Result["nodes"].([]map[string]any)
	if len(nodes) != 2 || nodes[0]["center_x"] != 60 || nodes[0]["center_y"] != 50 || nodes[1]["depth"] != 1 || nodes[1]["serial"] != "emulator-5554" {
		t.Fatalf("nodes = %#v", nodes)
	}
}

func TestUIAutomatorInspectionCanFilterTextIDAndRegion(t *testing.T) {
	xmlBody := `<?xml version="1.0"?><hierarchy><node text="Save" resource-id="app:id/save" content-desc="save document" bounds="[10,20][110,80]"/><node text="Cancel" resource-id="app:id/cancel" bounds="[500,600][600,680]"/></hierarchy>`
	runner, err := android.New(android.Options{AllowedSerials: []string{"phone"}, Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if args[len(args)-1] == "get-state" {
			return []byte("device"), nil
		}
		return []byte(xmlBody), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(t.Context(), request(android.CapabilityInspect, android.ImplementationInspect, map[string]any{"serial": "phone", "text_contains": "document", "resource_id": "app:id/save", "region": "[0,0][200,200]", "visible_only": true}))
	if err != nil {
		t.Fatal(err)
	}
	nodes := out.Result["nodes"].([]map[string]any)
	if len(nodes) != 1 || nodes[0]["resource_id"] != "app:id/save" {
		t.Fatalf("filtered nodes = %#v", nodes)
	}
}

func TestMirrorUsesFixedArgumentsAndCanOnlyStopItsOwnProcess(t *testing.T) {
	process := &fakeProcess{pid: 42}
	var started call
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"emulator-5554"}, ScrcpyBinary: "/usr/local/bin/scrcpy",
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if reflect.DeepEqual(args, []string{"-s", "emulator-5554", "get-state"}) {
				return []byte("device"), nil
			}
			t.Fatalf("unexpected command: %v", args)
			return nil, nil
		},
		Start: func(binary string, args ...string) (android.Process, error) {
			started = call{binary: binary, args: append([]string(nil), args...)}
			return process, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runner.Run(t.Context(), request(android.CapabilityMirror, android.ImplementationMirror,
		map[string]any{"serial": "emulator-5554"}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Result["pid"] != 42 || started.binary != "/usr/local/bin/scrcpy" || !reflect.DeepEqual(started.args,
		[]string{"-s", "emulator-5554", "--no-audio", "--window-title", "Atenea Android emulator-5554"}) {
		t.Fatalf("start = %#v result = %#v", started, out.Result)
	}
	_, err = runner.Run(t.Context(), request(android.CapabilityUnmirror, android.ImplementationUnmirror,
		map[string]any{"serial": "emulator-5554"}))
	if err != nil || !process.killed {
		t.Fatalf("stop error = %v killed = %v", err, process.killed)
	}
}

func TestMissingADBIsUnavailable(t *testing.T) {
	runner, err := android.New(android.Options{Command: func(context.Context, string, ...string) ([]byte, error) {
		return nil, &exec.Error{Name: "adb", Err: exec.ErrNotFound}
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), request(android.CapabilityDevices, android.ImplementationDevices, nil))
	if err == nil || contract.KindOf(err) != contract.FailureUnavailable || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("error = %v, want missing-tool unavailable", err)
	}
}

func TestVendorInputRestrictionIsPermissionDenied(t *testing.T) {
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"phone"},
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[len(args)-1] == "get-state" {
				return []byte("device"), nil
			}
			if args[len(args)-1] == "-p" {
				return screenshot(t, 1), nil
			}
			return []byte("SecurityException: INJECT_EVENTS permission required"), &exec.ExitError{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": "phone"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), request(android.CapabilityKey, android.ImplementationKey,
		map[string]any{"serial": "phone", "frame_id": shot.Result["frame_id"], "key": "home"}))
	if err == nil || contract.KindOf(err) != contract.FailurePermissionDenied ||
		!strings.Contains(err.Error(), "USB debugging") || !strings.Contains(contract.RawOf(err), "INJECT_EVENTS") {
		t.Fatalf("error = %v raw = %q", err, contract.RawOf(err))
	}
}

func TestZeroExitVendorInputRestrictionIsPermissionDenied(t *testing.T) {
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"phone"},
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[len(args)-1] == "get-state" {
				return []byte("device"), nil
			}
			if args[len(args)-1] == "-p" {
				return screenshot(t, 1), nil
			}
			return []byte("SecurityException: INJECT_EVENTS permission required"), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": "phone"}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(t.Context(), request(android.CapabilityKey, android.ImplementationKey,
		map[string]any{"serial": "phone", "frame_id": shot.Result["frame_id"], "key": "home"}))
	if err == nil || contract.KindOf(err) != contract.FailurePermissionDenied || !strings.Contains(contract.RawOf(err), "INJECT_EVENTS") {
		t.Fatalf("error = %v raw = %q", err, contract.RawOf(err))
	}
}

func TestIdenticalScreenshotsReceiveDistinctFrameTokens(t *testing.T) {
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"phone"},
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[len(args)-1] == "get-state" {
				return []byte("device"), nil
			}
			return screenshot(t, 1), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot, map[string]any{"serial": "phone"}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot, map[string]any{"serial": "phone"}))
	if err != nil {
		t.Fatal(err)
	}
	if first.Result["frame_id"] == second.Result["frame_id"] {
		t.Fatalf("frame IDs must be unique: %q", first.Result["frame_id"])
	}
}

func TestActionsForOneDeviceDoNotOverlap(t *testing.T) {
	var active, maximum atomic.Int32
	runner, err := android.New(android.Options{
		AllowedSerials: []string{"phone"},
		Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if args[len(args)-1] == "get-state" {
				return []byte("device"), nil
			}
			if args[len(args)-1] == "-p" {
				return screenshot(t, 1), nil
			}
			current := active.Add(1)
			for seen := maximum.Load(); current > seen && !maximum.CompareAndSwap(seen, current); seen = maximum.Load() {
			}
			time.Sleep(25 * time.Millisecond)
			active.Add(-1)
			return nil, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot, map[string]any{"serial": "phone"}))
	if err != nil {
		t.Fatal(err)
	}
	second, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot, map[string]any{"serial": "phone"}))
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for _, frameID := range []string{first.Result["frame_id"].(string), second.Result["frame_id"].(string)} {
		group.Add(1)
		go func(id string) {
			defer group.Done()
			_, _ = runner.Run(t.Context(), request(android.CapabilityKey, android.ImplementationKey, map[string]any{"serial": "phone", "frame_id": id, "key": "home"}))
		}(frameID)
	}
	group.Wait()
	if maximum.Load() != 1 {
		t.Fatalf("concurrent actions = %d, want 1", maximum.Load())
	}
}
