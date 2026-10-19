import XCTest
@testable import atenea_desktop_helper

final class AppsTests: XCTestCase {
    @MainActor
    func testOpeningRejectsPathsURLsAndMissingBundleIdentifiers() async throws {
        for bundle in ["", "/System/Applications/Calculator.app", "https://example.invalid", "io.atenea.test.missing.application"] {
            do {
                _ = try await Apps.open(bundleID: bundle, visualFeedback: false)
                XCTFail("an unresolved application must not be opened")
            } catch let error as RPCError {
                XCTAssertEqual(error.kind, "invalid")
            }
        }
    }

    @MainActor
    func testOpeningCannotBypassAnExistingHumanPause() async throws {
        try await ActionGate.shared.begin()
        await ActionGate.shared.interrupt()
        do {
            _ = try await PendingApplicationOpen().wait(until: Date().addingTimeInterval(5))
            XCTFail("an interrupted launch wait must stop without waiting for native completion")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "canceled")
        }
        await ActionGate.shared.finish()
        do {
            _ = try await Apps.open(bundleID: "io.atenea.test.missing.application")
            XCTFail("opening must refuse before resolving or dispatching while paused")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "denied")
            XCTAssertTrue(error.message.contains("paused"))
        }
        let paused = await ActionGate.shared.isPaused()
        XCTAssertTrue(paused)
        await ActionGate.shared.resume()
    }

    @MainActor
    func testNativeCompletionWaitIsBoundedEvenIfCallbackNeverArrives() async throws {
        let started = Date()
        do {
            _ = try await PendingApplicationOpen().wait(until: started.addingTimeInterval(0.05))
            XCTFail("an unconfirmed launch must not succeed")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "denied")
            XCTAssertTrue(error.message.contains("may have opened"))
        }
        XCTAssertLessThan(Date().timeIntervalSince(started), 1)
    }

    func testRecognizesTransportAndLegacyCancellationMessages() {
        XCTAssertTrue(isCancellation(["method": "notifications/canceled", "params": ["requestId": 42]]))
        XCTAssertTrue(isCancellation(["method": "$/cancelRequest", "params": ["id": 42]]))
        XCTAssertFalse(isCancellation(["method": "tools/call"]))
    }
}
