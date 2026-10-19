import XCTest
@testable import atenea_desktop_helper

final class InputMonitorTests: XCTestCase {
    func testMouseDragEventsAreMonitoredAsMovement() {
        let dragTypes: [CGEventType] = [.leftMouseDragged, .rightMouseDragged, .otherMouseDragged]
        for type in dragTypes {
            XCTAssertTrue(InputMonitor.monitoredEventTypes.contains(type))
            XCTAssertTrue(InputMonitor.isMovementEvent(type))
        }
    }

    func testSyntheticInputMarkerIsExcluded() {
        guard let synthetic = CGEvent(source: nil), let unmarked = CGEvent(source: nil) else {
            XCTFail("could not create test event")
            return
        }
        synthetic.setIntegerValueField(.eventSourceUserData, value: Input.marker)
        XCTAssertTrue(InputMonitor.isSynthetic(synthetic))
        XCTAssertFalse(InputMonitor.isSynthetic(unmarked))
    }

    func testDisabledTapCancelsAndPausesUntilExplicitResume() async throws {
        let gate = ActionGate.shared
        await gate.resume()
        try await gate.begin(visualFeedback: true)

        let monitor = InputMonitor()
        guard let interruption = monitor.tapWasDisabled(.tapDisabledByTimeout) else {
            XCTFail("disabled tap notification was ignored")
            await gate.finish()
            return
        }
        await interruption.value
        XCTAssertTrue(InputMonitor.isDisabledTapEvent(.tapDisabledByUserInput))
        XCTAssertFalse(monitor.active)
        XCTAssertNotNil(monitor.failure)

        do {
            try await gate.checkpoint()
            XCTFail("disabled monitor must cancel the active action")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "canceled")
        }

        let pausedDuringAction = await gate.isPaused()
        XCTAssertTrue(pausedDuringAction)
        await gate.finish()
        let pausedAfterFinish = await gate.isPaused()
        XCTAssertTrue(pausedAfterFinish)

        do {
            try await gate.begin()
            XCTFail("paused actions must stay blocked until explicitly resumed")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "denied")
        }

        await gate.resume()
        try await gate.begin()
        await gate.finish()
    }
}
