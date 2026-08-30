import Foundation

/// Serializes one desktop action and gives the passive input monitor a safe
/// cancellation point between input events.
actor ActionGate {
    static let shared = ActionGate()
    private var active = false
    private var interrupted = false
    private var paused = false
    private var visualAction = false

    func begin(visualFeedback: Bool = true) throws {
        guard !paused else { throw RPCError.denied("desktop actions are paused; press Resume in the Atenea preview") }
        guard !active else { throw RPCError.denied("another desktop action is already in progress") }
        active = true
        visualAction = visualFeedback
        interrupted = false
    }

    func checkpoint() throws {
        if interrupted {
            throw RPCError.canceled("interrupted by human input; the action was not retried")
        }
    }

    func finish() {
        active = false
        visualAction = false
        interrupted = false
    }

    /// Marks the current action as interrupted. The return value tells
    /// the event monitor whether there was an action to show as paused; idle
    /// human activity must not leave a newly-started helper looking paused.
    @discardableResult
    func interrupt() -> Bool {
        guard active else { return false }
        interrupted = true
        if visualAction {
            paused = true
            return true
        }
        // Even with the presentation disabled, an observed human event must
        // stop the in-flight input sequence. There is simply no panel to mark
        // as paused in this mode.
        return false
    }

    func resume() {
        paused = false
        interrupted = false
    }

    func isPaused() -> Bool { paused }
    func wasInterrupted() -> Bool { interrupted }
}
