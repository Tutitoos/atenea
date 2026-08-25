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
    static func invalidParams(_ m: String) -> RPCError { .init(code: -32602, message: m) }
    static func internalError(_ m: String) -> RPCError { .init(code: -32603, message: m) }
    /// Refusals that are the caller's answer rather than a protocol fault. The
    /// transport turns these into a tool result flagged as an error, which is
    /// what lets a model read why something did not work instead of seeing a
    /// dead connection.
    static func denied(_ m: String) -> RPCError { .init(code: -32000, message: m) }
}

/// One tool this helper offers, as MCP describes it.
struct Tool {
    let name: String
    let description: String
    let inputSchema: [String: Any]
    let run: ([String: Any]) throws -> Any
}

func log(_ message: String) {
    FileHandle.standardError.write(("atenea-desktop-helper: " + message + "\n").data(using: .utf8)!)
}
