// Walking an accessibility tree, bounded.
//
// The bound that matters is TIME, not size, and that is a measurement rather
// than a preference. On the machine this was written for, Finder's whole tree
// was 349 nodes and 17KB in 122ms; Google Chrome's was 1513 nodes and 91KB in
// 609ms. The bytes are nothing -- a tenth of what the transport already
// carries for a search result. The milliseconds are the cost, because a tree
// is walked over IPC one message per node, and a single unresponsive
// application can hold the whole walk.
//
// So there is a deadline, and the node and byte ceilings are a second net
// under it rather than the main one. Every one of them says so when it stops.
import Foundation
import ApplicationServices
import AppKit

struct TreeLimits {
    var deadline: Date
    var maxNodes: Int
    var maxBytes: Int
    var maxDepth: Int
}

/// Why a walk stopped early, empty when it did not.
enum Truncation: String {
    case none = ""
    case time = "time budget reached"
    case nodes = "node ceiling reached"
    case bytes = "byte ceiling reached"
    case depth = "depth ceiling reached"
}

enum Tree {
    /// Reads one attribute as text. Anything that is not a string or a number
    /// is skipped rather than described: `AXValue` of a size or a point
    /// stringifies into noise nobody can act on.
    private static func text(_ el: AXUIElement, _ attribute: String) -> String? {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(el, attribute as CFString, &value) == .success else {
            return nil
        }
        if let s = value as? String { return s.isEmpty ? nil : s }
        if let n = value as? NSNumber { return n.stringValue }
        return nil
    }

    /// Walks one application's tree into flat rows.
    ///
    /// Flat with a `depth` on each row rather than nested, because the result
    /// crosses a JSON boundary and then a model's context: a nested shape
    /// costs a brace per level and buys nothing a depth column does not say.
    static func walk(pid: pid_t, bundleID: String, appName: String,
                     roles: Set<String>, limits: TreeLimits) -> ([[String: Any]], Truncation) {
        let root = AXUIElementCreateApplication(pid)
        // Without this one unresponsive application holds the entire walk:
        // the default is measured in tens of seconds, per message.
        AXUIElementSetMessagingTimeout(root, 2.0)

        var rows: [[String: Any]] = []
        var bytes = 0
        var stopped = Truncation.none

        func visit(_ el: AXUIElement, _ depth: Int) {
            if stopped != .none { return }
            if Date() > limits.deadline { stopped = .time; return }
            if rows.count >= limits.maxNodes { stopped = .nodes; return }
            if bytes >= limits.maxBytes { stopped = .bytes; return }

            let role = text(el, kAXRoleAttribute as String) ?? "AXUnknown"
            if roles.isEmpty || roles.contains(role) {
                var row: [String: Any] = [
                    "role": role,
                    "depth": depth,
                    // Provenance travels on every row, not once per response.
                    // A caller may hold rows from several applications at
                    // once, and "which window did this sentence come from" is
                    // the question that decides whether it may be acted on.
                    "app": appName,
                    "bundle_id": bundleID,
                ]
                for (key, attribute) in [("title", kAXTitleAttribute),
                                         ("value", kAXValueAttribute),
                                         ("description", kAXDescriptionAttribute)] {
                    if let t = text(el, attribute as String) {
                        row[key] = t
                        bytes += t.utf8.count
                    }
                }
                rows.append(row)
            }

            if depth >= limits.maxDepth { stopped = .depth; return }
            var children: CFTypeRef?
            guard AXUIElementCopyAttributeValue(el, kAXChildrenAttribute as CFString, &children) == .success,
                  let list = children as? [AXUIElement] else { return }
            for child in list { visit(child, depth + 1) }
        }

        visit(root, 0)
        return (rows, stopped)
    }
}
