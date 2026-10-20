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
    @MainActor
    static func open(bundleID: String, visualFeedback: Bool = true) async throws -> [String: Any] {
        guard !bundleID.isEmpty else { throw RPCError.invalidParams("bundle_id is required") }
        try await ActionGate.shared.begin(visualFeedback: visualFeedback)
        do {
            guard let url = NSWorkspace.shared.urlForApplication(withBundleIdentifier: bundleID),
                  let bundle = Bundle(url: url), bundle.bundleIdentifier == bundleID else {
                throw RPCError.invalidParams("no installed application matches that bundle identifier")
            }
            guard Permissions.interactiveSession else { throw RPCError.denied("the interactive session is unavailable; no open request sent") }
            let configuration = NSWorkspace.OpenConfiguration()
            configuration.activates = true
            configuration.promptsUserIfNeeded = false
            configuration.addsToRecentItems = false
            try await ActionGate.shared.checkpoint()
            // One Launch Services request also reopens an existing instance;
            // activate(options:) alone may leave a background helper ignored.
            let pending = PendingApplicationOpen()
            NSWorkspace.shared.openApplication(at: url, configuration: configuration) { app, _ in
                let reply: Result<pid_t, RPCError>
                if let app, app.bundleIdentifier == bundleID, app.processIdentifier > 0 {
                    reply = .success(app.processIdentifier)
                } else {
                    reply = .failure(.denied("native open was not confirmed; observe before another action"))
                }
                Task { @MainActor in pending.reply = reply }
            }
            let pid = try await pending.wait(until: Date().addingTimeInterval(5))
            let deadline = Date().addingTimeInterval(2)
            repeat {
                try await ActionGate.shared.checkpoint()
                guard Permissions.interactiveSession else { throw RPCError.denied("the interactive session became unavailable; observe before another action") }
                guard let app = NSRunningApplication(processIdentifier: pid), app.bundleIdentifier == bundleID,
                      app.activationPolicy == .regular, !app.isTerminated else {
                    throw RPCError.denied("the opened process does not match a regular application; observe before another action")
                }
                if app.isActive && NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier {
                    await ActionGate.shared.finish()
                    return ["action_sent": true, "application": app.localizedName ?? bundleID,
                            "bundle_id": bundleID, "pid": Int(app.processIdentifier), "frontmost": true]
                }
                try await Task.sleep(nanoseconds: 50_000_000)
            } while Date() < deadline
            throw RPCError.denied("the application may have opened but did not become frontmost; observe before another action")
        } catch {
            await ActionGate.shared.finish()
            throw error
        }
    }

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

@MainActor
final class PendingApplicationOpen {
    var reply: Result<pid_t, RPCError>?

    func wait(until deadline: Date) async throws -> pid_t {
        repeat {
            try await ActionGate.shared.checkpoint()
            if let reply { return try reply.get() }
            try await Task.sleep(nanoseconds: 50_000_000)
        } while Date() < deadline
        // Launch Services may still finish after this answer; never replay it.
        throw RPCError.denied("the application may have opened but launch was not confirmed in time; observe before another action")
    }
}
