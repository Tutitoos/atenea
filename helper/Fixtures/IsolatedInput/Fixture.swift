// Standard AppKit controls plus an instrumented canvas. Event monitors only
// observe: no event forwarding and no acceptsFirstMouse override.
import AppKit
import CoreGraphics

let role = CommandLine.arguments[1]
let directory = URL(fileURLWithPath: CommandLine.arguments[2])
let app = NSApplication.shared
app.setActivationPolicy(.regular)
let menu = NSMenu()
let applicationMenu = NSMenuItem(title: "Atenea fixture", action: nil, keyEquivalent: "")
applicationMenu.submenu = NSMenu(title: "Atenea fixture")
menu.addItem(applicationMenu)
let edit = NSMenuItem(title: "Edit", action: nil, keyEquivalent: "")
let editMenu = NSMenu(title: "Edit")
editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
edit.submenu = editMenu
menu.addItem(edit)
app.mainMenu = menu
let window = NSWindow(contentRect: NSRect(x: 120, y: 100, width: 600, height: 750),
                      styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
window.title = "Atenea synthetic " + role
window.acceptsMouseMovedEvents = true
let editor = NSTextView(frame: NSRect(x: 20, y: 560, width: 560, height: 100))
editor.string = "SYNTHETIC " + role + "\n"
window.contentView!.addSubview(editor)
let secure = NSSecureTextField(frame: NSRect(x: 20, y: 680, width: 250, height: 30))
secure.stringValue = "SYNTHETIC_NOT_A_CREDENTIAL"
window.contentView!.addSubview(secure)
let scroll = NSScrollView(frame: NSRect(x: 20, y: 20, width: 560, height: 240))
scroll.hasVerticalScroller = true
let document = NSTextView(frame: NSRect(x: 0, y: 0, width: 540, height: 2600))
document.string = (1...100).map { "Synthetic row \($0)" }.joined(separator: "\n")
scroll.documentView = document
window.contentView!.addSubview(scroll)
final class Canvas: NSView {
    var path: [[Double]] = []
    var releases: [[Double]] = []
    var moves = 0
    var downs = 0
    override var isFlipped: Bool { true }
    override func draw(_ dirtyRect: NSRect) {
        NSColor.systemTeal.setFill(); bounds.fill()
    }
    override func mouseMoved(with event: NSEvent) { moves += 1 }
    override func mouseDown(with event: NSEvent) { downs += 1; path = [] }
    override func mouseDragged(with event: NSEvent) {
        let point = convert(event.locationInWindow, from: nil)
        path.append([point.x, point.y]); needsDisplay = true
    }
    override func mouseUp(with event: NSEvent) {
        let point = convert(event.locationInWindow, from: nil)
        releases.append([point.x, point.y])
    }
}
let canvas = Canvas(frame: NSRect(x: 20, y: 280, width: 560, height: 160))
canvas.addTrackingArea(NSTrackingArea(rect: canvas.bounds, options: [.mouseMoved, .activeAlways, .inVisibleRect], owner: canvas))
window.contentView!.addSubview(canvas)
let second = NSWindow(contentRect: NSRect(x: 780, y: 100, width: 260, height: 180),
                      styleMask: [.titled, .closable], backing: .buffered, defer: false)
second.title = "Atenea synthetic second"
let otherEditor = NSTextView(frame: NSRect(x: 0, y: 0, width: 260, height: 180))
otherEditor.string = "SECOND WINDOW"
second.contentView = otherEditor
final class Actions: NSObject {
    var count = 0
    @objc func press() { count += 1 }
    @objc func secureFocus() { window.makeFirstResponder(secure) }
    @objc func editorFocus() { window.makeFirstResponder(editor) }
    @objc func secondFocus() { second.makeKeyAndOrderFront(nil); second.makeFirstResponder(otherEditor) }
    @objc func overlap() { second.setFrame(window.frame, display: true); secondFocus() }
    @objc func moveWindow() { window.setFrameOrigin(NSPoint(x: window.frame.minX + 40, y: window.frame.minY)) }
}
let actions = Actions()
var buttons: [String: NSButton] = [:]
for (index, item) in [("increment", #selector(Actions.press)), ("secure", #selector(Actions.secureFocus)),
                      ("editor", #selector(Actions.editorFocus)), ("second", #selector(Actions.secondFocus)),
                      ("overlap", #selector(Actions.overlap)), ("move", #selector(Actions.moveWindow))].enumerated() {
    let button = NSButton(title: item.0, target: actions, action: item.1)
    button.frame = NSRect(x: 20 + index * 94, y: 480, width: 90, height: 32)
    window.contentView!.addSubview(button); buttons[item.0] = button
}
if role == "target" { second.orderFront(nil) }
window.makeKeyAndOrderFront(nil)
window.makeFirstResponder(editor)
var events: [String: Int] = [:]
var markedEvents: [String: Int] = [:]
var markedMouse: [[Double]] = []
var markedMouseNumbers: [Int] = []
var markedMoves: [[String: Any]] = []
var markedKeys: [[UInt64]] = []
var wrongWindowEvents = 0
let marker: Int64 = 0x4154454E4541
let monitor = NSEvent.addLocalMonitorForEvents(matching: [.keyDown, .keyUp, .leftMouseDown, .leftMouseUp, .mouseMoved, .leftMouseDragged, .scrollWheel]) { event in
    events[String(event.type.rawValue), default: 0] += 1
    if event.cgEvent?.getIntegerValueField(.eventSourceUserData) == marker {
        markedEvents[String(event.type.rawValue), default: 0] += 1
        if event.windowNumber != 0 && event.windowNumber != window.windowNumber { wrongWindowEvents += 1 }
        if [.leftMouseDown, .leftMouseDragged, .leftMouseUp].contains(event.type) {
            let p = canvas.convert(event.locationInWindow, from: nil)
            markedMouse.append([Double(event.type.rawValue), p.x, p.y])
            markedMouseNumbers.append(event.eventNumber)
        }
        if event.type == .mouseMoved {
            let point = event.locationInWindow
            let cgPoint = event.cgEvent?.location ?? .zero
            markedMoves.append(["window": event.windowNumber, "has_window": event.window != nil,
                "x": point.x, "y": point.y, "cg_x": cgPoint.x, "cg_y": cgPoint.y,
                "canvas_hit": window.contentView?.hitTest(point) === canvas])
        }
        if [.keyDown, .keyUp, .flagsChanged].contains(event.type) {
            markedKeys.append([UInt64(event.type.rawValue), UInt64(event.keyCode), UInt64(event.modifierFlags.rawValue)])
        }
    }
    return event
}
func snapshot() {
    let height = NSScreen.screens.first!.frame.maxY
    func point(_ view: NSView, dx: CGFloat = 0, dy: CGFloat = 0) -> [String: Double] {
        let p = window.convertPoint(toScreen: view.convert(NSPoint(x: view.bounds.midX + dx, y: view.bounds.midY + dy), to: nil))
        return ["x": p.x, "y": height - p.y]
    }
    let pointer = CGEvent(source: nil)!.location
    var points = buttons.mapValues { point($0) }
    points["canvas"] = point(canvas); points["editorPoint"] = point(editor); points["scroll"] = point(scroll)
    points["dragStart"] = point(canvas, dx: -160); points["dragEnd"] = point(canvas, dx: 160)
    let value: [String: Any] = ["pid": ProcessInfo.processInfo.processIdentifier, "window_id": window.windowNumber,
        "role": role, "text": editor.string, "selection_length": editor.selectedRange().length,
        "other_text": otherEditor.string, "secure_length": secure.stringValue.count,
        "count": actions.count, "events": events, "marked_events": markedEvents, "wrong_window_events": wrongWindowEvents,
        "marked_mouse": markedMouse, "marked_keys": markedKeys,
        "marked_mouse_numbers": markedMouseNumbers, "marked_moves": markedMoves,
        "pointer": ["x": pointer.x, "y": pointer.y], "frontmost": NSWorkspace.shared.frontmostApplication?.processIdentifier ?? 0,
        "points": points, "drag_path": canvas.path, "releases": canvas.releases, "downs": canvas.downs, "moves": canvas.moves,
        "key_window_id": app.keyWindow?.windowNumber ?? 0, "second_window_id": second.windowNumber,
        "second_frame": ["x":second.frame.minX,"y":height-second.frame.maxY,"width":second.frame.width,"height":second.frame.height],
        "scroll_y": scroll.contentView.bounds.origin.y,
        "window_frame": ["x":window.frame.minX,"y":height-window.frame.maxY,"width":window.frame.width,"height":window.frame.height]]
    let data = try! JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
    let file = directory.appendingPathComponent(role + ".json")
    try! data.write(to: file, options: .atomic)
    try! FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: file.path)
}
let timer = Timer.scheduledTimer(withTimeInterval: 0.01, repeats: true) { _ in snapshot() }
snapshot()
app.run()
