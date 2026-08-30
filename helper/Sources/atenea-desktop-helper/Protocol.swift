// The JSON-RPC framing Atenea's own stdio transport expects, and nothing more.
//
// Line-delimited JSON on stdout, one object per line, matching what
// internal/mcpstdio reads. Anything this process wants to say to a human goes
// to stderr: a stray print on stdout is a malformed frame, and the transport
// on the other side would have to guess.
import Foundation

struct RPCError: Error {
    let code: Int
    let message: String
    /// What kind of refusal this is, in one word the far side can sort on.
    ///
    /// It exists because the caller cannot tell them apart from the message,
    /// and getting it wrong is expensive: Atenea's funnel marks a provider
    /// DOWN when a call fails as unavailable, so "that window is not open" --
    /// an answer about the request -- would take screenshots out of service
    /// for everybody until the health went stale.
    var kind: String {
        if code == -32800 { return "canceled" }
        return code == -32000 ? "denied" : "invalid"
    }
    static func invalidParams(_ m: String) -> RPCError { .init(code: -32602, message: m) }
    static func internalError(_ m: String) -> RPCError { .init(code: -32603, message: m) }
    /// Refusals that are the caller's answer rather than a protocol fault. The
    /// transport turns these into a tool result flagged as an error, which is
    /// what lets a model read why something did not work instead of seeing a
    /// dead connection.
    static func denied(_ m: String) -> RPCError { .init(code: -32000, message: m) }
    static func canceled(_ m: String) -> RPCError { .init(code: -32800, message: m) }
}

/// One tool this helper offers, as MCP describes it.
struct Tool: @unchecked Sendable {
    let name: String
    let description: String
    let inputSchema: [String: Any]
    let run: ([String: Any]) async throws -> Any
}

func log(_ message: String) {
    FileHandle.standardError.write(("atenea-desktop-helper: " + message + "\n").data(using: .utf8)!)
}
