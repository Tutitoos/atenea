import XCTest
@testable import atenea_desktop_helper

final class CaptureContextTests: XCTestCase {
    func testExpiredFrameIsRejected() async {
        let contexts = CaptureContexts()
        let target = WindowTarget(pid: 2, bundleID: "x", appName: "X", windowID: 3,
                                  frame: .zero, imageWidth: 10, imageHeight: 10,
                                  scale: CGSize(width: 1, height: 1), visible: true,
                                  capturedAt: .distantPast)
        await contexts.store(CaptureFrame(id: "old", target: target))
        do {
            _ = try await contexts.latest(pid: 2, frameID: "old")
            XCTFail("expired frame accepted")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "denied")
        } catch {
            XCTFail("unexpected error: \(error)")
        }
    }

    func testWrongFrameTokenIsRejected() async throws {
        let contexts = CaptureContexts()
        let target = WindowTarget(pid: 4, bundleID: "x", appName: "X", windowID: 5,
                                  frame: CGRect(x: 10, y: 10, width: 100, height: 100),
                                  imageWidth: 100, imageHeight: 100,
                                  scale: CGSize(width: 1, height: 1), visible: true,
                                  capturedAt: Date())
        await contexts.store(CaptureFrame(id: "new", target: target))
        do {
            _ = try await contexts.latest(pid: 4, frameID: "other")
            XCTFail("wrong frame token accepted")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "denied")
        }
    }
}
