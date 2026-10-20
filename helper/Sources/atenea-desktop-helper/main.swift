// An MCP server over stdin/stdout, small enough to read in one sitting.
//
// It answers `initialize`, `tools/list` and `tools/call` and refuses anything
// else by name. There is no session state and no lifecycle of its own: the
// process is spawned, supervised and stopped by internal/supervisor, and a
// second opinion about when it should exit is how two owners end up
// disagreeing over whether a child is alive.
import Foundation
import AppKit

let version = "0.1.0"

// Connects this process to the window server before anything asks it about
// displays.
//
// A command-line tool has no connection by default, and ScreenCaptureKit does
// not make one: `SCContentFilter(desktopIndependentWindow:)` asserts inside
// SkyLight's SLSGetDisplaysWithRect and aborts the process. Measured, twice,
// with two different crash reports before the cause was clear -- the window
// list came back fine, so it looked like the capture was at fault rather than
// the connection underneath it.
//
// Touching the shared application is what establishes it -- the Swift-visible
// equivalent of AppKit's NSApplicationLoad. It puts no window on screen, and
// LSUIElement keeps it out of the Dock.
_ = NSApplication.shared
NSApplication.shared.setActivationPolicy(.accessory)
// Swift 5.10 does not infer main-actor isolation for synchronous top-level code.
MainActor.assumeIsolated { InputMonitor.shared.start() }

struct TargetArguments {
    let pid: pid_t
    let bundleID: String
    let appName: String
    let visualFeedback: Bool
}

func targetArguments(_ args: [String: Any]) throws -> TargetArguments {
    guard let pid = args["pid"] as? Int,
          let bundle = args["bundle_id"] as? String,
          let app = args["app"] as? String else {
        throw RPCError.invalidParams("pid, bundle_id and app are required")
    }
    return TargetArguments(pid: pid_t(pid), bundleID: bundle, appName: app,
                           visualFeedback: args["visual_feedback"] as? Bool ?? true)
}

@MainActor
func checkedPoint(_ args: [String: Any]) async throws -> (CGPoint, CaptureFrame, TargetArguments) {
    let target = try targetArguments(args)
    try await ensureInputMonitor(target.visualFeedback)
    guard let x = args["x"] as? Double, let y = args["y"] as? Double else {
        throw RPCError.invalidParams("x and y are required")
    }
    guard let frameID = args["frame_id"] as? String, !frameID.isEmpty else {
        throw RPCError.invalidParams("frame_id is required")
    }
    let (point, frame) = try await Capture.globalPoint(pid: target.pid, bundleID: target.bundleID,
                                                       appName: target.appName, frameID: frameID,
                                                       x: x, y: y)
    return (point, frame, target)
}

func actionReceipt(_ frame: CaptureFrame, input: InputReceipt) -> [String: Any] {
    var receipt: [String: Any] = [
        "action_sent": true,
        "frame_id": frame.id,
        "window_id": Int(frame.target.windowID),
        "dominant_display_id": Int(frame.target.dominantDisplayID),
        "geometry_generation": frame.target.geometryGeneration,
    ]
    receipt.merge(input.dictionary) { _, new in new }
    return receipt
}

func ensureInputMonitor(_ visualFeedback: Bool) async throws {
    guard !InputMonitor.shared.active else { return }
    throw RPCError.denied(InputMonitor.shared.failure ??
        "human input monitoring is unavailable; no input sent")
}

// Immutable once built and read only from the serial loop at the bottom, which
// is what makes this safe rather than merely quiet. The annotation says the
// compiler cannot see that and this file can.
let tools: [Tool] = [
    Tool(
        name: "health",
        description: "Report whether this machine can be driven: permissions and graphical session.",
        inputSchema: ["type": "object", "properties": [:] as [String: Any]],
        run: { _ in
            var out: [String: Any] = ["version": version, "pid": ProcessInfo.processInfo.processIdentifier]
            out.merge(Permissions.current().asDictionary()) { a, _ in a }
            let visual = await MainActor.run {
                (VisualFeedbackController.shared.enabled, VisualFeedbackController.shared.healthState,
                 NSApp != nil && !NSScreen.screens.isEmpty)
            }
            out["visual_feedback_supported"] = visual.2
            out["visual_feedback_enabled"] = visual.0
            out["input_monitor_active"] = InputMonitor.shared.active
            out["input_monitor_error"] = InputMonitor.shared.failure ?? ""
            out["isolated_input"] = true
            let virtual = await MainActor.run { (Input.virtualPointer, Input.virtualPointerPID) }
            if let pointer = virtual.0, let pid = virtual.1 {
                out["virtual_pointer_x"] = pointer.x
                out["virtual_pointer_y"] = pointer.y
                out["virtual_pointer_pid"] = Int(pid)
            }
            out["overlay_state"] = visual.1
            out["graphical_session"] = await MainActor.run { Permissions.interactiveSession }
            return out
        }
    ),
    Tool(
        name: "request_access",
        description: "Ask macOS to show the Accessibility prompt for this process.",
        inputSchema: ["type": "object", "properties": [:] as [String: Any]],
        run: { _ in
            let answer = Permissions.request()
            await MainActor.run { InputMonitor.shared.start() }
            return answer
        }
    ),
    Tool(
        name: "list_apps",
        description: "List the applications with a user interface that are running right now.",
        inputSchema: ["type": "object", "properties": [:] as [String: Any]],
        run: { _ in ["apps": Apps.list()] }
    ),
    Tool(
        name: "open",
        description: "Open an installed application by bundle identifier and verify it is frontmost.",
        inputSchema: ["type": "object", "required": ["bundle_id"],
                      "properties": ["bundle_id": ["type": "string"],
                                     "visual_feedback": ["type": "boolean"]] as [String: Any]],
        run: { args in
            guard let bundle = args["bundle_id"] as? String else {
                throw RPCError.invalidParams("bundle_id is required")
            }
            let visual = args["visual_feedback"] as? Bool ?? true
            try await ensureInputMonitor(visual)
            await MainActor.run { VisualFeedbackController.shared.setEnabled(visual) }
            return try await Apps.open(bundleID: bundle, visualFeedback: visual)
        }
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
                "visual_feedback": ["type": "boolean"],
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
            if args["visual_feedback"] as? Bool ?? true,
               let target = try? await Capture.currentTarget(pid: pid_t(pid), bundleID: bundle, appName: app) {
                await MainActor.run {
                    VisualFeedbackController.shared.setEnabled(true)
                    VisualFeedbackController.shared.show(target: target, state: .observing)
                }
            }
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
            "required": ["pid", "bundle_id", "app"],
            "properties": ["pid": ["type": "integer"],
                           "bundle_id": ["type": "string"], "app": ["type": "string"],
                           "visual_feedback": ["type": "boolean"]] as [String: Any],
        ],
        run: { args in
            guard let pid = args["pid"] as? Int,
                  let bundle = args["bundle_id"] as? String,
                  let app = args["app"] as? String else {
                throw RPCError.invalidParams("pid, bundle_id and app are required")
            }
            await MainActor.run {
                VisualFeedbackController.shared.setEnabled(args["visual_feedback"] as? Bool ?? true)
            }
            let shot = try await Capture.window(pid: pid_t(pid), bundleID: bundle, appName: app)
            await MainActor.run {
                VisualFeedbackController.shared.show(target: shot.target, imageData: shot.png, state: .observing)
            }
            return [
                "png_base64": shot.png.base64EncodedString(),
                "width": shot.width,
                "height": shot.height,
                "scale": shot.scale,
                "bytes": shot.png.count,
                "frame_id": shot.frameID,
                "window_id": Int(shot.target.windowID),
                "window_origin_x": shot.target.frame.origin.x,
                "window_origin_y": shot.target.frame.origin.y,
                "window_width": shot.target.frame.width,
                "window_height": shot.target.frame.height,
                "scale_x": shot.target.scale.width,
                "scale_y": shot.target.scale.height,
                "dominant_display_id": Int(shot.target.dominantDisplayID),
                "intersecting_display_ids": shot.target.intersectingDisplays.map { String($0.id) },
                "intersecting_displays": shot.target.intersectingDisplays.map {
                    ["id": Int($0.id), "origin_x": $0.frame.origin.x, "origin_y": $0.frame.origin.y,
                     "width": $0.frame.width, "height": $0.frame.height, "scale": $0.scale]
                },
                "geometry_generation": shot.target.geometryGeneration,
                "coordinate_space": "screenshot_pixels_top_left",
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
        inputSchema: ["type": "object", "required": ["pid", "bundle_id", "app", "x", "y", "frame_id"],
                      "properties": ["pid": ["type": "integer"], "bundle_id": ["type": "string"],
                                     "app": ["type": "string"], "x": ["type": "number"], "y": ["type": "number"],
                                     "clicks": ["type": "integer"], "frame_id": ["type": "string"],
                                     "visual_feedback": ["type": "boolean"]] as [String: Any]],
        run: { args in
            let (point, frame, target) = try await checkedPoint(args)
            let clicks = args["clicks"] as? Int ?? 1
            guard (1...2).contains(clicks) else { throw RPCError.invalidParams("clicks must be one or two") }
            let input = try await Input.perform(frame: frame, visualFeedback: target.visualFeedback, state: .clicking, point: point) {
                try await $0.click(at: point, clicks: clicks)
            }
            return actionReceipt(frame, input: input)
        }
    ),
    Tool(
        name: "move",
        description: "Move the pointer without clicking.",
        inputSchema: ["type": "object", "required": ["pid", "bundle_id", "app", "x", "y", "frame_id"],
                      "properties": ["pid": ["type": "integer"], "bundle_id": ["type": "string"],
                                     "app": ["type": "string"], "x": ["type": "number"], "y": ["type": "number"],
                                     "frame_id": ["type": "string"], "visual_feedback": ["type": "boolean"]] as [String: Any]],
        run: { args in
            let (point, frame, target) = try await checkedPoint(args)
            let input = try await Input.perform(frame: frame, visualFeedback: target.visualFeedback, state: .moving, point: point) {
                try await $0.move(to: point)
            }
            return actionReceipt(frame, input: input)
        }
    ),
    Tool(
        name: "drag",
        description: "Press at one point, drag to another, release.",
        inputSchema: ["type": "object", "required": ["pid", "bundle_id", "app", "from_x", "from_y", "to_x", "to_y", "frame_id"],
                      "properties": ["pid": ["type": "integer"], "bundle_id": ["type": "string"], "app": ["type": "string"],
                                     "from_x": ["type": "number"], "from_y": ["type": "number"],
                                     "to_x": ["type": "number"], "to_y": ["type": "number"], "frame_id": ["type": "string"],
                                     "visual_feedback": ["type": "boolean"]] as [String: Any]],
        run: { args in
            let target = try targetArguments(args)
            guard let fx = args["from_x"] as? Double, let fy = args["from_y"] as? Double,
                  let tx = args["to_x"] as? Double, let ty = args["to_y"] as? Double,
                  let frameID = args["frame_id"] as? String else { throw RPCError.invalidParams("drag points and frame_id are required") }
            let (start, frame) = try await Capture.globalPoint(pid: target.pid, bundleID: target.bundleID,
                appName: target.appName, frameID: frameID, x: fx, y: fy)
            let end = try frame.target.globalPoint(forImagePoint: CGPoint(x: tx, y: ty))
            let input = try await Input.perform(frame: frame, visualFeedback: target.visualFeedback, state: .dragging, point: start) {
                try await $0.drag(from: start, to: end, visualFeedback: target.visualFeedback)
            }
            return actionReceipt(frame, input: input)
        }
    ),
    Tool(
        name: "scroll",
        description: "Scroll at a point.",
        inputSchema: ["type": "object", "required": ["pid", "bundle_id", "app", "x", "y", "frame_id"],
                      "properties": ["pid": ["type": "integer"], "bundle_id": ["type": "string"], "app": ["type": "string"],
                                     "x": ["type": "number"], "y": ["type": "number"],
                                     "dx": ["type": "integer"], "dy": ["type": "integer"], "frame_id": ["type": "string"],
                                     "visual_feedback": ["type": "boolean"]] as [String: Any]],
        run: { args in
            let (point, frame, target) = try await checkedPoint(args)
            let dx = args["dx"] as? Int ?? 0
            let dy = args["dy"] as? Int ?? 0
            let input = try await Input.perform(frame: frame, visualFeedback: target.visualFeedback, state: .scrolling, point: point) {
                try await $0.scroll(at: point, dx: dx, dy: dy)
            }
            return actionReceipt(frame, input: input)
        }
    ),
    Tool(
        name: "type",
        description: "Type literal text into the validated target window using private PID-directed input.",
        inputSchema: ["type": "object", "required": ["pid", "bundle_id", "app", "text", "frame_id"],
                      "properties": ["pid": ["type": "integer"], "bundle_id": ["type": "string"],
                                     "app": ["type": "string"], "text": ["type": "string"], "frame_id": ["type": "string"],
                                     "visual_feedback": ["type": "boolean"]] as [String: Any]],
        run: { args in
            let target = try targetArguments(args)
            guard let value = args["text"] as? String, let frameID = args["frame_id"] as? String else {
                throw RPCError.invalidParams("text and frame_id are required")
            }
            let frame = try await Capture.validatedFrame(pid: target.pid, bundleID: target.bundleID, appName: target.appName, frameID: frameID)
            let input = try await Input.perform(frame: frame, visualFeedback: target.visualFeedback, state: .typing) {
                try await $0.type(value)
            }
            return actionReceipt(frame, input: input)
        }
    ),
    Tool(
        name: "key",
        description: "Press one key, with optional modifiers.",
        inputSchema: ["type": "object", "required": ["pid", "bundle_id", "app", "key", "frame_id"],
                      "properties": ["pid": ["type": "integer"], "bundle_id": ["type": "string"],
                                     "app": ["type": "string"], "key": ["type": "string"], "frame_id": ["type": "string"],
                                     "modifiers": ["type": "array", "items": ["type": "string"]],
                                     "visual_feedback": ["type": "boolean"]] as [String: Any]],
        run: { args in
            let target = try targetArguments(args)
            guard let value = args["key"] as? String, let frameID = args["frame_id"] as? String else {
                throw RPCError.invalidParams("key and frame_id are required")
            }
            let modifiers = args["modifiers"] as? [String] ?? []
            let frame = try await Capture.validatedFrame(pid: target.pid, bundleID: target.bundleID, appName: target.appName, frameID: frameID)
            let input = try await Input.perform(frame: frame, visualFeedback: target.visualFeedback, state: .typing) {
                try await $0.key(value, modifiers: modifiers)
            }
            return actionReceipt(frame, input: input)
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

func handle(_ message: [String: Any]) async {
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
            let result = try await RPCRequestContext.$id.withValue(RPCRequestID(id)) {
                try RequestCancellation.shared.checkpoint()
                return try await tool.run(args)
            }
            reply(id: id, result: toolResult(result))
        } catch let err as RPCError {
            reply(id: id, result: toolResult(["error": err.message, "kind": err.kind], isError: true))
        } catch {
            // Unlabeled on purpose. An error this file did not raise is one
            // nobody has classified, and calling it a refusal would be a
            // guess -- the far side treats an unlabeled failure as the
            // provider's own, which is the safe reading.
            reply(id: id, result: toolResult(["error": "\(error)"], isError: true))
        }
    default:
        // Named rather than ignored. Silence here is what hangs a caller
        // waiting on an answer that is never coming.
        replyError(id: id, .init(code: -32601, message: "method not supported: \(method)"))
    }
}

// Execute input sequences serially. The separate reader records cancellation
// immediately, including while an asynchronous action is awaiting its next step.
let incoming = AsyncStream<String> { continuation in
    DispatchQueue.global(qos: .userInitiated).async {
        while let line = readLine(strippingNewline: true) {
            if let data = line.data(using: .utf8),
               let message = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] {
                if isCancellation(message) {
                    let params = message["params"] as? [String: Any] ?? [:]
                    if let id = RPCRequestID(params["requestId"] ?? params["id"]) {
                        RequestCancellation.shared.cancel(id)
                    }
                    continue
                }
                if let id = RPCRequestID(message["id"]) { RequestCancellation.shared.register(id) }
            }
            if !line.isEmpty { continuation.yield(line) }
        }
        Task { await ActionGate.shared.interrupt() }
        continuation.finish()
    }
}

Task {
    for await line in incoming {
        guard let data = line.data(using: .utf8),
              let message = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any] else {
            log("skipping a line that is not a JSON object")
            continue
        }
        let id = RPCRequestID(message["id"])
        await handle(message)
        if let id { RequestCancellation.shared.finish(id) }
    }
    await MainActor.run {
        InputMonitor.shared.stop()
        VisualFeedbackController.shared.hide()
        NSApplication.shared.terminate(nil)
    }
}

NSApplication.shared.run()
