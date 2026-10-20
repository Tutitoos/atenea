import XCTest
@testable import atenea_desktop_helper

final class RequestCancellationTests: XCTestCase {
    func testCancelingQueuedRequestDoesNotInterruptActiveRequestOrRunLater() async throws {
        let active = RPCRequestID.integer(7001)
        let queued = RPCRequestID.integer(7002)
        let cancellation = RequestCancellation.shared
        cancellation.register(active)
        cancellation.register(queued)
        defer { cancellation.finish(active); cancellation.finish(queued) }
        let gate = ActionGate()
        try await RPCRequestContext.$id.withValue(active) {
            try await gate.begin(visualFeedback: false)
            cancellation.cancel(queued)
            try await gate.checkpoint()
            await gate.finish()
        }
        var dispatched = false
        do {
            try await RPCRequestContext.$id.withValue(queued) {
                try await gate.begin(visualFeedback: false)
                dispatched = true
                await gate.finish()
            }
            XCTFail("the canceled queued request must not run")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "canceled")
        }
        XCTAssertFalse(dispatched)
    }

    func testActiveCancellationSurvivesGateFinishUntilRequestCompletes() async throws {
        let id = RPCRequestID.string("active")
        let cancellation = RequestCancellation.shared
        cancellation.register(id)
        defer { cancellation.finish(id) }
        let gate = ActionGate()
        try await RPCRequestContext.$id.withValue(id) {
            try await gate.begin(visualFeedback: false)
            cancellation.cancel(id)
            do { try await gate.checkpoint(); XCTFail("canceled request continued") }
            catch let error as RPCError { XCTAssertEqual(error.kind, "canceled") }
            await gate.finish()
            do { try await gate.begin(); XCTFail("finishing must not clear request cancellation") }
            catch let error as RPCError { XCTAssertEqual(error.kind, "canceled") }
        }
        cancellation.finish(id)
        cancellation.register(id)
        try await RPCRequestContext.$id.withValue(id) {
            try await gate.begin(visualFeedback: false)
            try await gate.checkpoint()
            await gate.finish()
        }
    }
}
