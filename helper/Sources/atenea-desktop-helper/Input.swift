// Synthetic mouse and keyboard, with the one refusal that cannot be made
// anywhere else.
//
// Everything about WHICH application may be touched is decided in Go, where
// the settings file is read. What is decided here is narrower and has to be:
// whether the thing about to receive keystrokes is a password field. Only the
// process holding the accessibility connection can ask, and it has to ask at
// the moment of typing -- focus moves, and a check made a round trip earlier
// is a check made about a different field.
//
// Coordinates arrive in the space of the image the caller was given, which is
// the space Capture already reduced to. Nothing here multiplies anything: the
// scaling lives in one file and this is not it.
import Foundation
import ApplicationServices
@preconcurrency import CoreGraphics

enum Input {
	static let marker: Int64 = 0x4154454E4541
    private static let eventSource: CGEventSource? = {
        let source = CGEventSource(stateID: .combinedSessionState)
        source?.userData = marker
        return source
    }()

    /// Refuses when the focused element takes a password.
    ///
    /// macOS marks these itself -- AXSecureTextField -- so this is a fact the
    /// system reports rather than a guess from a field's name or placeholder.
    /// A refusal rather than a filter: a caller that meant to fill a password
    /// manager has to be told no, not quietly given a redacted string that
    /// looks like it worked.
    static func refuseIfSecureFieldFocused() throws {
        let system = AXUIElementCreateSystemWide()
        AXUIElementSetMessagingTimeout(system, 2.0)
        var focused: CFTypeRef?
        guard AXUIElementCopyAttributeValue(
            system, kAXFocusedUIElementAttribute as CFString, &focused) == .success,
            let element = focused else { return }
        // swiftlint:disable:next force_cast
        let el = element as! AXUIElement
        var role: CFTypeRef?
        guard AXUIElementCopyAttributeValue(el, kAXRoleAttribute as CFString, &role) == .success,
              let name = role as? String else { return }
        if name == "AXSecureTextField" {
            throw RPCError.denied(
                "the focused field is a secure text field: typing into a password field is refused")
        }
    }

    private static func post(_ event: CGEvent?) {
        guard let event else { return }
        event.setIntegerValueField(.eventSourceUserData, value: marker)
        event.post(tap: .cghidEventTap)
    }

    static func move(to point: CGPoint, visualFeedback: Bool = true) async throws {
        try await ActionGate.shared.begin(visualFeedback: visualFeedback)
        do {
            try await ActionGate.shared.checkpoint()
            post(CGEvent(mouseEventSource: eventSource, mouseType: .mouseMoved,
                         mouseCursorPosition: point, mouseButton: .left))
            try await ActionGate.shared.checkpoint()
            await ActionGate.shared.finish()
        } catch {
            await ActionGate.shared.finish()
            throw error
        }
    }

    static func click(at point: CGPoint, clicks: Int, visualFeedback: Bool = true) async throws {
        let original = CGEvent(source: nil)?.location
        try await ActionGate.shared.begin(visualFeedback: visualFeedback)
        do {
            try await ActionGate.shared.checkpoint()
            for n in 1...max(1, clicks) {
                for type in [CGEventType.leftMouseDown, .leftMouseUp] {
                    guard let event = CGEvent(mouseEventSource: eventSource, mouseType: type,
                                              mouseCursorPosition: point, mouseButton: .left) else { continue }
                    // Without the click count a double click is two single
                    // clicks, which is a different gesture: a file opens on one
                    // and is renamed on the other.
                    event.setIntegerValueField(.mouseEventClickState, value: Int64(n))
                    post(event)
                }
            }
            if let original, !(await ActionGate.shared.wasInterrupted()) {
                post(CGEvent(mouseEventSource: eventSource, mouseType: .mouseMoved,
                             mouseCursorPosition: original, mouseButton: .left))
            }
            try await ActionGate.shared.checkpoint()
            await ActionGate.shared.finish()
        } catch {
            await ActionGate.shared.finish()
            throw error
        }
    }

    /// Prefer the Accessibility action so a normal button can be activated
    /// without moving the user's real pointer. Canvas and emulator surfaces
    /// return false and use the guarded CGEvent fallback in the caller.
    static func semanticClick(pid: pid_t, at point: CGPoint, visualFeedback: Bool = true) async throws -> Bool {
        try await ActionGate.shared.begin(visualFeedback: visualFeedback)
        do {
            try await ActionGate.shared.checkpoint()
            let system = AXUIElementCreateSystemWide()
            var raw: AXUIElement?
            guard AXUIElementCopyElementAtPosition(system, Float(point.x), Float(point.y), &raw) == .success,
                  let element = raw else {
                await ActionGate.shared.finish()
                return false
            }
            var owner: pid_t = 0
            guard AXUIElementGetPid(element, &owner) == .success, owner == pid else {
                await ActionGate.shared.finish()
                return false
            }
            var actions: CFArray?
            guard AXUIElementCopyActionNames(element, &actions) == .success,
                  let names = actions as? [String], names.contains(kAXPressAction as String) else {
                await ActionGate.shared.finish()
                return false
            }
            let pressed = AXUIElementPerformAction(element, kAXPressAction as CFString) == .success
            try await ActionGate.shared.checkpoint()
            await ActionGate.shared.finish()
            return pressed
        } catch {
            await ActionGate.shared.finish()
            throw error
        }
    }

    static func drag(from start: CGPoint, to end: CGPoint, visualFeedback: Bool = true) async throws {
        try await ActionGate.shared.begin(visualFeedback: visualFeedback)
        var buttonDown = false
        defer {
            if buttonDown {
                post(CGEvent(mouseEventSource: eventSource, mouseType: .leftMouseUp,
                             mouseCursorPosition: end, mouseButton: .left))
            }
        }
        do {
            try await ActionGate.shared.checkpoint()
            post(CGEvent(mouseEventSource: eventSource, mouseType: .leftMouseDown,
                         mouseCursorPosition: start, mouseButton: .left))
            buttonDown = true
            // A continuous, bounded path keeps canvas/emulator surfaces from
            // reading the gesture as a teleport. The same points update the
            // local cursor, so the preview never gets ahead of the real drag.
            for step in 1...18 {
                try await ActionGate.shared.checkpoint()
                let progress = CGFloat(step) / 18
                let point = CGPoint(x: start.x + (end.x - start.x) * progress,
                                    y: start.y + (end.y - start.y) * progress)
                post(CGEvent(mouseEventSource: eventSource, mouseType: .leftMouseDragged,
                             mouseCursorPosition: point, mouseButton: .left))
                if visualFeedback {
                    await MainActor.run { VisualFeedbackController.shared.setCursor(globalPoint: point) }
                    try await Task.sleep(nanoseconds: 16_000_000)
                }
            }
            try await ActionGate.shared.checkpoint()
            post(CGEvent(mouseEventSource: eventSource, mouseType: .leftMouseUp,
                         mouseCursorPosition: end, mouseButton: .left))
            buttonDown = false
            await ActionGate.shared.finish()
        } catch {
            await ActionGate.shared.finish()
            throw error
        }
    }

    static func scroll(at point: CGPoint, dx: Int, dy: Int, visualFeedback: Bool = true) async throws {
        let original = CGEvent(source: nil)?.location
        try await ActionGate.shared.begin(visualFeedback: visualFeedback)
        do {
            try await ActionGate.shared.checkpoint()
            post(CGEvent(mouseEventSource: eventSource, mouseType: .mouseMoved,
                         mouseCursorPosition: point, mouseButton: .left))
            post(CGEvent(scrollWheelEvent2Source: eventSource, units: .pixel,
                         wheelCount: 2, wheel1: Int32(dy), wheel2: Int32(dx), wheel3: 0))
            if let original, !(await ActionGate.shared.wasInterrupted()) {
                post(CGEvent(mouseEventSource: eventSource, mouseType: .mouseMoved,
                             mouseCursorPosition: original, mouseButton: .left))
            }
            try await ActionGate.shared.checkpoint()
            await ActionGate.shared.finish()
        } catch {
            await ActionGate.shared.finish()
            throw error
        }
    }

    /// Types literal text. Unicode goes in as a string rather than as
    /// keycodes: a keycode table is per-layout, and somebody on a Spanish
    /// keyboard would otherwise get a different character than they asked for.
    static func type(_ text: String, visualFeedback: Bool = true) async throws {
        try refuseIfSecureFieldFocused()
        try await ActionGate.shared.begin(visualFeedback: visualFeedback)
        do {
            var sent = 0
            for chunk in text.chunked(20) {
                do { try await ActionGate.shared.checkpoint() }
                catch _ as RPCError where sent > 0 {
                    throw RPCError.canceled("interrupted by human input after \(sent) characters; typing may have been partially applied")
                }
                guard let down = CGEvent(keyboardEventSource: eventSource, virtualKey: 0, keyDown: true),
                      let up = CGEvent(keyboardEventSource: eventSource, virtualKey: 0, keyDown: false)
                else { continue }
                var utf16 = Array(chunk.utf16)
                down.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: &utf16)
                up.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: &utf16)
                post(down)
                post(up)
                sent += chunk.count
            }
            do { try await ActionGate.shared.checkpoint() }
            catch _ as RPCError {
                throw RPCError.canceled("interrupted by human input after (sent) characters; typing may have been partially applied")
            }
            await ActionGate.shared.finish()
        } catch {
            await ActionGate.shared.finish()
            throw error
        }
    }

    /// Presses one key, with modifiers. Named keys rather than numbers so a
    /// caller never has to know this machine's keycode table.
    static func key(_ name: String, modifiers: [String], visualFeedback: Bool = true) async throws {
        try refuseIfSecureFieldFocused()
        try await ActionGate.shared.begin(visualFeedback: visualFeedback)
        do {
            try await ActionGate.shared.checkpoint()
            guard let code = keyCodes[name.lowercased()] else {
                throw RPCError.invalidParams("unknown key \(name); known: \(keyCodes.keys.sorted().joined(separator: ", "))")
            }
            var flags = CGEventFlags()
            for modifier in modifiers {
                switch modifier.lowercased() {
                case "cmd", "command": flags.insert(.maskCommand)
                case "shift": flags.insert(.maskShift)
                case "alt", "option": flags.insert(.maskAlternate)
                case "ctrl", "control": flags.insert(.maskControl)
                default: throw RPCError.invalidParams("unknown modifier \(modifier)")
                }
            }
            for isDown in [true, false] {
                guard let event = CGEvent(keyboardEventSource: eventSource, virtualKey: code, keyDown: isDown)
                else { continue }
                event.flags = flags
                post(event)
            }
            try await ActionGate.shared.checkpoint()
            await ActionGate.shared.finish()
        } catch {
            await ActionGate.shared.finish()
            throw error
        }
    }

    // Deliberately small. Every entry here is a key somebody asked for, and a
    // table that tried to be complete would be a table nobody checked.
    private static let keyCodes: [String: CGKeyCode] = [
        "return": 36, "enter": 36, "tab": 48, "space": 49, "delete": 51,
        "escape": 53, "esc": 53, "left": 123, "right": 124, "down": 125, "up": 126,
        "home": 115, "end": 119, "pageup": 116, "pagedown": 121,
        "a": 0, "c": 8, "v": 9, "x": 7, "z": 6, "s": 1, "f": 3, "w": 13, "q": 12,
    ]
}

private extension String {
    /// CGEvent's unicode payload is bounded; a long string posted in one event
    /// is silently truncated.
    func chunked(_ size: Int) -> [String] {
        var out: [String] = []
        var current = ""
        for character in self {
            current.append(character)
            if current.count >= size { out.append(current); current = "" }
        }
        if !current.isEmpty { out.append(current) }
        return out
    }
}
