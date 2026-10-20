import Foundation

/// Serializes one desktop action and gives the passive input monitor a safe
/// cancellation point between input events.
actor ActionGate {
    static let shared = ActionGate()
    private var active = false
    private var interrupted = false
    private var visualAction = false
    private let inputEpoch: HumanInputEpoch
    private var beganAt: UInt64 = 0
    private var beganTime: UInt64 = 0

    init(inputEpoch: HumanInputEpoch = .shared) { self.inputEpoch = inputEpoch }

    func begin(visualFeedback: Bool = true, targetPID: Int32? = nil) throws {
        try RequestCancellation.shared.checkpoint()
        guard !active else { throw RPCError.denied("another desktop action is already in progress") }
        (beganAt, beganTime) = try inputEpoch.begin(targetPID: targetPID, pauseOnInput: visualFeedback)
        active = true
        visualAction = visualFeedback
        interrupted = false
    }

    func checkpoint() throws {
        try RequestCancellation.shared.checkpoint()
        if active && inputEpoch.hasInput(since: beganAt, after: beganTime) { interrupted = true }
        if interrupted {
            throw RPCError.canceled("interrupted by human input (\(inputEpoch.lastMetadata)); the action was not retried")
        }
    }

    func finish() {
        inputEpoch.end()
        active = false
        visualAction = false
        interrupted = false
    }

    /// Marks the current action as interrupted. The return value tells
    /// the event monitor whether there was an action to show as paused; idle
    /// human activity must not leave a newly-started helper looking paused.
    @discardableResult
    func interrupt(observedEpoch: UInt64? = nil, observedAt: UInt64? = nil) -> Bool {
        guard active else { return false }
        if let observedEpoch, observedEpoch <= beganAt { return false }
        if let observedAt, observedAt < beganTime { return false }
        interrupted = true
        if visualAction {
            if observedEpoch == nil { inputEpoch.pause() }
            return inputEpoch.isPaused
        }
        // Even with the presentation disabled, an observed human event must
        // stop the in-flight input sequence. There is simply no panel to mark
        // as paused in this mode.
        return false
    }

    func resume() {
        inputEpoch.resume()
    }

    func pause() {
        inputEpoch.pause()
        if active { interrupted = true }
    }

    func isPaused() -> Bool { inputEpoch.isPaused }
    func wasInterrupted() -> Bool { interrupted }
}
