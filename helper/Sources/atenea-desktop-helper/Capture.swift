// Screen capture, downscaled here and never at the caller.
//
// A Retina display reports points while the pixels underneath are twice as
// many, and both vendors' computer-use APIs document the same trap: capture at
// 2x and either reduce before sending or halve the coordinates before
// clicking. Getting it wrong is not a subtle bug -- every click lands at
// double the intended offset.
//
// The rule that keeps it out of the rest of the system: this file is the only
// place that knows the scale factor, and the answer it returns is expressed in
// the coordinate space of the IMAGE it returns. Nothing above this seam ever
// sees a point, a pixel ratio, or a CGPoint.
//
// The budget is the standard's own: 1568px on the long edge and about 1.15
// megapixels is what a model reads without further reduction, and roughly 20
// images fill a conversation. Reducing iteratively against a byte ceiling
// rather than in one step, because a single guess either overshoots into
// unreadable or undershoots and gets reduced again downstream.
import Foundation
import CoreGraphics
import ImageIO
import UniformTypeIdentifiers
import AppKit

enum Capture {
    static let maxLongEdge = 1568
    static let maxBytes = 900_000
    static let minScale = 0.25

    struct Shot {
        let png: Data
        let width: Int
        let height: Int
        /// What the returned image was multiplied by, relative to the pixels
        /// the display actually holds. Reported so a reader can tell a small
        /// window from a heavily reduced one; never needed to interpret the
        /// coordinates, which are already in the image's own space.
        let scale: Double
    }

    private static func png(_ image: CGImage) -> Data? {
        let out = NSMutableData()
        guard let dest = CGImageDestinationCreateWithData(
            out, UTType.png.identifier as CFString, 1, nil) else { return nil }
        CGImageDestinationAddImage(dest, image, nil)
        guard CGImageDestinationFinalize(dest) else { return nil }
        return out as Data
    }

    private static func resized(_ image: CGImage, to scale: Double) -> CGImage? {
        let w = max(1, Int(Double(image.width) * scale))
        let h = max(1, Int(Double(image.height) * scale))
        guard let space = image.colorSpace,
              let ctx = CGContext(data: nil, width: w, height: h, bitsPerComponent: 8,
                                  bytesPerRow: 0, space: space,
                                  bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
        else { return nil }
        ctx.interpolationQuality = .high
        ctx.draw(image, in: CGRect(x: 0, y: 0, width: w, height: h))
        return ctx.makeImage()
    }

    /// Reduces until the image is inside both ceilings, or until reducing
    /// further would make it unreadable. Returning something too large is
    /// better than returning something nobody can read: the caller can see the
    /// size and decide, and cannot un-blur a picture.
    static func fit(_ original: CGImage) -> Shot? {
        var scale = 1.0
        let longEdge = max(original.width, original.height)
        if longEdge > maxLongEdge {
            scale = Double(maxLongEdge) / Double(longEdge)
        }
        while true {
            let image = scale >= 1.0 ? original : resized(original, to: scale)
            guard let image, let data = png(image) else { return nil }
            if data.count <= maxBytes || scale <= minScale {
                return Shot(png: data, width: image.width, height: image.height, scale: scale)
            }
            scale *= 0.85
        }
    }
}

// MARK: - reaching the window server

import ScreenCaptureKit

extension Capture {
    /// Captures the frontmost window belonging to one process.
    ///
    /// One window rather than the whole display, and that is the security
    /// posture rather than a convenience: a full-screen grab carries whatever
    /// else happens to be open, including the applications the allow-list
    /// exists to keep out of frame. Asking for a window means the refusal
    /// upstream is actually enforceable.
    ///
    /// Async all the way down, with no semaphore bridging it to a synchronous
    /// caller. Bridging is what the first version did, and it crashed:
    /// blocking a thread the Swift runtime needs for the Task is a deadlock
    /// the runtime aborts on rather than hangs on -- SIGABRT inside
    /// `semaphore.wait`, measured. Putting a lock around the captured variable
    /// fixed a data race that was real and left the deadlock untouched.
    ///
    /// The call is bounded by the adapter's own timeout instead. One ceiling
    /// where there is a caller to report to beats two that can disagree.
    static func window(pid: pid_t) async throws -> Shot {
        let content = try await SCShareableContent.excludingDesktopWindows(
            true, onScreenWindowsOnly: true)
        // Largest on-screen window for the process: an application may own a
        // menu-bar item and a panel as well, and the biggest one is the
        // document somebody means.
        let candidates = content.windows
            .filter { $0.owningApplication?.processID == pid }
            .sorted { ($0.frame.width * $0.frame.height) > ($1.frame.width * $1.frame.height) }
        guard let target = candidates.first else {
            throw RPCError.denied("that application has no window on screen right now")
        }
        let config = SCStreamConfiguration()
        // The pixels the display actually holds, not the points the frame
        // reports. fit() reduces from here; capturing at point size on a
        // Retina display would throw away half the detail before anybody could
        // decide whether they needed it.
        config.width = Int(target.frame.width * 2)
        config.height = Int(target.frame.height * 2)
        let filter = SCContentFilter(desktopIndependentWindow: target)
        let image: CGImage
        do {
            image = try await SCScreenshotManager.captureImage(
                contentFilter: filter, configuration: config)
        } catch {
            throw RPCError.denied("capture failed: \(error.localizedDescription)")
        }
        guard let shot = fit(image) else {
            throw RPCError.internalError("the capture could not be encoded")
        }
        return shot
    }
}
