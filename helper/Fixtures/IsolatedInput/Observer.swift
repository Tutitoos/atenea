// No input is sent. Samples global state without recording keystroke contents.
import AppKit
import ApplicationServices
import CoreGraphics

let directory = URL(fileURLWithPath: CommandLine.arguments[1])
let app = NSApplication.shared
app.setActivationPolicy(.accessory)
let system = AXUIElementCreateSystemWide()
AXUIElementSetMessagingTimeout(system, 0.1)
let keys: [CGKeyCode] = [0, 12, 36, 51, 55, 56, 58, 59, 123, 124]
func globalState() -> [UInt64] {
    var result: [UInt64] = []
    for id in [CGEventSourceStateID.hidSystemState, .combinedSessionState] {
        result.append(CGEventSource.flagsState(id).rawValue)
        result.append(CGEventSource.buttonState(id, button: .left) ? 1 : 0)
        result += keys.map { CGEventSource.keyState(id, key: $0) ? 1 : 0 }
    }
    return result
}
func focus() -> CFTypeRef? {
    var value: CFTypeRef?
    guard AXUIElementCopyAttributeValue(system, kAXFocusedUIElementAttribute as CFString, &value) == .success else { return nil }
    return value
}
var phase = ""
var baselinePointer = CGPoint.zero
var baselineFront: pid_t = 0
var baselineFocus: CFTypeRef?
var baselineState: [UInt64] = []
var samples = 0
var pointerChanges = 0
var frontChanges = 0
var focusChanges = 0
var stateChanges = 0
var activations = 0
var targetActivations = 0
var globalMarkers = 0
var destinations: [String: Int] = [:]
var maxGap = 0.0
var previous = Date()
let activation = NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.didActivateApplicationNotification, object: nil, queue: .main) { note in
    if !phase.isEmpty {
        activations += 1
        if let activated = note.userInfo?[NSWorkspace.applicationUserInfoKey] as? NSRunningApplication,
           activated.bundleIdentifier == "org.atenea.cua-fixture.target" { targetActivations += 1 }
    }
}
let observedTypes: [CGEventType] = [.keyDown, .keyUp, .flagsChanged, .leftMouseDown, .leftMouseUp, .mouseMoved, .leftMouseDragged, .scrollWheel]
let mask = observedTypes.reduce(0) { $0 | (CGEventMask(1) << CGEventMask($1.rawValue)) }
let globalTap = CGEvent.tapCreate(tap: .cgSessionEventTap, place: .headInsertEventTap, options: .listenOnly,
    eventsOfInterest: mask, callback: { _, _, event, _ in
        if !phase.isEmpty && event.getIntegerValueField(.eventSourceUserData) == 0x4154454E4541 { globalMarkers += 1 }
        return Unmanaged.passUnretained(event)
    }, userInfo: nil)
let annotatedTap = CGEvent.tapCreate(tap: .cgAnnotatedSessionEventTap, place: .headInsertEventTap, options: .listenOnly,
    eventsOfInterest: mask, callback: { _, _, event, _ in
        if !phase.isEmpty && event.getIntegerValueField(.eventSourceUserData) != 0x4154454E4541 {
            destinations[String(event.getIntegerValueField(.eventTargetUnixProcessID)), default: 0] += 1
        }
        return Unmanaged.passUnretained(event)
    }, userInfo: nil)
var tapSources: [CFRunLoopSource] = []
for tap in [globalTap, annotatedTap].compactMap({ $0 }) {
    let source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, tap, 0)!
    tapSources.append(source)
    CFRunLoopAddSource(CFRunLoopGetMain(), source, .commonModes)
    CGEvent.tapEnable(tap: tap, enable: true)
}
func write() {
    let payload: [String: Any] = ["phase": phase, "samples": samples, "pointer_changes": pointerChanges,
        "frontmost_changes": frontChanges, "focus_changes": focusChanges, "state_changes": stateChanges,
        "activation_notifications": activations, "target_activations": targetActivations,
        "global_marker_events": globalMarkers, "human_destination_events": destinations,
        "taps_active": [globalTap, annotatedTap].allSatisfy { $0.map { CGEvent.tapIsEnabled(tap: $0) } ?? false },
        "max_sample_gap_ms": maxGap * 1000,
        "focus_readable": baselineFocus != nil, "trusted": AXIsProcessTrusted()]
    let path = directory.appendingPathComponent("observer.json")
    try! JSONSerialization.data(withJSONObject: payload, options: [.sortedKeys]).write(to: path, options: .atomic)
    try! FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: path.path)
}
let timer = Timer.scheduledTimer(withTimeInterval: 0.001, repeats: true) { _ in
    guard let data = try? Data(contentsOf: directory.appendingPathComponent("phase.json")),
          let requested = try? JSONSerialization.jsonObject(with: data) as? [String: String], let name = requested["phase"] else { return }
    let now = Date()
    let pointer = CGEvent(source: nil)!.location
    let front = NSWorkspace.shared.frontmostApplication?.processIdentifier ?? 0
    let state = globalState()
    if phase != name {
        phase = name; baselinePointer = pointer; baselineFront = front; baselineFocus = focus(); baselineState = state
        samples = 0; pointerChanges = 0; frontChanges = 0; focusChanges = 0; stateChanges = 0; activations = 0
        targetActivations = 0; globalMarkers = 0; destinations = [:]; maxGap = 0; previous = now
    }
    maxGap = max(maxGap, now.timeIntervalSince(previous)); previous = now
    samples += 1
    if pointer != baselinePointer { pointerChanges += 1 }
    if front != baselineFront { frontChanges += 1 }
    if state != baselineState { stateChanges += 1 }
    if samples % 10 == 0, let baselineFocus {
        if let current = focus() { if !CFEqual(baselineFocus, current) { focusChanges += 1 } }
        else { focusChanges += 1 }
    }
    if samples % 20 == 0 { write() }
}
app.run()
