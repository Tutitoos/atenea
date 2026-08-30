import XCTest
@testable import atenea_desktop_helper

final class ActionGateTests: XCTestCase {
    func testHumanInterruptionCancelsAndResumeDoesNotReplay() async throws {
        let gate = ActionGate()
        try await gate.begin()
        await gate.interrupt()
        do {
            try await gate.checkpoint()
            XCTFail("interruption must cancel the in-flight action")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "canceled")
        }
        await gate.finish()
        await gate.resume()
        try await gate.begin()
        try await gate.checkpoint()
        await gate.finish()
    }
}
