import XCTest
@testable import atenea_desktop_helper

final class VisualCoordinatesTests: XCTestCase {
    @MainActor
    func testQuartzConversionUsesPrimaryOriginForEveryMonitor() {
        for (input, expected) in [
            (CGRect(x: 100, y: -700, width: 400, height: 300), CGRect(x: 100, y: 1300, width: 400, height: 300)),
            (CGRect(x: -900, y: 200, width: 400, height: 300), CGRect(x: -900, y: 400, width: 400, height: 300)),
            (CGRect(x: 1500, y: 500, width: 400, height: 300), CGRect(x: 1500, y: 100, width: 400, height: 300))
        ] {
            XCTAssertEqual(VisualFeedbackController.appKitFrame(input, primaryHeight: 900), expected)
        }
    }
}
