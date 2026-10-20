import AppKit
import ApplicationServices
import CoreGraphics

enum WindowSafety {
    // PID delivery needs identity and geometry, never foreground activation.
    @MainActor static func ensure(_ target: WindowTarget, point: CGPoint? = nil) throws {
        guard Permissions.interactiveSession else { throw RPCError.denied("the interactive session is unavailable; no input sent") }
        guard let app = NSRunningApplication(processIdentifier: target.pid),
              !app.isTerminated, app.bundleIdentifier == target.bundleID,
              let windows = CGWindowListCopyWindowInfo(.optionOnScreenOnly, kCGNullWindowID) as? [[String: Any]],
              let info = windows.first(where: {
                  ($0[kCGWindowNumber as String] as? NSNumber)?.uint32Value == target.windowID &&
                  ($0[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value == target.pid
              }), let bounds = info[kCGWindowBounds as String] as? [String: Any],
              let frame = CGRect(dictionaryRepresentation: bounds as CFDictionary), frame == target.frame,
              point.map({ frame.contains($0) }) ?? true else {
            throw RPCError.denied("the target identity, window or geometry changed; request a new screenshot")
        }
        // A PID is not a window selector. Refuse a second target-owned window
        // in front of this point rather than guess which one AppKit will use.
        if let point {
            for window in windows {
                guard (window[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value == target.pid,
                      let raw = window[kCGWindowBounds as String] as? [String: Any],
                      let rect = CGRect(dictionaryRepresentation: raw as CFDictionary), rect.contains(point) else { continue }
                guard (window[kCGWindowNumber as String] as? NSNumber)?.uint32Value == target.windowID else {
                    throw RPCError.denied("another target-owned window covers that point; request a new screenshot")
                }
                break
            }
        }
    }

    static func attribute(_ element: AXUIElement, _ name: String) -> CFTypeRef? {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, name as CFString, &value) == .success else { return nil }
        return value
    }

    static func windowMatches(_ element: AXUIElement, target: WindowTarget) -> Bool {
        var owner: pid_t = 0
        guard AXUIElementGetPid(element, &owner) == .success, owner == target.pid,
              let position = attribute(element, kAXPositionAttribute as String),
              let size = attribute(element, kAXSizeAttribute as String),
              CFGetTypeID(position) == AXValueGetTypeID(), CFGetTypeID(size) == AXValueGetTypeID() else { return false }
        var point = CGPoint.zero
        var extent = CGSize.zero
        // swiftlint:disable:next force_cast
        guard AXValueGetValue(position as! AXValue, .cgPoint, &point),
              // swiftlint:disable:next force_cast
              AXValueGetValue(size as! AXValue, .cgSize, &extent) else { return false }
        guard CGRect(origin: point, size: extent) == target.frame,
              let windows = CGWindowListCopyWindowInfo(.optionOnScreenOnly, kCGNullWindowID) as? [[String: Any]] else { return false }
        let matches = windows.filter { info in
            guard (info[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value == target.pid,
                  let bounds = info[kCGWindowBounds as String] as? [String: Any],
                  let frame = CGRect(dictionaryRepresentation: bounds as CFDictionary) else { return false }
            return frame == target.frame
        }
        return matches.count == 1 && (matches[0][kCGWindowNumber as String] as? NSNumber)?.uint32Value == target.windowID
    }

    static func window(of element: AXUIElement) -> AXUIElement? {
        guard let value = attribute(element, kAXWindowAttribute as String), CFGetTypeID(value) == AXUIElementGetTypeID() else { return nil }
        // swiftlint:disable:next force_cast
        return (value as! AXUIElement)
    }

    static func focusedElement(_ target: WindowTarget) throws -> AXUIElement {
        let application = AXUIElementCreateApplication(target.pid)
        AXUIElementSetMessagingTimeout(application, 0.5)
        guard let raw = attribute(application, kAXFocusedUIElementAttribute as String),
              CFGetTypeID(raw) == AXUIElementGetTypeID() else {
            throw RPCError.denied("the target application's focused element is unknown; no keyboard event sent")
        }
        // swiftlint:disable:next force_cast
        let element = raw as! AXUIElement
        var pid: pid_t = 0
        guard AXUIElementGetPid(element, &pid) == .success, pid == target.pid,
              let window = window(of: element), windowMatches(window, target: target) else {
            throw RPCError.denied("keyboard focus does not belong to the captured window; no keyboard event sent")
        }
        return element
    }
}
