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
import CoreGraphics

enum Input {
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
        event?.post(tap: .cghidEventTap)
    }

    static func move(to point: CGPoint) {
        post(CGEvent(mouseEventSource: nil, mouseType: .mouseMoved,
                     mouseCursorPosition: point, mouseButton: .left))
    }

    static func click(at point: CGPoint, clicks: Int) {
        for n in 1...max(1, clicks) {
            for type in [CGEventType.leftMouseDown, .leftMouseUp] {
                guard let event = CGEvent(mouseEventSource: nil, mouseType: type,
                                          mouseCursorPosition: point, mouseButton: .left) else { continue }
                // Without the click count a double click is two single
                // clicks, which is a different gesture: a file opens on one
                // and is renamed on the other.
                event.setIntegerValueField(.mouseEventClickState, value: Int64(n))
                event.post(tap: .cghidEventTap)
            }
        }
    }

    static func drag(from start: CGPoint, to end: CGPoint) {
        post(CGEvent(mouseEventSource: nil, mouseType: .leftMouseDown,
                     mouseCursorPosition: start, mouseButton: .left))
        // One intermediate move: an application watching for a drag needs to
        // see the pointer travel, and a down followed immediately by an up at
        // another point reads as a click in the wrong place.
        post(CGEvent(mouseEventSource: nil, mouseType: .leftMouseDragged,
                     mouseCursorPosition: CGPoint(x: (start.x + end.x) / 2, y: (start.y + end.y) / 2),
                     mouseButton: .left))
        post(CGEvent(mouseEventSource: nil, mouseType: .leftMouseDragged,
                     mouseCursorPosition: end, mouseButton: .left))
        post(CGEvent(mouseEventSource: nil, mouseType: .leftMouseUp,
                     mouseCursorPosition: end, mouseButton: .left))
    }

    static func scroll(at point: CGPoint, dx: Int, dy: Int) {
        move(to: point)
        post(CGEvent(scrollWheelEvent2Source: nil, units: .pixel,
                     wheelCount: 2, wheel1: Int32(dy), wheel2: Int32(dx), wheel3: 0))
    }

    /// Types literal text. Unicode goes in as a string rather than as
    /// keycodes: a keycode table is per-layout, and somebody on a Spanish
    /// keyboard would otherwise get a different character than they asked for.
    static func type(_ text: String) throws {
        try refuseIfSecureFieldFocused()
        for chunk in text.chunked(20) {
            guard let down = CGEvent(keyboardEventSource: nil, virtualKey: 0, keyDown: true),
                  let up = CGEvent(keyboardEventSource: nil, virtualKey: 0, keyDown: false)
            else { continue }
            var utf16 = Array(chunk.utf16)
            down.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: &utf16)
            up.keyboardSetUnicodeString(stringLength: utf16.count, unicodeString: &utf16)
            down.post(tap: .cghidEventTap)
            up.post(tap: .cghidEventTap)
        }
    }

    /// Presses one key, with modifiers. Named keys rather than numbers so a
    /// caller never has to know this machine's keycode table.
    static func key(_ name: String, modifiers: [String]) throws {
        try refuseIfSecureFieldFocused()
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
            guard let event = CGEvent(keyboardEventSource: nil, virtualKey: code, keyDown: isDown)
            else { continue }
            event.flags = flags
            event.post(tap: .cghidEventTap)
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
