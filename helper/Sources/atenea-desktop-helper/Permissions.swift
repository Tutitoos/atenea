// What macOS will and will not let this process do, reported without
// embellishment.
//
// The important caution lives here rather than in a comment further away: a
// `true` from either of these calls does NOT mean somebody granted it to
// Atenea. macOS attributes a TCC grant to the responsible ancestor, so a
// process launched from a terminal that holds Accessibility reports holding it
// too, whatever its own identifier and signature say. Measured on macOS 26.6
// with a signed binary whose identifier was never authorized: it reported full
// access.
//
// So this type answers "may I, right now", which is a real and useful
// question, and deliberately does not answer "was this granted to Atenea".
// Only the Go side can approach that one, by knowing whether launchd started
// it -- and `responsible` below is where the two halves are joined.
import Foundation
import ApplicationServices
import CoreGraphics

struct Permissions {
    let accessibility: Bool
    let screenRecording: Bool

    /// Reads current state without prompting. The prompting variant of the
    /// accessibility check puts a dialog on the user's screen, which is not
    /// something a health probe may do on its own: a probe that interrupts is
    /// a probe nobody runs.
    static func current() -> Permissions {
        Permissions(
            accessibility: AXIsProcessTrusted(),
            screenRecording: CGPreflightScreenCaptureAccess()
        )
    }

    /// A distinct sentence per failure, because "denied" alone sends people to
    /// the wrong settings pane. The empty string means nothing is missing.
    func missing() -> String {
        switch (accessibility, screenRecording) {
        case (true, true):   return ""
        case (false, true):  return "Accessibility is not granted: System Settings > Privacy & Security > Accessibility"
        case (true, false):  return "Screen Recording is not granted: System Settings > Privacy & Security > Screen Recording"
        case (false, false): return "neither Accessibility nor Screen Recording is granted: System Settings > Privacy & Security"
        }
    }

    /// Asks macOS to show the Accessibility prompt, and reports what changed.
    ///
    /// Separate from `current()` because this one interrupts somebody. A health
    /// probe that put a dialog on the screen is a probe nobody would run, and
    /// the state it reported would then depend on whether anybody was looking.
    ///
    /// It also only does half the job. macOS shows this prompt once per process
    /// identity and then never again, and Screen Recording has no prompting API
    /// at all -- so the answer carries the manual route, because that is what
    /// somebody needs the second time and there is no second prompt to tell
    /// them.
    static func request() -> [String: Any] {
        let before = current()
        // The key's own string rather than the SDK's symbol for it. macOS
        // declares kAXTrustedCheckOptionPrompt as a global `var`, which Swift
        // 6 refuses to read from a nonisolated context -- and no local copy
        // helps, because reading it is the problem. The value is documented
        // and fixed; only its declaration is mutable.
        let options = ["AXTrustedCheckOptionPrompt": true]
        let granted = AXIsProcessTrustedWithOptions(options as CFDictionary)
        return [
            "accessibility_before": before.accessibility,
            "accessibility_now": granted,
            "screen_recording": before.screenRecording,
            "note": "macOS shows this prompt once per process identity. If no dialog appeared, "
                + "add the binary by hand in System Settings > Privacy & Security > "
                + "Accessibility. Screen Recording has no prompt and is always added by hand.",
        ]
    }

    func asDictionary() -> [String: Any] {
        [
            "accessibility": accessibility,
            "screen_recording": screenRecording,
            "missing": missing(),
            // Said out loud in every health answer so a reader of the JSON is
            // not left to infer it: what the two booleans above report may
            // belong to whoever launched this process.
            "attribution_note": "TCC attributes a grant to the responsible ancestor; a true here may belong to the launching process rather than to Atenea",
        ]
    }
}
