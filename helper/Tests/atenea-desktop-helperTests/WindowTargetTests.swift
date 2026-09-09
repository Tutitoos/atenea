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

    func testDominantDisplayUsesLargestIntersectionAndKeepsEveryDisplay() {
        let displays = [
            DisplayTarget(id: 11, frame: CGRect(x: -1200, y: 0, width: 1200, height: 900), scale: 1.25),
            DisplayTarget(id: 22, frame: CGRect(x: 0, y: 0, width: 1728, height: 1117), scale: 2.0),
        ]
        let (dominant, intersecting) = Capture.displays(
            for: CGRect(x: -300, y: 100, width: 900, height: 600), from: displays)
        XCTAssertEqual(dominant?.id, 22)
        XCTAssertEqual(intersecting.map(\.id), [11, 22])
    }

    func testTopologyGenerationChangesForScaleRotationAndArrangement() {
        let original = [DisplayTarget(id: 1, frame: CGRect(x: 0, y: 0, width: 1000, height: 800), scale: 1.5)]
        let rescaled = [DisplayTarget(id: 1, frame: CGRect(x: 0, y: 0, width: 1000, height: 800), scale: 2.0)]
        let rotated = [DisplayTarget(id: 1, frame: CGRect(x: 0, y: 0, width: 800, height: 1000), scale: 1.5)]
        let moved = [DisplayTarget(id: 1, frame: CGRect(x: -1000, y: 0, width: 1000, height: 800), scale: 1.5)]
        XCTAssertNotEqual(Capture.geometryGeneration(original), Capture.geometryGeneration(rescaled))
        XCTAssertNotEqual(Capture.geometryGeneration(original), Capture.geometryGeneration(rotated))
        XCTAssertNotEqual(Capture.geometryGeneration(original), Capture.geometryGeneration(moved))
    }
}
