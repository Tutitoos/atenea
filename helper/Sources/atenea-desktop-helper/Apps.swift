// Listing what is running, which is the one thing here that needs no
// permission at all: NSWorkspace answers from the process's own session.
//
// `.regular` only. Every macOS session carries dozens of agents and
// accessory processes with no window and no user-visible existence, and a
// list that included them would be a list nobody reads -- and a much wider
// statement about the user's machine than "what applications are open".
import Foundation
import AppKit

enum Apps {
    static func list() -> [[String: Any]] {
        NSWorkspace.shared.runningApplications
            .filter { $0.activationPolicy == .regular && $0.processIdentifier > 0 }
            .map { app in
                var row: [String: Any] = [
                    "pid": Int(app.processIdentifier),
                    "name": app.localizedName ?? "",
                    "frontmost": app.isActive,
                ]
                // The bundle id is what an allow-list can be written against;
                // a display name is localized and changes under the reader's
                // feet. Absent for a few processes, so it is omitted rather
                // than filled with an empty string that would look like a
                // real value in a settings file.
                if let bundle = app.bundleIdentifier { row["bundle_id"] = bundle }
                return row
            }
            .sorted { ($0["name"] as? String ?? "") < ($1["name"] as? String ?? "") }
    }
}
