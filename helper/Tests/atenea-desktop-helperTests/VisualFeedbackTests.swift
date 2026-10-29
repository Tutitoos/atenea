import AppKit
import XCTest
@testable import atenea_desktop_helper

final class VisualFeedbackTests: XCTestCase {
    @MainActor
    func testMinimizedPreviewStaysMinimizedDuringUpdatesAndCanRestore() async throws {
        _ = NSApplication.shared
        let controller = VisualFeedbackController.shared
        let target = WindowTarget(pid: getpid(), bundleID: "io.atenea.preview-test",
                                  appName: "Preview test", windowID: 0,
                                  frame: CGRect(x: 100, y: 100, width: 360, height: 240),
                                  imageWidth: 360, imageHeight: 240, scale: CGSize(width: 1, height: 1),
                                  visible: true, capturedAt: Date())
        defer { controller.hide() }
        // A paused preview exercises the real panel without starting screen capture.
        controller.show(target: target, state: .pausedByUser)
        let panel = try XCTUnwrap(NSApplication.shared.windows.first { $0.title == "Atenea" })
        XCTAssertTrue(panel.standardWindowButton(.miniaturizeButton)?.isEnabled == true)
        panel.performMiniaturize(nil)
        try await Task.sleep(nanoseconds: 700_000_000)
        XCTAssertTrue(panel.isMiniaturized)
        controller.show(target: target, state: .pausedByUser)
        controller.pause(byHumanInput: true)
        try await Task.sleep(nanoseconds: 700_000_000)
        XCTAssertTrue(panel.isMiniaturized)
        panel.deminiaturize(nil)
        try await Task.sleep(nanoseconds: 700_000_000)
        XCTAssertFalse(panel.isMiniaturized)
        XCTAssertTrue(panel.isVisible)
    }
}
