import Foundation
import AppKit
import ApplicationServices
@preconcurrency import CoreGraphics

struct InputReceipt: Sendable {
    let backend: String
    let pid: pid_t
    let events: Int
    let pointerBefore: CGPoint
    let pointerAfter: CGPoint
    let frontmostBefore: pid_t
    let frontmostAfter: pid_t
    let elapsedMS: Double
    let virtualPointer: CGPoint?

    var dictionary: [String: Any] {
        var value: [String: Any] = ["input_backend": backend, "input_source": "private", "target_pid": Int(pid),
         "events_sent": events, "pointer_before": [pointerBefore.x, pointerBefore.y],
         "pointer_after": [pointerAfter.x, pointerAfter.y],
         "frontmost_before": Int(frontmostBefore), "frontmost_after": Int(frontmostAfter),
         "elapsed_ms": elapsedMS]
        if let virtualPointer { value["virtual_pointer"] = [virtualPointer.x, virtualPointer.y] }
        return value
    }
}

@MainActor
enum Input {
    nonisolated static let marker: Int64 = 0x4154454E4541
    private(set) static var virtualPointer: CGPoint?
    private(set) static var virtualPointerPID: pid_t?

    static func recordPointer(_ point: CGPoint, pid: pid_t) {
        virtualPointer = point
        virtualPointerPID = pid
    }

    static func perform(frame: CaptureFrame, visualFeedback: Bool, state: VisualState,
                        point: CGPoint? = nil,
                        operation: @MainActor (InputSession) async throws -> Void) async throws -> InputReceipt {
        try await ActionGate.shared.begin(visualFeedback: visualFeedback, targetPID: frame.target.pid)
        let started = ProcessInfo.processInfo.systemUptime
        do {
            guard let pointer = CGEvent(source: nil)?.location,
                  let front = NSWorkspace.shared.frontmostApplication?.processIdentifier else {
                throw RPCError.denied("global input state could not be sampled; no input sent")
            }
            guard AXUIElementSetMessagingTimeout(AXUIElementCreateSystemWide(), 0.2) == .success else {
                throw RPCError.denied("bounded accessibility messaging could not be configured; no input sent")
            }
            guard InputMonitor.shared.active else {
                throw RPCError.denied("human input monitoring is unavailable; no input sent")
            }
            _ = try await Capture.validatedFrame(pid: frame.target.pid, bundleID: frame.target.bundleID,
                                                appName: frame.target.appName, frameID: frame.id)
            try WindowSafety.ensure(frame.target, point: point)
            let session = try InputSession(target: frame.target)
            VisualFeedbackController.shared.setEnabled(visualFeedback)
            if visualFeedback {
                VisualFeedbackController.shared.show(target: frame.target, state: state)
                if let point { try await VisualFeedbackController.shared.animateCursor(globalPoint: point) }
            }
            try await ActionGate.shared.checkpoint()
            try await operation(session)
            try await ActionGate.shared.checkpoint()
            guard let after = CGEvent(source: nil)?.location,
                  let frontAfter = NSWorkspace.shared.frontmostApplication?.processIdentifier else {
                throw RPCError.denied("input was dispatched but its isolation samples are unavailable; observe before another action")
            }
            VisualFeedbackController.shared.setState(.observing)
            let receipt = InputReceipt(backend: session.backend, pid: frame.target.pid, events: session.eventsSent,
                                       pointerBefore: pointer, pointerAfter: after,
                                       frontmostBefore: front, frontmostAfter: frontAfter,
                                       elapsedMS: (ProcessInfo.processInfo.systemUptime - started) * 1000,
                                       virtualPointer: virtualPointerPID == frame.target.pid ? virtualPointer : nil)
            await ActionGate.shared.finish()
            return receipt
        } catch {
            if await ActionGate.shared.isPaused() { VisualFeedbackController.shared.pause(byHumanInput: true) }
            await ActionGate.shared.finish()
            throw error
        }
    }

    nonisolated static func secureField(role: String?, subrole: String?) -> Bool {
        role == "AXSecureTextField" || subrole == "AXSecureTextField"
    }

    static func refuseIfSecureFieldFocused(_ target: WindowTarget) async throws {
        var element = try WindowSafety.focusedElement(target)
        for _ in 0..<32 {
            try await ActionGate.shared.checkpoint()
            let role = WindowSafety.attribute(element, kAXRoleAttribute as String) as? String
            var raw: CFTypeRef?
            let status = AXUIElementCopyAttributeValue(element, kAXSubroleAttribute as CFString, &raw)
            let subrole = raw as? String
            try validateSecurity(role: role, subrole: subrole, status: status, hasSubroleValue: raw != nil)
            if role == kAXWindowRole as String {
                guard WindowSafety.windowMatches(element, target: target) else {
                    throw RPCError.denied("the focused field's window changed; no keyboard event sent")
                }
                return
            }
            guard let parent = WindowSafety.attribute(element, kAXParentAttribute as String),
                  CFGetTypeID(parent) == AXUIElementGetTypeID() else {
                throw RPCError.denied("the target field's ancestry is unknown; no keyboard event sent")
            }
            // swiftlint:disable:next force_cast
            element = parent as! AXUIElement
        }
        throw RPCError.denied("the target field ancestry exceeded the safety limit; no keyboard event sent")
    }

    nonisolated static func validateSecurity(role: String?, subrole: String?, status: AXError, hasSubroleValue: Bool) throws {
        guard let role, role != "AXUnknown" else {
            throw RPCError.denied("the target field role is unknown; no keyboard event sent")
        }
        if secureField(role: role, subrole: subrole) {
            throw RPCError.denied("typing into a secure text field is refused")
        }
        if status == .success && hasSubroleValue && subrole == nil {
            throw RPCError.denied("the target field subrole has an unknown type; no keyboard event sent")
        }
        if role == kAXTextFieldRole as String || role == kAXTextAreaRole as String {
            // AppKit's ordinary NSTextView exposes AXTextArea and explicitly
            // does not support AXSubrole. Secure text fields use AXTextField;
            // ancestors are still checked up to the captured AXWindow.
            if role == kAXTextAreaRole as String && status == .attributeUnsupported { return }
            guard status == .success, subrole != nil else {
                throw RPCError.denied("the target text field's security state is unknown; no keyboard event sent")
            }
        } else if status != .success && status != .attributeUnsupported {
            throw RPCError.denied("the target field security query failed; no keyboard event sent")
        }
    }

    nonisolated static let keyCodes: [String: CGKeyCode] = [
        "return": 36, "enter": 36, "tab": 48, "space": 49, "delete": 51,
        "escape": 53, "esc": 53, "left": 123, "right": 124, "down": 125, "up": 126,
        "home": 115, "end": 119, "pageup": 116, "pagedown": 121,
        "a": 0, "c": 8, "v": 9, "x": 7, "z": 6, "s": 1, "f": 3, "w": 13, "q": 12,
    ]

    nonisolated static func textChunks(_ text: String) -> [String] {
        var chunks: [String] = []
        var current = ""
        for character in text {
            // AppKit interprets a leading newline/tab as a command for the entire event.
            if character.isNewline || character == "\t" {
                if !current.isEmpty { chunks.append(current); current = "" }
                chunks.append(String(character))
            } else {
                current.append(character)
                if current.utf16.count >= 20 { chunks.append(current); current = "" }
            }
        }
        if !current.isEmpty { chunks.append(current) }
        return chunks
    }

}

/// All generated state belongs to Atenea. No HID/session posting, pointer warp,
/// activation, clipboard or globally pressed modifier is used for delivery.
@MainActor
final class InputSession {
    let target: WindowTarget
    let source: CGEventSource
    private let launchDate: Date?
    private(set) var eventsSent = 0
    private(set) var backend = "cg_event_pid"

    init(target: WindowTarget) throws {
        guard let source = CGEventSource(stateID: .privateState) else {
            throw RPCError.denied("a private input source could not be created")
        }
        source.userData = Input.marker
        self.source = source
        self.target = target
        launchDate = NSRunningApplication(processIdentifier: target.pid)?.launchDate
    }

    func checkpoint(at point: CGPoint? = nil) async throws {
        try await ActionGate.shared.checkpoint()
        try WindowSafety.ensure(target, point: point)
    }

    func post(_ event: CGEvent?, point: CGPoint? = nil, flags: CGEventFlags = []) throws {
        guard let event else { throw RPCError.denied("an input event could not be created") }
        try WindowSafety.ensure(target, point: point)
        event.flags = flags
        event.setIntegerValueField(.eventSourceUserData, value: Input.marker)
        event.setIntegerValueField(.mouseEventWindowUnderMousePointer, value: Int64(target.windowID))
        event.setIntegerValueField(.mouseEventWindowUnderMousePointerThatCanHandleThisEvent, value: Int64(target.windowID))
        event.postToPid(target.pid)
        if let point { Input.recordPointer(point, pid: target.pid) }
        eventsSent += 1
    }

    func mouse(_ type: CGEventType, at point: CGPoint, clicks: Int = 1, eventNumber: Int? = nil) -> CGEvent? {
        guard let eventType = NSEvent.EventType(rawValue: UInt(type.rawValue)) else { return nil }
        let local = CGPoint(x: point.x - target.frame.minX, y: point.y - target.frame.minY)
        // Foreign window IDs are not NSWindows owned by this helper. Build a
        // window-bound NSEvent, then verify its CG payload uses window-local
        // top-left coordinates before PID delivery. Never warp the real cursor.
        let height = NSScreen.screens.first?.frame.maxY ?? 0
        guard let event = NSEvent.mouseEvent(with: eventType,
            location: CGPoint(x: local.x, y: height - local.y), modifierFlags: [],
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: Int(target.windowID),
            context: nil, eventNumber: eventNumber ?? eventsSent + 1, clickCount: clicks,
            pressure: type == .leftMouseDown || type == .leftMouseDragged ? 1 : 0)?.cgEvent,
            event.location == local, NSEvent(cgEvent: event)?.windowNumber == Int(target.windowID) else { return nil }
        event.setSource(source)
        return event
    }

    private func release(_ event: CGEvent?, flags: CGEventFlags = []) {
        // A moved window still needs its release. A dead/reused process must
        // not receive it, and no release is ever sent to the session stream.
        guard let event, let app = NSRunningApplication(processIdentifier: target.pid),
              !app.isTerminated, app.bundleIdentifier == target.bundleID, app.launchDate == launchDate else { return }
        event.flags = flags
        event.setIntegerValueField(.eventSourceUserData, value: Input.marker)
        event.postToPid(target.pid)
        eventsSent += 1
    }

    private func pair(down: CGEvent?, up: CGEvent?, point: CGPoint? = nil, flags: CGEventFlags = []) throws {
        guard let down, let up else { throw RPCError.denied("an input event could not be created") }
        try post(down, point: point, flags: flags)
        do { try post(up, point: point, flags: flags) }
        catch { release(up, flags: flags); throw error }
    }

    func semanticClick(at point: CGPoint) async throws -> Bool {
        try await checkpoint(at: point)
        let app = AXUIElementCreateApplication(target.pid)
        AXUIElementSetMessagingTimeout(app, 0.5)
        var raw: AXUIElement?
        guard AXUIElementCopyElementAtPosition(app, Float(point.x), Float(point.y), &raw) == .success,
              let element = raw else { return false }
        var owner: pid_t = 0
        guard AXUIElementGetPid(element, &owner) == .success, owner == target.pid,
              let window = WindowSafety.window(of: element), WindowSafety.windowMatches(window, target: target) else { return false }
        var actions: CFArray?
        guard AXUIElementCopyActionNames(element, &actions) == .success,
              let names = actions as? [String], names.contains(kAXPressAction as String) else { return false }
        try await checkpoint(at: point)
        guard AXUIElementPerformAction(element, kAXPressAction as CFString) == .success else {
            throw RPCError.denied("AXPress was not confirmed; observe before another action")
        }
        backend = "ax_press"
        Input.recordPointer(point, pid: target.pid)
        eventsSent += 1
        return true
    }

    func click(at point: CGPoint, clicks: Int) async throws {
        if clicks == 1, try await semanticClick(at: point) { return }
        for n in 1...clicks {
            try await checkpoint(at: point)
            try pair(down: mouse(.leftMouseDown, at: point, clicks: n),
                     up: mouse(.leftMouseUp, at: point, clicks: n), point: point)
        }
    }

    func move(to point: CGPoint) async throws {
        try await checkpoint(at: point)
        try post(mouse(.mouseMoved, at: point), point: point)
    }

    func drag(from start: CGPoint, to end: CGPoint, visualFeedback: Bool) async throws {
        var last = start
        var down = false
        // AppKit associates a mouse-up with its down by event number, including cancellation cleanup.
        let eventNumber = eventsSent + 1
        // Cleanup stays in this PID at the last dispatched point, before the
        // gate finishes. A partial drag is not rolled back or replayed.
        defer {
            if down { release(mouse(.leftMouseUp, at: last, eventNumber: eventNumber)) }
        }
        try await checkpoint(at: start)
        try post(mouse(.leftMouseDown, at: start, eventNumber: eventNumber), point: start)
        down = true
        for step in 1...18 {
            let point = Self.dragPoint(from: start, to: end, step: step)
            try await checkpoint(at: point)
            try post(mouse(.leftMouseDragged, at: point, eventNumber: eventNumber), point: point)
            last = point
            if visualFeedback { VisualFeedbackController.shared.setCursor(globalPoint: point) }
            try await Task.sleep(nanoseconds: 16_000_000)
        }
        try await checkpoint(at: end)
        try post(mouse(.leftMouseUp, at: end, eventNumber: eventNumber), point: end)
        down = false
    }

    nonisolated static func dragPoint(from start: CGPoint, to end: CGPoint, step: Int) -> CGPoint {
        let progress = CGFloat(step) / 18
        return CGPoint(x: start.x + (end.x - start.x) * progress, y: start.y + (end.y - start.y) * progress)
    }

    func scroll(at point: CGPoint, dx: Int, dy: Int) async throws {
        try await checkpoint(at: point)
        guard let dx = Int32(exactly: dx), let dy = Int32(exactly: dy) else {
            throw RPCError.invalidParams("scroll delta is outside the supported range")
        }
        guard dx != 0 || dy != 0,
              let template = CGEvent(scrollWheelEvent2Source: source, units: .pixel, wheelCount: 2, wheel1: dy, wheel2: dx, wheel3: 0),
              let event = mouse(.mouseMoved, at: point) else { throw RPCError.invalidParams("scroll requires a nonzero delta and a window-bound event") }
        event.type = .scrollWheel
        for field in [CGEventField.scrollWheelEventDeltaAxis1, .scrollWheelEventDeltaAxis2,
                      .scrollWheelEventFixedPtDeltaAxis1, .scrollWheelEventFixedPtDeltaAxis2,
                      .scrollWheelEventPointDeltaAxis1, .scrollWheelEventPointDeltaAxis2,
                      .scrollWheelEventIsContinuous, .scrollWheelEventScrollPhase, .scrollWheelEventMomentumPhase] {
            event.setIntegerValueField(field, value: template.getIntegerValueField(field))
        }
        try post(event, point: point)
    }

    func type(_ text: String) async throws {
        guard !text.isEmpty else { throw RPCError.invalidParams("text must not be empty") }
        let field = try WindowSafety.focusedElement(target)
        var sent = 0
        do {
            for chunk in Input.textChunks(text) {
                try await checkpoint()
                try await Input.refuseIfSecureFieldFocused(target)
                guard CFEqual(field, try WindowSafety.focusedElement(target)) else {
                    throw RPCError.denied("the target field changed; no further text sent")
                }
                let down = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: true)
                let up = CGEvent(keyboardEventSource: source, virtualKey: 0, keyDown: false)
                var utf16 = Array(chunk.utf16)
                down?.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: &utf16)
                up?.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: &utf16)
                try pair(down: down, up: up)
                sent += chunk.count
                // Let AX and the passive monitor observe changes between chunks.
                try await Task.sleep(nanoseconds: 1_000_000)
            }
        } catch {
            if sent > 0 { throw RPCError.canceled("typing stopped after dispatching \(sent) characters; observe partial effects before another action") }
            throw error
        }
    }

    func key(_ name: String, modifiers: [String]) async throws {
        guard let code = Input.keyCodes[name.lowercased()] else { throw RPCError.invalidParams("unknown key") }
        var flags = CGEventFlags()
        for modifier in modifiers {
            switch modifier.lowercased() {
            case "cmd", "command": flags.insert(.maskCommand)
            case "shift": flags.insert(.maskShift)
            case "alt", "option": flags.insert(.maskAlternate)
            case "ctrl", "control": flags.insert(.maskControl)
            default: throw RPCError.invalidParams("unknown modifier")
            }
        }
        try await checkpoint()
        try await Input.refuseIfSecureFieldFocused(target)
        if code == 0, flags == .maskCommand {
            if try await selectAllText() { return }
            try await checkpoint()
            try await Input.refuseIfSecureFieldFocused(target)
        }
        try pair(down: CGEvent(keyboardEventSource: source, virtualKey: code, keyDown: true),
                 up: CGEvent(keyboardEventSource: source, virtualKey: code, keyDown: false), flags: flags)
    }
    private func selectAllText() async throws -> Bool {
        let field = try WindowSafety.focusedElement(target)
        let role = WindowSafety.attribute(field, kAXRoleAttribute as String) as? String
        guard role == kAXTextFieldRole as String || role == kAXTextAreaRole as String else { return false }
        var settable = DarwinBoolean(false)
        let writable = AXUIElementIsAttributeSettable(field, kAXSelectedTextRangeAttribute as CFString, &settable)
        if writable == .attributeUnsupported || (writable == .success && !settable.boolValue) { return false }
        guard writable == .success,
              let raw = WindowSafety.attribute(field, kAXNumberOfCharactersAttribute as String),
              CFGetTypeID(raw) == CFNumberGetTypeID(), let count = (raw as? NSNumber)?.intValue, count >= 0 else {
            throw RPCError.denied("the target text selection is unknown; no input sent")
        }
        var range = CFRange(location: 0, length: count)
        guard let value = AXValueCreate(.cfRange, &range) else { throw RPCError.denied("text selection could not be created") }
        try await checkpoint()
        try await Input.refuseIfSecureFieldFocused(target)
        try await checkpoint()
        guard CFEqual(field, try WindowSafety.focusedElement(target)) else {
            throw RPCError.denied("keyboard focus changed; no selection sent")
        }
        guard let finalCount = WindowSafety.attribute(field, kAXNumberOfCharactersAttribute as String),
              CFGetTypeID(finalCount) == CFNumberGetTypeID(), (finalCount as? NSNumber)?.intValue == count else {
            throw RPCError.denied("the target text changed; no selection sent")
        }
        try WindowSafety.ensure(target)
        guard AXUIElementSetAttributeValue(field, kAXSelectedTextRangeAttribute as CFString, value) == .success else {
            throw RPCError.denied("text selection was not confirmed; observe before another action")
        }
        backend = "ax_selection"
        eventsSent += 1
        return true
    }
}
