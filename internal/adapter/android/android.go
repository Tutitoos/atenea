// Package android exposes a narrow Android computer-use surface backed by
// ADB, UIAutomator and scrcpy. Calls are constructed from typed capability
// inputs; caller supplied command names or arbitrary arguments never cross
// the process boundary.
package android

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/Tutitoos/atenea/pkg/contract"
)

// DefaultADBBinary and the constants in this block define the bridge defaults,
// capability IDs and implementation IDs shared with Atenea's catalog.
const (
	DefaultADBBinary    = "adb"
	DefaultScrcpyBinary = "scrcpy"
	DefaultTimeout      = 15 * time.Second
	DefaultFrameTTL     = 30 * time.Second
	maxScreenshotBytes  = 12 << 20
	defaultImageMaxSide = 1280

	CapabilityDevices    = "android.devices"
	CapabilityScreenshot = "android.screenshot"
	CapabilityInspect    = "android.inspect"
	CapabilityTap        = "android.tap"
	CapabilitySwipe      = "android.swipe"
	CapabilityType       = "android.type"
	CapabilityKey        = "android.key"
	CapabilityMirror     = "android.mirror"
	CapabilityUnmirror   = "android.unmirror"

	ImplementationDevices    = "adb.devices"
	ImplementationScreenshot = "adb.screenshot"
	ImplementationInspect    = "uiautomator.inspect"
	ImplementationTap        = "adb.tap"
	ImplementationSwipe      = "adb.swipe"
	ImplementationType       = "adb.type"
	ImplementationKey        = "adb.key"
	ImplementationMirror     = "scrcpy.mirror"
	ImplementationUnmirror   = "scrcpy.unmirror"
)

var implementations = map[string]string{
	ImplementationDevices:    CapabilityDevices,
	ImplementationScreenshot: CapabilityScreenshot,
	ImplementationInspect:    CapabilityInspect,
	ImplementationTap:        CapabilityTap,
	ImplementationSwipe:      CapabilitySwipe,
	ImplementationType:       CapabilityType,
	ImplementationKey:        CapabilityKey,
	ImplementationMirror:     CapabilityMirror,
	ImplementationUnmirror:   CapabilityUnmirror,
}

// DefaultImplementations returns every implementation exposed by the bridge.
func DefaultImplementations() []string {
	out := make([]string, 0, len(implementations))
	for id := range implementations {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}

// Command runs one fixed binary with typed arguments and returns its stdout.
type Command func(context.Context, string, ...string) ([]byte, error)

// Process is the narrow lifecycle surface needed for a scrcpy child process.
type Process interface {
	PID() int
	Kill() error
}

// StartCommand starts scrcpy without exposing a shell to callers.
type StartCommand func(string, ...string) (Process, error)

// Options configures an Android runner and its injectable process boundaries.
type Options struct {
	Implementations []string
	AllowedSerials  []string
	ADBBinary       string
	ScrcpyBinary    string
	Timeout         time.Duration
	FrameTTL        time.Duration
	Command         Command
	Start           StartCommand
	Now             func() time.Time
}

type frame struct {
	serial      string
	generation  uint64
	digest      [sha256.Size]byte
	width       int
	height      int
	window      string
	orientation string
	expires     time.Time
}

// Runner implements the typed ADB, UIAutomator and scrcpy capability surface.
type Runner struct {
	implementations []string
	allowed         []string
	adb, scrcpy     string
	timeout         time.Duration
	frameTTL        time.Duration
	command         Command
	start           StartCommand
	now             func() time.Time

	mu          sync.Mutex
	frames      map[string]frame
	mirrors     map[string]Process
	states      map[string]string
	generations map[string]uint64
	actions     map[string]*sync.Mutex
}

// New constructs an allow-listed Android runner.
func New(opts Options) (*Runner, error) {
	impls := slices.Clone(opts.Implementations)
	if len(impls) == 0 {
		impls = DefaultImplementations()
	}
	known := DefaultImplementations()
	for _, id := range impls {
		if !slices.Contains(known, id) {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"android: nothing here answers implementation %q", id)
		}
	}
	if slices.Contains(opts.AllowedSerials, "*") {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: allowed_serials must name devices explicitly; wildcard access is refused")
	}
	adb := strings.TrimSpace(opts.ADBBinary)
	if adb == "" {
		adb = DefaultADBBinary
	}
	scrcpy := strings.TrimSpace(opts.ScrcpyBinary)
	if scrcpy == "" {
		scrcpy = DefaultScrcpyBinary
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	frameTTL := opts.FrameTTL
	if frameTTL <= 0 {
		frameTTL = DefaultFrameTTL
	}
	command := opts.Command
	if command == nil {
		command = runCommand
	}
	start := opts.Start
	if start == nil {
		start = startCommand
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &Runner{
		implementations: impls,
		allowed:         slices.Clone(opts.AllowedSerials),
		adb:             adb, scrcpy: scrcpy, timeout: timeout, frameTTL: frameTTL,
		command: command, start: start, now: now,
		frames: make(map[string]frame), mirrors: make(map[string]Process),
		states: make(map[string]string), generations: make(map[string]uint64), actions: make(map[string]*sync.Mutex),
	}, nil
}

// ID returns the runner identifier used by the orchestrator.
func (r *Runner) ID() string { return "android" }

// Surface summarizes the configured local executables and device scope.
func (r *Runner) Surface() string {
	return fmt.Sprintf("adb:%s scrcpy:%s allowed:%d", r.adb, r.scrcpy, len(r.allowed))
}

// Serves reports whether this runner exposes an implementation ID.
func (r *Runner) Serves(id string) bool { return slices.Contains(r.implementations, id) }

// Implementations returns the configured implementation IDs.
func (r *Runner) Implementations() []string { return slices.Clone(r.implementations) }

// Capabilities returns the capabilities backed by configured implementations.
func (r *Runner) Capabilities() []string {
	out := make([]string, 0, len(r.implementations))
	for _, id := range r.implementations {
		out = append(out, implementations[id])
	}
	slices.Sort(out)
	return out
}

// Run executes one validated Android capability request.
func (r *Runner) Run(ctx context.Context, req contract.RunRequest) (contract.Outcome, error) {
	capability, ok := implementations[req.Implementation.ID]
	if !ok || capability != req.Capability.ID {
		return contract.Outcome{}, contract.Fail(contract.FailureInvalidInput,
			"android: implementation %q does not answer %q", req.Implementation.ID, req.Capability.ID)
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	if isActionCapability(capability) {
		serial, err := r.serialFromPayload(req.Payload)
		if err != nil {
			return contract.Outcome{}, err
		}
		lock := r.actionLock(serial)
		lock.Lock()
		defer lock.Unlock()
	}
	started := time.Now()
	var result map[string]any
	var err error
	switch capability {
	case CapabilityDevices:
		result, err = r.devices(ctx)
	case CapabilityScreenshot:
		result, err = r.screenshot(ctx, req.Payload)
	case CapabilityInspect:
		result, err = r.inspect(ctx, req.Payload)
	case CapabilityTap:
		result, err = r.tap(ctx, req.Payload)
	case CapabilitySwipe:
		result, err = r.swipe(ctx, req.Payload)
	case CapabilityType:
		result, err = r.typeText(ctx, req.Payload)
	case CapabilityKey:
		result, err = r.key(ctx, req.Payload)
	case CapabilityMirror:
		result, err = r.mirror(ctx, req.Payload)
	case CapabilityUnmirror:
		result, err = r.unmirror(req.Payload)
	}
	if err != nil {
		return contract.Outcome{}, err
	}
	return contract.Outcome{
		Result: result, Verdict: contract.VerdictOK,
		Spent:    contract.Sample{Duration: time.Since(started)},
		SpentUSD: 0, SpentUSDKnown: true,
	}, nil
}

func isActionCapability(capability string) bool {
	return capability == CapabilityTap || capability == CapabilitySwipe || capability == CapabilityType || capability == CapabilityKey
}

func (r *Runner) actionLock(serial string) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock := r.actions[serial]
	if lock == nil {
		lock = &sync.Mutex{}
		r.actions[serial] = lock
	}
	return lock
}

func (r *Runner) devices(ctx context.Context) (map[string]any, error) {
	body, err := r.run(ctx, r.adb, "devices", "-l")
	if err != nil {
		return nil, err
	}
	devices := parseDevices(string(body), r.allowed)
	return map[string]any{"devices": devices}, nil
}

func parseDevices(body string, allowed []string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[0] == "List" || strings.HasPrefix(fields[0], "*") {
			continue
		}
		row := map[string]any{
			"serial": fields[0], "state": fields[1],
			"allowed": slices.Contains(allowed, fields[0]),
		}
		for _, field := range fields[2:] {
			key, value, ok := strings.Cut(field, ":")
			if ok && (key == "model" || key == "product" || key == "device") {
				row[key] = value
			}
		}
		kind := "physical"
		if strings.HasPrefix(fields[0], "emulator-") {
			kind = "emulator"
		}
		row["kind"] = kind
		out = append(out, row)
	}
	return out
}

func (r *Runner) screenshot(ctx context.Context, payload map[string]any) (map[string]any, error) {
	serial, err := r.serial(ctx, payload)
	if err != nil {
		return nil, err
	}
	body, width, height, digest, err := r.capture(ctx, serial)
	if err != nil {
		return nil, err
	}
	presented, imageWidth, imageHeight, err := presentScreenshot(body, width, height, payload)
	if err != nil {
		return nil, err
	}
	known := frame{serial: serial, generation: r.generation(serial), digest: digest, width: width, height: height, expires: r.now().Add(r.frameTTL)}
	if boolean(payload["semantic"]) {
		window, err := r.focusedWindow(ctx, serial)
		if err != nil {
			return nil, err
		}
		tree, err := r.dumpHierarchy(ctx, serial)
		if err != nil {
			return nil, err
		}
		known.window, known.orientation = window, tree.orientation()
	}
	id := "android-" + uuid.NewString()
	r.mu.Lock()
	r.pruneFramesLocked()
	r.frames[id] = known
	r.mu.Unlock()
	result := map[string]any{
		"png_base64": base64.StdEncoding.EncodeToString(presented),
		"width":      width, "height": height, "bytes": len(presented),
		"source_bytes": len(body), "image_width": imageWidth, "image_height": imageHeight,
		"scaled":        imageWidth != width || imageHeight != height,
		"legacy_base64": boolean(payload["legacy_base64"]),
		"serial":        serial, "untrusted": true, "frame_id": id,
	}
	if known.window != "" {
		result["semantic"] = true
		result["window"] = known.window
		result["orientation"] = known.orientation
	}
	return result, nil
}

func presentScreenshot(body []byte, width, height int, payload map[string]any) ([]byte, int, int, error) {
	if payload["resolution"] != "adaptive" || max(width, height) <= defaultImageMaxSide {
		return body, width, height, nil
	}
	source, err := png.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, 0, 0, contract.Fail(contract.FailureUnavailable, "android: decode screenshot for scaling: %v", err)
	}
	ratio := float64(defaultImageMaxSide) / float64(max(width, height))
	targetWidth, targetHeight := max(1, int(float64(width)*ratio)), max(1, int(float64(height)*ratio))
	target := image.NewRGBA(image.Rect(0, 0, targetWidth, targetHeight))
	for y := 0; y < targetHeight; y++ {
		sy := min(height-1, int(float64(y)/ratio))
		for x := 0; x < targetWidth; x++ {
			sx := min(width-1, int(float64(x)/ratio))
			target.Set(x, y, source.At(sx, sy))
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, target); err != nil {
		return nil, 0, 0, contract.Fail(contract.FailureUnavailable, "android: encode scaled screenshot: %v", err)
	}
	return encoded.Bytes(), targetWidth, targetHeight, nil
}

func (r *Runner) capture(ctx context.Context, serial string) ([]byte, int, int, [sha256.Size]byte, error) {
	body, err := r.run(ctx, r.adb, "-s", serial, "exec-out", "screencap", "-p")
	if err != nil {
		return nil, 0, 0, [sha256.Size]byte{}, err
	}
	if len(body) > maxScreenshotBytes {
		return nil, 0, 0, [sha256.Size]byte{}, contract.Fail(contract.FailureUnavailable,
			"android: screenshot exceeds the %d MiB safety ceiling", maxScreenshotBytes>>20)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(body))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, 0, 0, [sha256.Size]byte{}, contract.Fail(contract.FailureUnavailable,
			"android: adb returned an invalid PNG screenshot: %v", err)
	}
	return body, cfg.Width, cfg.Height, sha256.Sum256(body), nil
}

type xmlNode struct {
	XMLName  xml.Name   `xml:"node"`
	Attrs    []xml.Attr `xml:",any,attr"`
	Children []xmlNode  `xml:"node"`
}

type hierarchy struct {
	Attrs []xml.Attr `xml:",any,attr"`
	Nodes []xmlNode  `xml:"node"`
}

var boundsPattern = regexp.MustCompile(`^\[(\d+),(\d+)\]\[(\d+),(\d+)\]$`)

func (r *Runner) inspect(ctx context.Context, payload map[string]any) (map[string]any, error) {
	serial, err := r.serial(ctx, payload)
	if err != nil {
		return nil, err
	}
	tree, err := r.dumpHierarchy(ctx, serial)
	if err != nil {
		return nil, err
	}
	nodes, truncated := flattenHierarchy(tree, serial)
	nodes = filterNodes(nodes, payload)
	result := map[string]any{"nodes": nodes, "count": len(nodes), "serial": serial, "untrusted": true}
	if truncated != "" {
		result["truncated"] = truncated
	}
	return result, nil
}

func (r *Runner) dumpHierarchy(ctx context.Context, serial string) (hierarchy, error) {
	body, err := r.run(ctx, r.adb, "-s", serial, "exec-out", "uiautomator", "dump", "/dev/tty")
	if err != nil {
		return hierarchy{}, err
	}
	if len(body) > 2<<20 {
		return hierarchy{}, contract.Fail(contract.FailureUnavailable,
			"android: uiautomator tree exceeds the 2 MiB safety ceiling")
	}
	start := bytes.Index(body, []byte("<?xml"))
	if start < 0 {
		return hierarchy{}, contract.Fail(contract.FailureUnavailable,
			"android: uiautomator returned no XML hierarchy")
	}
	var tree hierarchy
	if err := xml.Unmarshal(body[start:], &tree); err != nil {
		return hierarchy{}, contract.Fail(contract.FailureUnavailable,
			"android: uiautomator returned invalid XML: %v", err)
	}
	return tree, nil
}

func (tree hierarchy) orientation() string {
	for _, attr := range tree.Attrs {
		if attr.Name.Local == "rotation" {
			return attr.Value
		}
	}
	return "unknown"
}

func flattenHierarchy(tree hierarchy, serial string) ([]map[string]any, string) {
	nodes := make([]map[string]any, 0, 256)
	truncated := ""
	var walk func([]xmlNode, int)
	walk = func(rows []xmlNode, depth int) {
		for _, node := range rows {
			if len(nodes) >= 5000 {
				truncated = "node_limit"
				return
			}
			row := map[string]any{"depth": depth, "serial": serial}
			for _, attr := range node.Attrs {
				name := strings.ReplaceAll(attr.Name.Local, "-", "_")
				switch name {
				case "text", "resource_id", "class", "package", "content_desc", "bounds":
					if attr.Value != "" {
						row[name] = attr.Value
					}
				case "clickable", "enabled", "focusable", "focused", "scrollable", "password", "selected", "checked", "checkable":
					row[name] = attr.Value == "true"
				}
			}
			if raw, ok := row["bounds"].(string); ok {
				if match := boundsPattern.FindStringSubmatch(raw); len(match) == 5 {
					x1, _ := strconv.Atoi(match[1])
					y1, _ := strconv.Atoi(match[2])
					x2, _ := strconv.Atoi(match[3])
					y2, _ := strconv.Atoi(match[4])
					row["center_x"], row["center_y"] = (x1+x2)/2, (y1+y2)/2
				}
			}
			nodes = append(nodes, row)
			walk(node.Children, depth+1)
			if truncated != "" {
				return
			}
		}
	}
	walk(tree.Nodes, 0)
	return nodes, truncated
}

func filterNodes(nodes []map[string]any, payload map[string]any) []map[string]any {
	text, _ := payload["text_contains"].(string)
	resourceID, _ := payload["resource_id"].(string)
	region, _ := payload["region"].(string)
	visible := boolean(payload["visible_only"])
	if text == "" && resourceID == "" && region == "" && !visible {
		return nodes
	}
	needle := strings.ToLower(text)
	var regionBounds [4]int
	hasRegion := false
	if match := boundsPattern.FindStringSubmatch(region); len(match) == 5 {
		for i := range regionBounds {
			regionBounds[i], _ = strconv.Atoi(match[i+1])
		}
		hasRegion = true
	}
	filtered := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		if resourceID != "" && node["resource_id"] != resourceID {
			continue
		}
		if needle != "" && !strings.Contains(strings.ToLower(fmt.Sprint(node["text"])), needle) && !strings.Contains(strings.ToLower(fmt.Sprint(node["content_desc"])), needle) {
			continue
		}
		raw, hasBounds := node["bounds"].(string)
		match := boundsPattern.FindStringSubmatch(raw)
		if (visible || hasRegion) && (!hasBounds || len(match) != 5) {
			continue
		}
		if len(match) == 5 {
			var bounds [4]int
			for i := range bounds {
				bounds[i], _ = strconv.Atoi(match[i+1])
			}
			if visible && (bounds[2] <= bounds[0] || bounds[3] <= bounds[1]) {
				continue
			}
			if hasRegion && (bounds[2] <= regionBounds[0] || bounds[0] >= regionBounds[2] || bounds[3] <= regionBounds[1] || bounds[1] >= regionBounds[3]) {
				continue
			}
		}
		filtered = append(filtered, node)
	}
	return filtered
}

func (r *Runner) tap(ctx context.Context, payload map[string]any) (map[string]any, error) {
	serial, current, target, err := r.validateFrame(ctx, payload)
	if err != nil {
		return nil, err
	}
	if target != nil {
		if _, hasX := payload["x"]; hasX {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"android: selector tap must not include x or y coordinates")
		}
		if _, hasY := payload["y"]; hasY {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"android: selector tap must not include x or y coordinates")
		}
		x, xok := integer(target["center_x"])
		y, yok := integer(target["center_y"])
		if !xok || !yok {
			return nil, contract.Fail(contract.FailureUnavailable,
				"android: selected control has no tappable bounds")
		}
		if _, err := r.run(ctx, r.adb, "-s", serial, "shell", "input", "tap", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
			return nil, err
		}
		return actionResult(CapabilityTap, serial), nil
	}
	x, xok := integer(payload["x"])
	y, yok := integer(payload["y"])
	if !xok || !yok || x < 0 || y < 0 || x >= current.width || y >= current.height {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: tap coordinates must be inside the captured %dx%d frame", current.width, current.height)
	}
	if _, err := r.run(ctx, r.adb, "-s", serial, "shell", "input", "tap", strconv.Itoa(x), strconv.Itoa(y)); err != nil {
		return nil, err
	}
	return actionResult(CapabilityTap, serial), nil
}

func (r *Runner) swipe(ctx context.Context, payload map[string]any) (map[string]any, error) {
	serial, current, _, err := r.validateFrame(ctx, payload)
	if err != nil {
		return nil, err
	}
	keys := []string{"from_x", "from_y", "to_x", "to_y"}
	values := make([]int, len(keys))
	for i, key := range keys {
		value, ok := integer(payload[key])
		if !ok {
			return nil, contract.Fail(contract.FailureInvalidInput, "android: swipe needs integer %s", key)
		}
		values[i] = value
	}
	for i := 0; i < len(values); i += 2 {
		if values[i] < 0 || values[i+1] < 0 || values[i] >= current.width || values[i+1] >= current.height {
			return nil, contract.Fail(contract.FailureInvalidInput,
				"android: swipe coordinates must be inside the captured %dx%d frame", current.width, current.height)
		}
	}
	duration := 300
	if value, ok := integer(payload["duration_ms"]); ok {
		duration = value
	}
	if duration < 50 || duration > 5000 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: duration_ms must be between 50 and 5000")
	}
	args := []string{"-s", serial, "shell", "input", "swipe"}
	for _, value := range values {
		args = append(args, strconv.Itoa(value))
	}
	args = append(args, strconv.Itoa(duration))
	if _, err := r.run(ctx, r.adb, args...); err != nil {
		return nil, err
	}
	return actionResult(CapabilitySwipe, serial), nil
}

var safeText = regexp.MustCompile(`^[A-Za-z0-9 .,_@+%:/?#=-]+$`)

func (r *Runner) typeText(ctx context.Context, payload map[string]any) (map[string]any, error) {
	serial, _, _, err := r.validateFrame(ctx, payload)
	if err != nil {
		return nil, err
	}
	text, _ := payload["text"].(string)
	if text == "" || len(text) > 512 || !safeText.MatchString(text) {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: text must be 1-512 safe ASCII characters; use a dedicated input helper for Unicode")
	}
	encoded := strings.ReplaceAll(text, "%", "%25")
	encoded = strings.ReplaceAll(encoded, " ", "%s")
	if _, err := r.run(ctx, r.adb, "-s", serial, "shell", "input", "text", encoded); err != nil {
		return nil, err
	}
	return actionResult(CapabilityType, serial), nil
}

var allowedKeys = map[string]string{
	"back": "KEYCODE_BACK", "home": "KEYCODE_HOME", "recents": "KEYCODE_APP_SWITCH",
	"enter": "KEYCODE_ENTER", "tab": "KEYCODE_TAB", "escape": "KEYCODE_ESCAPE",
	"delete": "KEYCODE_DEL", "up": "KEYCODE_DPAD_UP", "down": "KEYCODE_DPAD_DOWN",
	"left": "KEYCODE_DPAD_LEFT", "right": "KEYCODE_DPAD_RIGHT",
}

func (r *Runner) key(ctx context.Context, payload map[string]any) (map[string]any, error) {
	serial, _, _, err := r.validateFrame(ctx, payload)
	if err != nil {
		return nil, err
	}
	name, _ := payload["key"].(string)
	code, ok := allowedKeys[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: unsupported key %q", name)
	}
	if _, err := r.run(ctx, r.adb, "-s", serial, "shell", "input", "keyevent", code); err != nil {
		return nil, err
	}
	return actionResult(CapabilityKey, serial), nil
}

func (r *Runner) mirror(ctx context.Context, payload map[string]any) (map[string]any, error) {
	serial, err := r.serial(ctx, payload)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if process := r.mirrors[serial]; process != nil {
		return map[string]any{"did": CapabilityMirror, "serial": serial, "pid": process.PID(), "already_running": true}, nil
	}
	process, err := r.start(r.scrcpy, "-s", serial, "--no-audio", "--window-title", "Atenea Android "+serial)
	if err != nil {
		return nil, commandFailure(r.scrcpy, err, "")
	}
	r.mirrors[serial] = process
	return map[string]any{"did": CapabilityMirror, "serial": serial, "pid": process.PID(), "already_running": false}, nil
}

func (r *Runner) unmirror(payload map[string]any) (map[string]any, error) {
	serial, err := r.serialFromPayload(payload)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	process := r.mirrors[serial]
	delete(r.mirrors, serial)
	r.mu.Unlock()
	if process == nil {
		return nil, contract.Fail(contract.FailureNotFound,
			"android: no Atenea scrcpy mirror is running for %q", serial)
	}
	if err := process.Kill(); err != nil {
		return nil, commandFailure(r.scrcpy, err, "")
	}
	return actionResult(CapabilityUnmirror, serial), nil
}

func (r *Runner) serial(ctx context.Context, payload map[string]any) (string, error) {
	serial, err := r.serialFromPayload(payload)
	if err != nil {
		return "", err
	}
	body, err := r.run(ctx, r.adb, "-s", serial, "get-state")
	if err != nil {
		return "", err
	}
	state := strings.TrimSpace(string(body))
	r.mu.Lock()
	if previous, known := r.states[serial]; !known || previous != state {
		r.states[serial] = state
		r.generations[serial]++
		r.invalidateSerialFramesLocked(serial)
	}
	r.mu.Unlock()
	if state != "device" {
		return "", contract.Fail(contract.FailureUnavailable,
			"android: device %q is not online", serial)
	}
	return serial, nil
}

func (r *Runner) serialFromPayload(payload map[string]any) (string, error) {
	serial, _ := payload["serial"].(string)
	serial = strings.TrimSpace(serial)
	if serial == "" {
		return "", contract.Fail(contract.FailureInvalidInput, "android: serial is required")
	}
	if !slices.Contains(r.allowed, serial) {
		return "", contract.Fail(contract.FailurePermissionDenied,
			"android: serial %q is not in [android] allowed_serials", serial)
	}
	return serial, nil
}

func (r *Runner) validateFrame(ctx context.Context, payload map[string]any) (string, frame, map[string]any, error) {
	serial, err := r.serial(ctx, payload)
	if err != nil {
		return "", frame{}, nil, err
	}
	id, _ := payload["frame_id"].(string)
	r.mu.Lock()
	r.pruneFramesLocked()
	known, ok := r.frames[id]
	if ok {
		delete(r.frames, id) // one-shot even when the following freshness check fails
	}
	r.mu.Unlock()
	if id == "" || !ok || known.serial != serial || known.generation != r.generation(serial) {
		return "", frame{}, nil, contract.Fail(contract.FailureInvalidInput,
			"android: a fresh frame_id from android.screenshot for %q is required", serial)
	}
	selector, semantic, err := selectorFromPayload(payload)
	if err != nil {
		return "", frame{}, nil, err
	}
	if semantic {
		target, err := r.validateSemanticFrame(ctx, serial, known, selector)
		if err != nil {
			return "", frame{}, nil, err
		}
		return serial, known, target, nil
	}
	_, _, _, digest, err := r.capture(ctx, serial)
	if err != nil {
		return "", frame{}, nil, err
	}
	if digest != known.digest {
		return "", frame{}, nil, contract.Fail(contract.FailureInvalidInput,
			"android: the device screen changed after that frame; capture a fresh screenshot before acting")
	}
	return serial, known, nil, nil
}

type selector struct {
	resourceID  string
	text        string
	contentDesc string
}

func selectorFromPayload(payload map[string]any) (selector, bool, error) {
	raw, exists := payload["selector"]
	if !exists {
		return selector{}, false, nil
	}
	values, ok := raw.(map[string]any)
	if !ok {
		return selector{}, true, contract.Fail(contract.FailureInvalidInput,
			"android: selector must be an object")
	}
	for key := range values {
		if key != "resource_id" && key != "text" && key != "content_desc" {
			return selector{}, true, contract.Fail(contract.FailureInvalidInput,
				"android: selector supports resource_id, text, and content_desc only")
		}
	}
	value := func(key string) (string, error) {
		raw, ok := values[key]
		if !ok {
			return "", nil
		}
		text, ok := raw.(string)
		text = strings.TrimSpace(text)
		if !ok || text == "" || len(text) > 512 {
			return "", contract.Fail(contract.FailureInvalidInput,
				"android: selector %s must be a non-empty string of at most 512 bytes", key)
		}
		return text, nil
	}
	resourceID, err := value("resource_id")
	if err != nil {
		return selector{}, true, err
	}
	text, err := value("text")
	if err != nil {
		return selector{}, true, err
	}
	contentDesc, err := value("content_desc")
	if err != nil {
		return selector{}, true, err
	}
	if resourceID == "" && text == "" && contentDesc == "" {
		return selector{}, true, contract.Fail(contract.FailureInvalidInput,
			"android: selector needs resource_id, text, or content_desc")
	}
	return selector{resourceID: resourceID, text: text, contentDesc: contentDesc}, true, nil
}

func (r *Runner) validateSemanticFrame(ctx context.Context, serial string, known frame, selector selector) (map[string]any, error) {
	if known.window == "" {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: selector actions require a screenshot captured with semantic=true")
	}
	_, width, height, _, err := r.capture(ctx, serial)
	if err != nil {
		return nil, err
	}
	if width != known.width || height != known.height {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: display dimensions changed after that frame; capture a fresh screenshot before acting")
	}
	window, err := r.focusedWindow(ctx, serial)
	if err != nil {
		return nil, err
	}
	if window != known.window {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: focused window changed after that frame; capture a fresh screenshot before acting")
	}
	tree, err := r.dumpHierarchy(ctx, serial)
	if err != nil {
		return nil, err
	}
	if tree.orientation() != known.orientation {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: orientation changed after that frame; capture a fresh screenshot before acting")
	}
	nodes, _ := flattenHierarchy(tree, serial)
	matches := matchingVisibleNodes(nodes, selector)
	if len(matches) == 0 {
		return nil, contract.Fail(contract.FailureNotFound,
			"android: selector no longer identifies a visible enabled control")
	}
	if len(matches) > 1 {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: selector is ambiguous (%d visible enabled controls match)", len(matches))
	}
	after, err := r.focusedWindow(ctx, serial)
	if err != nil {
		return nil, err
	}
	if after != window {
		return nil, contract.Fail(contract.FailureInvalidInput,
			"android: focused window changed while resolving the selector; capture a fresh screenshot before acting")
	}
	return matches[0], nil
}

func matchingVisibleNodes(nodes []map[string]any, selector selector) []map[string]any {
	matches := make([]map[string]any, 0, 1)
	for _, node := range nodes {
		if node["enabled"] != true || !isVisibleNode(node) {
			continue
		}
		if selector.resourceID != "" && node["resource_id"] != selector.resourceID {
			continue
		}
		if selector.text != "" && node["text"] != selector.text {
			continue
		}
		if selector.contentDesc != "" && node["content_desc"] != selector.contentDesc {
			continue
		}
		matches = append(matches, node)
	}
	return matches
}

func isVisibleNode(node map[string]any) bool {
	raw, ok := node["bounds"].(string)
	match := boundsPattern.FindStringSubmatch(raw)
	if !ok || len(match) != 5 {
		return false
	}
	return match[1] != match[3] && match[2] != match[4]
}

var focusedWindowPattern = regexp.MustCompile(`(?m)^\s*mCurrentFocus=Window\{[^ ]+ u\d+ ([^}]+)\}`)

func (r *Runner) focusedWindow(ctx context.Context, serial string) (string, error) {
	body, err := r.run(ctx, r.adb, "-s", serial, "shell", "dumpsys", "window")
	if err != nil {
		return "", err
	}
	match := focusedWindowPattern.FindStringSubmatch(string(body))
	if len(match) != 2 || strings.TrimSpace(match[1]) == "" {
		return "", contract.Fail(contract.FailureUnavailable,
			"android: unable to determine the focused window")
	}
	return strings.TrimSpace(match[1]), nil
}

func (r *Runner) generation(serial string) uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.generations[serial]
}

func (r *Runner) invalidateSerialFramesLocked(serial string) {
	for id, known := range r.frames {
		if known.serial == serial {
			delete(r.frames, id)
		}
	}
}

func (r *Runner) pruneFramesLocked() {
	now := r.now()
	for id, known := range r.frames {
		if !known.expires.After(now) {
			delete(r.frames, id)
		}
	}
}

func (r *Runner) run(ctx context.Context, binary string, args ...string) ([]byte, error) {
	body, err := r.command(ctx, binary, args...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, contract.Stopped(ctxErr, "android "+binary, r.timeout)
		}
		return nil, commandFailure(binary, err, string(body))
	}
	if binary == r.adb && adbInputCommand(args) && inputSecurityFailure(body) {
		return nil, contract.Fail(contract.FailurePermissionDenied,
			"android: adb input was refused: enable the device vendor's USB debugging security/input setting").WithRaw(strings.TrimSpace(string(body)))
	}
	return body, nil
}

func runCommand(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return append(stdout.Bytes(), stderr.Bytes()...), err
	}
	if adbInputCommand(args) && inputSecurityFailure(stderr.Bytes()) {
		return stderr.Bytes(), nil
	}
	return stdout.Bytes(), nil
}

func adbInputCommand(args []string) bool {
	return len(args) >= 5 && args[2] == "shell" && args[3] == "input"
}

func inputSecurityFailure(body []byte) bool {
	text := string(body)
	return strings.Contains(text, "SecurityException") || strings.Contains(text, "INJECT_EVENTS")
}

type osProcess struct{ cmd *exec.Cmd }

func (p osProcess) PID() int { return p.cmd.Process.Pid }

func (p osProcess) Kill() error { return p.cmd.Process.Kill() }

func startCommand(binary string, args ...string) (Process, error) {
	cmd := exec.Command(binary, args...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	return osProcess{cmd: cmd}, nil
}

func commandFailure(binary string, err error, output string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return contract.Stopped(err, "android "+binary, DefaultTimeout)
	}
	var missing *exec.Error
	if errors.As(err, &missing) {
		return contract.Fail(contract.FailureUnavailable,
			"android: %s is not installed or not on PATH", binary).WithRaw(err.Error())
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		raw := strings.TrimSpace(output)
		if raw == "" {
			raw = strings.TrimSpace(string(exited.Stderr))
		}
		kind := contract.FailureUnavailable
		if strings.Contains(raw, "unauthorized") || strings.Contains(raw, "no permissions") ||
			strings.Contains(raw, "SecurityException") || strings.Contains(raw, "INJECT_EVENTS") {
			kind = contract.FailurePermissionDenied
		}
		message := fmt.Sprintf("android: %s failed", binary)
		if kind == contract.FailurePermissionDenied && strings.Contains(raw, "INJECT_EVENTS") {
			message += ": enable the device vendor's USB debugging security/input setting"
		}
		return contract.Fail(kind, "%s", message).WithRaw(raw)
	}
	return contract.Fail(contract.FailureUnavailable, "android: %s failed: %v", binary, err).WithRaw(err.Error())
}

func integer(value any) (int, bool) {
	switch n := value.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n == float64(int(n)) {
			return int(n), true
		}
	}
	return 0, false
}

func boolean(value any) bool { out, _ := value.(bool); return out }

func actionResult(capability, serial string) map[string]any {
	return map[string]any{"did": capability, "serial": serial}
}
