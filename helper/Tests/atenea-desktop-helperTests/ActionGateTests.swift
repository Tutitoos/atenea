import XCTest
@testable import atenea_desktop_helper

final class ActionGateTests: XCTestCase {
    func testFastPhysicalPauseResumeCannotReviveActiveAction() async throws {
        let epoch = HumanInputEpoch()
        let gate = ActionGate(inputEpoch: epoch)
        try await gate.begin(targetPID: 42)
        epoch.pause()
        epoch.resume()
        do { try await gate.checkpoint(); XCTFail("fast Pause/Resume revived active input") }
        catch let error as RPCError { XCTAssertEqual(error.kind, "canceled") }
        await gate.finish()
        try await gate.begin(targetPID: 42)
        try await gate.checkpoint()
        await gate.finish()
    }

    func testHumanInputInOtherAppDoesNotInterruptCUA() async throws {
        let epoch = HumanInputEpoch()
        let gate = ActionGate(inputEpoch: epoch)
        try await gate.begin(targetPID: 42)
        XCTAssertNil(epoch.record(destinationPID: 77))
        XCTAssertNil(epoch.record(destinationPID: 0))
        try await gate.checkpoint()
        let conflict = epoch.record(destinationPID: 42)
        XCTAssertNotNil(conflict)
        do { try await gate.checkpoint(); XCTFail("missed input directed to controlled app") }
        catch let error as RPCError { XCTAssertEqual(error.kind, "canceled") }
        await gate.finish()
        XCTAssertNil(epoch.record(destinationPID: 42))
        await gate.resume()
        try await gate.begin(targetPID: 77)
        let oldCallback = await gate.interrupt(observedEpoch: conflict)
        XCTAssertFalse(oldCallback)
        XCTAssertNil(epoch.record(destinationPID: 42))
        try await gate.checkpoint()
        await gate.finish()
    }

    func testUnknownRecipientFailsClosedAndManualPauseWorksWhileIdle() async throws {
        let epoch = HumanInputEpoch()
        let gate = ActionGate(inputEpoch: epoch)
        try await gate.begin(targetPID: 42)
        XCTAssertNotNil(epoch.record(destinationPID: 0, unknownDestination: true))
        do { try await gate.checkpoint(); XCTFail("unknown recipient accepted") }
        catch let error as RPCError { XCTAssertEqual(error.kind, "canceled") }
        await gate.finish()
        await gate.resume()
        epoch.pause() // The UI latch must work before its actor task is scheduled.
        do { try await gate.begin(targetPID: 42); XCTFail("idle Pause did not persist") }
        catch let error as RPCError { XCTAssertEqual(error.kind, "denied") }
        await gate.resume()
        try await gate.begin(targetPID: 42)
        try await gate.checkpoint()
        await gate.finish()
    }

    func testResumeDoesNotUncancelTheActiveAction() async throws {
        let gate = ActionGate()
        try await gate.begin()
        await gate.interrupt()
        await gate.resume()
        do { try await gate.checkpoint(); XCTFail("Resume revived the canceled action") }
        catch let error as RPCError { XCTAssertEqual(error.kind, "canceled") }
        await gate.finish()
        try await gate.begin()
        try await gate.checkpoint()
        await gate.finish()
    }

    func testSynchronousInterferenceSurvivesDelayedCallback() async throws {
        let epoch = HumanInputEpoch()
        let gate = ActionGate(inputEpoch: epoch)
        let idleEvent = epoch.record()
        try await gate.begin()
        let interruptedByIdle = await gate.interrupt(observedEpoch: idleEvent)
        XCTAssertFalse(interruptedByIdle)
        try await gate.checkpoint()
        let duringAction = epoch.record()
        do { try await gate.checkpoint(); XCTFail("missed synchronous interference") }
        catch let error as RPCError { XCTAssertEqual(error.kind, "canceled") }
        await gate.finish()
        await gate.resume()
        try await gate.begin()
        let oldCallback = await gate.interrupt(observedEpoch: duringAction)
        XCTAssertFalse(oldCallback)
        try await gate.checkpoint()
        await gate.finish()
    }

    func testEventObservedLateButCreatedBeforeActionDoesNotCancelIt() async throws {
        let epoch = HumanInputEpoch()
        let gate = ActionGate(inputEpoch: epoch)
        let oldTime = DispatchTime.now().uptimeNanoseconds
        try await Task.sleep(nanoseconds: 1_000_000)
        try await gate.begin()
        XCTAssertNil(epoch.record(destinationPID: 42, timestamp: oldTime))
        XCTAssertFalse(epoch.isPaused)
        let delayed = epoch.record(timestamp: oldTime)
        let interrupted = await gate.interrupt(observedEpoch: delayed, observedAt: oldTime)
        XCTAssertFalse(interrupted)
        try await gate.checkpoint()
        await gate.finish()
    }

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

    func testPartiallyAppliedSequenceIsCanceledWithoutRepeatingSentEvents() async throws {
        let gate = ActionGate()
        var sentEvents = 0
        try await gate.begin()
        sentEvents += 1
        await gate.interrupt()
        do {
            try await gate.checkpoint()
            XCTFail("partially applied action continued after interruption")
        } catch let error as RPCError {
            XCTAssertEqual(error.kind, "canceled")
        }
        await gate.finish()
        await gate.resume()
        XCTAssertEqual(sentEvents, 1)
        let paused = await gate.isPaused()
        XCTAssertFalse(paused)
    }
}
