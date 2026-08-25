// An MCP server over stdin/stdout, small enough to read in one sitting.
//
// It answers `initialize`, `tools/list` and `tools/call` and refuses anything
// else by name. There is no session state and no lifecycle of its own: the
// process is spawned, supervised and stopped by internal/supervisor, and a
// second opinion about when it should exit is how two owners end up
// disagreeing over whether a child is alive.
import Foundation

let version = "0.1.0"

let tools: [Tool] = [
    Tool(
        name: "health",
        description: "Report whether this machine can be driven: permissions and graphical session.",
        inputSchema: ["type": "object", "properties": [:] as [String: Any]],
        run: { _ in
            var out: [String: Any] = ["version": version, "pid": ProcessInfo.processInfo.processIdentifier]
            out.merge(Permissions.current().asDictionary()) { a, _ in a }
            return out
        }
    ),
    Tool(
        name: "list_apps",
        description: "List the applications with a user interface that are running right now.",
        inputSchema: ["type": "object", "properties": [:] as [String: Any]],
        run: { _ in ["apps": Apps.list()] }
    ),
]

// MARK: - the loop

/// Writes one frame. Everything that leaves this process on stdout goes
/// through here, so there is exactly one place that can put a malformed line
/// on the wire.
func emit(_ object: [String: Any]) {
    guard let data = try? JSONSerialization.data(withJSONObject: object, options: [.withoutEscapingSlashes]) else {
        log("could not serialize a reply; dropping it")
        return
    }
    FileHandle.standardOutput.write(data)
    FileHandle.standardOutput.write("\n".data(using: .utf8)!)
}

func reply(id: Any, result: [String: Any]) {
    emit(["jsonrpc": "2.0", "id": id, "result": result])
}

func replyError(id: Any, _ err: RPCError) {
    emit(["jsonrpc": "2.0", "id": id, "error": ["code": err.code, "message": err.message]])
}

/// An MCP tool result. A refusal travels as `isError` on a normal result
/// rather than as a JSON-RPC error, because it is an answer the caller asked
/// for and can read, not a fault in the conversation.
func toolResult(_ payload: Any, isError: Bool = false) -> [String: Any] {
    let text: String
    if let data = try? JSONSerialization.data(withJSONObject: payload, options: [.sortedKeys, .withoutEscapingSlashes]),
       let s = String(data: data, encoding: .utf8) {
        text = s
    } else {
        text = "\(payload)"
    }
    return ["content": [["type": "text", "text": text]], "isError": isError]
}

func handle(_ message: [String: Any]) {
    let method = message["method"] as? String ?? ""
    let id = message["id"]

    // A notification carries no id and expects no answer. Replying to one puts
    // a frame on the wire that nobody is waiting for.
    guard let id else {
        if method != "notifications/initialized" { log("ignoring notification \(method)") }
        return
    }

    switch method {
    case "initialize":
        reply(id: id, result: [
            // Echoed rather than asserted: the client picks the revision, and
            // this helper speaks a subset small enough that every revision it
            // might name works the same way.
            "protocolVersion": (message["params"] as? [String: Any])?["protocolVersion"] as? String ?? "2025-06-18",
            "capabilities": ["tools": [:] as [String: Any]],
            "serverInfo": ["name": "atenea-desktop-helper", "version": version],
        ])
    case "tools/list":
        reply(id: id, result: ["tools": tools.map {
            ["name": $0.name, "description": $0.description, "inputSchema": $0.inputSchema]
        }])
    case "tools/call":
        let params = message["params"] as? [String: Any] ?? [:]
        let name = params["name"] as? String ?? ""
        let args = params["arguments"] as? [String: Any] ?? [:]
        guard let tool = tools.first(where: { $0.name == name }) else {
            replyError(id: id, .invalidParams("no such tool: \(name)"))
            return
        }
        do {
            reply(id: id, result: toolResult(try tool.run(args)))
        } catch let err as RPCError {
            reply(id: id, result: toolResult(["error": err.message], isError: true))
        } catch {
            reply(id: id, result: toolResult(["error": "\(error)"], isError: true))
        }
    default:
        // Named rather than ignored. Silence here is what hangs a caller
        // waiting on an answer that is never coming.
        replyError(id: id, .init(code: -32601, message: "method not supported: \(method)"))
    }
}

while let line = readLine(strippingNewline: true) {
    if line.isEmpty { continue }
    guard let data = line.data(using: .utf8),
          let message = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
        log("skipping a line that is not a JSON object")
        continue
    }
    handle(message)
}
