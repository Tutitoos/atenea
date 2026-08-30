import Foundation
import AppKit
import CoreGraphics

enum WindowSafety {
    /// Bring the selected application forward and verify that no other
    /// layer-zero window covers the point before posting a CGEvent.
    static func ensure(pid: pid_t, windowID: CGWindowID, point: CGPoint) throws {
        let deadline = Date().addingTimeInterval(0.75)
        repeat {
            if coveredByTarget(pid: pid, windowID: windowID, point: point) { return }
            if let app = NSRunningApplication(processIdentifier: pid) {
                _ = app.activate(options: [.activateAllWindows])
            }
            RunLoop.current.run(until: Date().addingTimeInterval(0.05))
        } while Date() < deadline
        throw RPCError.denied("the target window is covered or could not be focused safely")
    }

    private static func coveredByTarget(pid: pid_t, windowID: CGWindowID, point: CGPoint) -> Bool {
        guard let list = CGWindowListCopyWindowInfo(.optionOnScreenOnly, kCGNullWindowID) as? [[String: Any]] else {
            return false
        }
        for info in list {
            let layer = (info[kCGWindowLayer as String] as? NSNumber)?.intValue ?? 1
            guard layer == 0,
                  let bounds = info[kCGWindowBounds as String] as? [String: Any],
                  let rect = CGRect(dictionaryRepresentation: bounds as CFDictionary) else { continue }
            guard rect.contains(point) else { continue }
            let owner = (info[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value ?? -1
            let id = (info[kCGWindowNumber as String] as? NSNumber)?.uint32Value ?? 0
            return owner == pid && id == windowID
        }
        return false
    }
}
