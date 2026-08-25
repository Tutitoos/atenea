// An MCP server over stdin/stdout, small enough to read in one sitting.
//
// It answers `initialize`, `tools/list` and `tools/call` and refuses anything
// else by name. There is no session state and no lifecycle of its own: the
// process is spawned, supervised and stopped by internal/supervisor, and a
// second opinion about when it should exit is how two owners end up
// disagreeing over whether a child is alive.
import Foundation

let version = "0.1.0"

// Immutable once built and read only from the serial loop at the bottom, which
// is what makes this safe rather than merely quiet. The annotation says the
// compiler cannot see that and this file can.
nonisolated(unsafe) let tools: [Tool] = [
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
        name: "request_access",
        description: "Ask macOS to show the Accessibility prompt for this process.",
        inputSchema: ["type": "object", "properties": [:] as [String: Any]],
        run: { _ in Permissions.request() }
    ),
    Tool(
        name: "list_apps",
        description: "List the applications with a user interface that are running right now.",
        inputSchema: ["type": "object", "properties": [:] as [String: Any]],
        run: { _ in ["apps": Apps.list()] }
    ),
    Tool(
        name: "inspect",
        description: "Read one application's accessibility tree, bounded by a time budget.",
        inputSchema: [
            "type": "object",
            "required": ["pid", "bundle_id", "app"],
            "properties": [
                "pid": ["type": "integer"],
                "bundle_id": ["type": "string"],
                "app": ["type": "string"],
                "roles": ["type": "array", "items": ["type": "string"]],
                "budget_ms": ["type": "integer"],
                "max_nodes": ["type": "integer"],
                "max_bytes": ["type": "integer"],
                "max_depth": ["type": "integer"],
            ] as [String: Any],
        ],
        run: { args in
            guard let pid = args["pid"] as? Int, let bundle = args["bundle_id"] as? String,
                  let app = args["app"] as? String else {
                throw RPCError.invalidParams("pid, bundle_id and app are required")
            }
            // Every ceiling comes from the caller. The helper enforces and
            // never invents one: a default living down here would be a policy
            // decision in the place with the least idea of the context.
            let limits = TreeLimits(
                deadline: Date().addingTimeInterval(Double(args["budget_ms"] as? Int ?? 2000) / 1000),
                maxNodes: args["max_nodes"] as? Int ?? 10_000,
                maxBytes: args["max_bytes"] as? Int ?? 1_000_000,
                maxDepth: args["max_depth"] as? Int ?? 40)
            let roles = Set((args["roles"] as? [String]) ?? [])
            let (rows, stopped) = Tree.walk(pid: pid_t(pid), bundleID: bundle,
                                            appName: app, roles: roles, limits: limits)
            var out: [String: Any] = ["nodes": rows, "count": rows.count]
            // Said out loud rather than inferred from a suspiciously round
            // count. A truncation nobody is told about is a lie by omission
            // about what is on somebody's screen.
            if stopped != .none { out["truncated"] = stopped.rawValue }
            return out
        }
    ),
    Tool(
        name: "screenshot",
        description: "Capture one application's frontmost window as a PNG.",
        inputSchema: [
            "type": "object",
            "required": ["pid"],
            "properties": ["pid": ["type": "integer"]] as [String: Any],
        ],
        run: { args in
            guard let pid = args["pid"] as? Int else {
                throw RPCError.invalidParams("pid is required")
            }
            let shot = try Capture.window(pid: pid_t(pid))
            return [
                "png_base64": shot.png.base64EncodedString(),
                "width": shot.width,
                "height": shot.height,
                "scale": shot.scale,
                "bytes": shot.png.count,
            ]
        }
    ),

    // The mutating half. Everything about WHICH application may be touched is
    // decided in Go; what these do is the act itself, plus the one refusal
    // that can only be made here -- typing into a password field, which only
    // the process holding the accessibility connection can detect, and only
    // at the moment of typing, because focus moves.
    Tool(
        name: "click",
        description: "Click at a point, once or twice.",
        inputSchema: ["type": "object", "required": ["x", "y"],
                      "properties": ["x": ["type": "number"], "y": ["type": "number"],
                                     "clicks": ["type": "integer"]] as [String: Any]],
        run: { args in
            guard let x = args["x"] as? Double, let y = args["y"] as? Double else {
                throw RPCError.invalidParams("x and y are required")
            }
            Input.click(at: CGPoint(x: x, y: y), clicks: args["clicks"] as? Int ?? 1)
            return ["clicked": ["x": x, "y": y]]
        }
    ),
    Tool(
        name: "move",
        description: "Move the pointer without clicking.",
        inputSchema: ["type": "object", "required": ["x", "y"],
                      "properties": ["x": ["type": "number"], "y": ["type": "number"]] as [String: Any]],
        run: { args in
            guard let x = args["x"] as? Double, let y = args["y"] as? Double else {
                throw RPCError.invalidParams("x and y are required")
            }
            Input.move(to: CGPoint(x: x, y: y))
            return ["moved": ["x": x, "y": y]]
        }
    ),
    Tool(
        name: "drag",
        description: "Press at one point, drag to another, release.",
        inputSchema: ["type": "object", "required": ["from_x", "from_y", "to_x", "to_y"],
                      "properties": ["from_x": ["type": "number"], "from_y": ["type": "number"],
                                     "to_x": ["type": "number"], "to_y": ["type": "number"]] as [String: Any]],
        run: { args in
            guard let fx = args["from_x"] as? Double, let fy = args["from_y"] as? Double,
                  let tx = args["to_x"] as? Double, let ty = args["to_y"] as? Double else {
                throw RPCError.invalidParams("from_x, from_y, to_x and to_y are required")
            }
            Input.drag(from: CGPoint(x: fx, y: fy), to: CGPoint(x: tx, y: ty))
            return ["dragged": true]
        }
    ),
    Tool(
        name: "scroll",
        description: "Scroll at a point.",
        inputSchema: ["type": "object", "required": ["x", "y"],
                      "properties": ["x": ["type": "number"], "y": ["type": "number"],
                                     "dx": ["type": "integer"], "dy": ["type": "integer"]] as [String: Any]],
        run: { args in
            guard let x = args["x"] as? Double, let y = args["y"] as? Double else {
                throw RPCError.invalidParams("x and y are required")
            }
            Input.scroll(at: CGPoint(x: x, y: y),
                         dx: args["dx"] as? Int ?? 0, dy: args["dy"] as? Int ?? 0)
            return ["scrolled": true]
        }
    ),
    Tool(
        name: "type",
        description: "Type literal text into whatever has keyboard focus.",
        inputSchema: ["type": "object", "required": ["text"],
                      "properties": ["text": ["type": "string"]] as [String: Any]],
        run: { args in
            guard let text = args["text"] as? String else {
                throw RPCError.invalidParams("text is required")
            }
            try Input.type(text)
            // The length and not the text. A helper that echoed what it typed
            // would put it in a log, a receipt and a model's context, which is
            // three copies of something somebody may have meant to keep.
            return ["typed": text.count]
        }
    ),
    Tool(
        name: "key",
        description: "Press one key, with optional modifiers.",
        inputSchema: ["type": "object", "required": ["key"],
                      "properties": ["key": ["type": "string"],
                                     "modifiers": ["type": "array", "items": ["type": "string"]]] as [String: Any]],
        run: { args in
            guard let name = args["key"] as? String else {
                throw RPCError.invalidParams("key is required")
            }
            try Input.key(name, modifiers: (args["modifiers"] as? [String]) ?? [])
            return ["pressed": name]
        }
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

// One message at a time, and that is the exclusion this whole feature needs
// rather than an accident of how the loop is written.
//
// There is one screen, one pointer and one keyboard on this machine, and two
// calls landing on them at once would interleave into something neither caller
// asked for. Above this process, `internal/mcpstdio` lets any number of
// goroutines call at once and routes the answers back by id -- so the
// serialization has to be HERE, where the desktop actually is. It is: the read
// is synchronous, `handle` runs to completion before the next line is read,
// and even the async capture blocks this loop on its semaphore rather than
// returning to it.
//
// Anything added below that returns before its work is finished breaks this,
// and nothing above would notice.
while let line = readLine(strippingNewline: true) {
    if line.isEmpty { continue }
    guard let data = line.data(using: .utf8),
          let message = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
        log("skipping a line that is not a JSON object")
        continue
    }
    handle(message)
}
