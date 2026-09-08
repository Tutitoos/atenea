package android_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/Tutitoos/atenea/internal/adapter/android"
)

// TestRealAndroidBridge is an opt-in device-real gate. It reads the selected
// device, presses only Home, and starts/stops only the scrcpy process it owns.
func TestRealAndroidBridge(t *testing.T) {
	serial := os.Getenv("ATENEA_TEST_REAL_ANDROID_SERIAL")
	if serial == "" {
		t.Skip("set ATENEA_TEST_REAL_ANDROID_SERIAL for device-real validation")
	}
	runner, err := android.New(android.Options{AllowedSerials: []string{serial}})
	if err != nil {
		t.Fatal(err)
	}
	devices, err := runner.Run(t.Context(), request(android.CapabilityDevices, android.ImplementationDevices, nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(devices.Result["devices"].([]map[string]any)) == 0 {
		t.Fatal("ADB returned no devices")
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": serial}))
	if err != nil {
		t.Fatal(err)
	}
	if shot.Result["bytes"].(int) == 0 || shot.Result["frame_id"].(string) == "" {
		t.Fatalf("incomplete screenshot result: %#v", shot.Result)
	}
	inspection, err := runner.Run(t.Context(), request(android.CapabilityInspect, android.ImplementationInspect,
		map[string]any{"serial": serial}))
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Result["count"].(int) == 0 || inspection.Result["untrusted"] != true {
		t.Fatalf("incomplete inspection result: %#v", inspection.Result)
	}
	if _, err := runner.Run(t.Context(), request(android.CapabilityKey, android.ImplementationKey,
		map[string]any{"serial": serial, "frame_id": shot.Result["frame_id"], "key": "home"})); err != nil {
		t.Fatal(err)
	}
	mirror, err := runner.Run(t.Context(), request(android.CapabilityMirror, android.ImplementationMirror,
		map[string]any{"serial": serial}))
	if err != nil {
		t.Fatal(err)
	}
	if mirror.Result["pid"].(int) <= 0 {
		t.Fatalf("mirror result: %#v", mirror.Result)
	}
	if _, err := runner.Run(t.Context(), request(android.CapabilityUnmirror, android.ImplementationUnmirror,
		map[string]any{"serial": serial})); err != nil {
		t.Fatal(err)
	}
}

// TestRealAndroidSemanticSelector is an opt-in MIUI-compatible gate. The host
// starts only Atenea's fixture app, then the bridge validates its current
// accessibility control before sending the harmless Home key.
func TestRealAndroidSemanticSelector(t *testing.T) {
	serial := os.Getenv("ATENEA_TEST_ANDROID_SEMANTIC_HELPER_SERIAL")
	if serial == "" {
		t.Skip("set ATENEA_TEST_ANDROID_SEMANTIC_HELPER_SERIAL after installing android-helper")
	}
	if output, err := exec.CommandContext(t.Context(), "adb", "-s", serial, "shell", "am", "start", "-W", "-n", "io.atenea.androidhelper/.MainActivity").CombinedOutput(); err != nil {
		t.Fatalf("start helper fixture: %v: %s", err, output)
	}
	runner, err := android.New(android.Options{AllowedSerials: []string{serial}})
	if err != nil {
		t.Fatal(err)
	}
	shot, err := runner.Run(t.Context(), request(android.CapabilityScreenshot, android.ImplementationScreenshot,
		map[string]any{"serial": serial, "semantic": true}))
	if err != nil {
		t.Fatal(err)
	}
	if shot.Result["window"] == "" || shot.Result["orientation"] == "" {
		t.Fatalf("incomplete semantic screenshot result: %#v", shot.Result)
	}
	if _, err := runner.Run(t.Context(), request(android.CapabilityKey, android.ImplementationKey, map[string]any{
		"serial": serial, "frame_id": shot.Result["frame_id"], "key": "home",
		"selector": map[string]any{"content_desc": "atenea unicode fixture"},
	})); err != nil {
		t.Fatal(err)
	}
}
