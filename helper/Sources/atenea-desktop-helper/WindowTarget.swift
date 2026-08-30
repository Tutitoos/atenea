import Foundation
import CoreGraphics

/// The exact window and image geometry associated with one screenshot.
///
/// A frame token is deliberately opaque. Callers can pass it back to an
/// action, but cannot manufacture a coordinate transform from a stale frame.
struct WindowTarget: Sendable {
    let pid: pid_t
    let bundleID: String
    let appName: String
    let windowID: CGWindowID
    let frame: CGRect
    let imageWidth: Int
    let imageHeight: Int
    let scale: CGSize
    let visible: Bool
    let capturedAt: Date

    func contains(imagePoint point: CGPoint) -> Bool {
        point.x >= 0 && point.y >= 0 && point.x <= CGFloat(imageWidth) && point.y <= CGFloat(imageHeight)
    }

    /// Convert image pixels to Quartz global points using the real dimensions.
    /// This intentionally does not assume Retina 2x or a particular display
    /// origin, so secondary displays and fractional scales work as expected.
    func globalPoint(forImagePoint point: CGPoint) throws -> CGPoint {
        guard visible, contains(imagePoint: point), imageWidth > 0, imageHeight > 0 else {
            throw RPCError.denied("the screenshot point is outside the captured window; request a new screenshot")
        }
        return CGPoint(
            x: frame.minX + point.x * frame.width / CGFloat(imageWidth),
            y: frame.minY + point.y * frame.height / CGFloat(imageHeight))
    }
}

struct CaptureFrame: Sendable {
    let id: String
    let target: WindowTarget
}

/// Actor-isolated, short-lived capture contexts. No image data is retained:
/// only the geometry needed to reject stale or cross-window actions.
actor CaptureContexts {
    static let shared = CaptureContexts()
    private var byPID: [pid_t: CaptureFrame] = [:]
    private let lifetime: TimeInterval = 30

    func store(_ frame: CaptureFrame) {
        byPID[frame.target.pid] = frame
    }

    func latest(pid: pid_t, frameID: String?) throws -> CaptureFrame {
        guard let frame = byPID[pid], Date().timeIntervalSince(frame.target.capturedAt) <= lifetime else {
            byPID[pid] = nil
            throw RPCError.denied("no valid screenshot frame exists; request a new screenshot")
        }
        if let frameID, frameID != frame.id {
            throw RPCError.denied("frame_id does not belong to the current application window; request a new screenshot")
        }
        return frame
    }

    func invalidate(pid: pid_t) {
        byPID[pid] = nil
    }
}
