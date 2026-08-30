import XCTest
@testable import atenea_desktop_helper

final class WindowTargetTests: XCTestCase {
    func testCoordinateMappingUsesActualImageDimensionsAndNegativeOrigin() throws {
        let target = WindowTarget(pid: 9, bundleID: "example.app", appName: "Example",
                                  windowID: 12, frame: CGRect(x: -1440, y: 120, width: 720, height: 480),
                                  imageWidth: 900, imageHeight: 600,
                                  scale: CGSize(width: 1.25, height: 1.25), visible: true,
                                  capturedAt: Date())
        XCTAssertEqual(try target.globalPoint(forImagePoint: CGPoint(x: 450, y: 300)),
                       CGPoint(x: -1080, y: 360))
    }

    func testOutOfBoundsCoordinatesAreRejected() {
        let target = WindowTarget(pid: 1, bundleID: "x", appName: "X", windowID: 1,
                                  frame: CGRect(x: 0, y: 0, width: 100, height: 100),
                                  imageWidth: 100, imageHeight: 100,
                                  scale: CGSize(width: 1, height: 1), visible: true,
                                  capturedAt: Date())
        XCTAssertThrowsError(try target.globalPoint(forImagePoint: CGPoint(x: 101, y: 10)))
    }
}
