import Foundation
import AppKit

enum VisualState: String {
    case hidden
    case observing = "Observing"
    case acting = "Acting"
    case moving = "Moving"
    case clicking = "Clicking"
    case dragging = "Dragging"
    case scrolling = "Scrolling"
    case typing = "Typing"
    case pausedByUser = "Paused"
    case idleBlurred
    case suppressedByUser
    case unavailable = "Window unavailable"
}

@MainActor
final class VisualFeedbackController: NSObject {
    static let shared = VisualFeedbackController()

    private var overlay: NSPanel?
    private var miniature: NSPanel?
    private var preview: NSImageView?
    private var miniatureCursor: MiniatureCursorView?
    private var stateLabel: NSTextField?
    private var resumeButton: NSButton?
    private var cursor = CursorView()
    private var blurTimer: Timer?
    private var closeTimer: Timer?
    private var previewTask: Task<Void, Never>?
    private var followTimer: Timer?
    private var suppressionTimer: Timer?
    private var clickPulseTimer: Timer?
    private(set) var clickPulseProgress: CGFloat = 1
    private(set) var state: VisualState = .hidden
    private(set) var enabled = true
    private(set) var suppressed = false
    private var target: WindowTarget?

    private override init() {
        super.init()
        NotificationCenter.default.addObserver(self,
            selector: #selector(accessibilityChanged),
            name: NSWorkspace.accessibilityDisplayOptionsDidChangeNotification,
            object: nil)
        NotificationCenter.default.addObserver(self, selector: #selector(sessionUnavailable),
            name: NSWorkspace.willSleepNotification, object: nil)
        NotificationCenter.default.addObserver(self, selector: #selector(sessionUnavailable),
            name: NSWorkspace.sessionDidResignActiveNotification, object: nil)
    }

    func setEnabled(_ value: Bool) {
        enabled = value
        if !value { hide() }
    }

    func show(target: WindowTarget, imageData: Data? = nil, state: VisualState = .observing) {
        if suppressed, self.target?.bundleID != target.bundleID {
            suppressed = false
            suppressionTimer?.invalidate(); suppressionTimer = nil
        }
        guard enabled, !suppressed else { return }
        self.target = target
        ensureOverlay()
        ensureMiniature()
        overlay?.setFrame(appKitFrame(target.frame), display: true)
        overlay?.orderFrontRegardless()
        miniature?.orderFrontRegardless()
        self.state = state
        stateLabel?.stringValue = "\(target.appName) • \(state.rawValue)"
        if let imageData, let image = NSImage(data: imageData) {
            preview?.image = image
            preview?.isHidden = false
        }
        startPreviewStream()
        blurTimer?.invalidate()
        closeTimer?.invalidate()
        blurTimer = Timer.scheduledTimer(withTimeInterval: 3, repeats: false) { [weak self] _ in
            Task { @MainActor [weak self] in self?.idleBlurred() }
        }
        closeTimer = Timer.scheduledTimer(withTimeInterval: 30, repeats: false) { [weak self] _ in
            Task { @MainActor [weak self] in
                self?.suppressed = false
                self?.hide()
            }
        }
        followTimer?.invalidate()
        followTimer = Timer.scheduledTimer(withTimeInterval: 0.1, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in self?.followTargetWindow() }
        }
    }

    func setState(_ state: VisualState) {
        guard enabled, !suppressed else { return }
        self.state = state
        stateLabel?.stringValue = "\(target?.appName ?? "Atenea") • \(state.rawValue)"
        clickPulseTimer?.invalidate()
        clickPulseProgress = state == .clicking ? 0 : 1
        if state == .clicking {
            let started = Date()
            clickPulseTimer = Timer.scheduledTimer(withTimeInterval: 1.0 / 60.0, repeats: true) { [weak self] _ in
                Task { @MainActor [weak self] in
                    self?.advanceClickPulse(started: started)
                }
            }
        }
        overlay?.contentView?.needsDisplay = true
        startPreviewStream()
    }

    func setCursor(globalPoint: CGPoint) {
        guard let target, !suppressed else { return }
        let local = CGPoint(x: globalPoint.x - target.frame.minX,
                            y: globalPoint.y - target.frame.minY)
        cursor.position = local
        cursor.isHidden = false
        if let miniatureCursor {
            miniatureCursor.position = CGPoint(x: local.x / max(target.frame.width, 1) * miniatureCursor.bounds.width,
                                               y: (1 - local.y / max(target.frame.height, 1)) * miniatureCursor.bounds.height)
            miniatureCursor.isHidden = false
            miniatureCursor.needsDisplay = true
        }
        overlay?.contentView?.needsDisplay = true
    }

    private func advanceClickPulse(started: Date) {
        let progress = CGFloat(min(1, Date().timeIntervalSince(started) / 0.22))
        clickPulseProgress = progress
        overlay?.contentView?.needsDisplay = true
        if progress >= 1 {
            clickPulseTimer?.invalidate()
            clickPulseTimer = nil
        }
    }

    /// Critically damped cursor travel. It reaches the destination within the
    /// 300ms action budget and can be interrupted without overshoot.
    func animateCursor(globalPoint: CGPoint) async {
        guard let target, !suppressed else { return }
        let destination = CGPoint(x: globalPoint.x - target.frame.minX,
                                  y: globalPoint.y - target.frame.minY)
        if accessibilityReduceMotion {
            setCursor(globalPoint: globalPoint)
            return
        }
        let start = cursor.position
        for tick in 1...18 {
            if Task.isCancelled { return }
            let t = Double(tick) / 18.0
            let damped = 1 - (1 + 5 * t) * exp(-5 * t)
            cursor.position = CGPoint(x: start.x + (destination.x - start.x) * damped,
                                      y: start.y + (destination.y - start.y) * damped)
            cursor.isHidden = false
            miniatureCursor?.needsDisplay = true
            overlay?.contentView?.needsDisplay = true
            try? await Task.sleep(nanoseconds: 16_000_000)
        }
        setCursor(globalPoint: globalPoint)
    }

    func pause(byHumanInput: Bool) {
        guard enabled else { return }
        state = .pausedByUser
        stateLabel?.stringValue = (target?.appName ?? "Atenea") + " • " + VisualState.pausedByUser.rawValue
        miniature?.orderFrontRegardless()
        preview?.alphaValue = 0.45
        resumeButton?.isHidden = false
        previewTask?.cancel(); previewTask = nil
        overlay?.contentView?.needsDisplay = true
    }

    func resume() {
        suppressed = false
        suppressionTimer?.invalidate(); suppressionTimer = nil
        state = .observing
        preview?.alphaValue = 1
        stateLabel?.stringValue = (target?.appName ?? "Atenea") + " • " + VisualState.observing.rawValue
        resumeButton?.isHidden = true
        startPreviewStream()
        Task { await ActionGate.shared.resume() }
    }

    func suppressByUser() {
        suppressed = true
        hide()
        state = .suppressedByUser
        suppressionTimer?.invalidate()
        suppressionTimer = Timer.scheduledTimer(withTimeInterval: 30, repeats: false) { [weak self] _ in
            Task { @MainActor [weak self] in
                self?.suppressed = false
                self?.suppressionTimer = nil
            }
        }
    }

    func idleBlurred() {
        guard state != .pausedByUser else { return }
        state = .idleBlurred
        stateLabel?.stringValue = (target?.appName ?? "Atenea") + " • Observing"
        preview?.alphaValue = 0.18
        preview?.image = nil
        previewTask?.cancel(); previewTask = nil
    }

    func hide() {
        blurTimer?.invalidate(); blurTimer = nil
        closeTimer?.invalidate(); closeTimer = nil
        followTimer?.invalidate(); followTimer = nil
        clickPulseTimer?.invalidate(); clickPulseTimer = nil
        clickPulseProgress = 1
        previewTask?.cancel(); previewTask = nil
        overlay?.orderOut(nil)
        miniature?.orderOut(nil)
        state = .hidden
        cursor.isHidden = true
        miniatureCursor?.isHidden = true
    }

    var healthState: String { state.rawValue }
    var accessibilityReduceMotion: Bool { NSWorkspace.shared.accessibilityDisplayShouldReduceMotion }
    var accessibilityReduceTransparency: Bool { NSWorkspace.shared.accessibilityDisplayShouldReduceTransparency }
    var accessibilityIncreaseContrast: Bool { NSWorkspace.shared.accessibilityDisplayShouldIncreaseContrast }

    private func followTargetWindow() {
        guard let target else { return }
        guard let windows = CGWindowListCopyWindowInfo(.optionOnScreenOnly, kCGNullWindowID) as? [[String: Any]],
              let info = windows.first(where: {
                  (($0[kCGWindowNumber as String] as? NSNumber)?.uint32Value ?? 0) == target.windowID
              }),
              let bounds = info[kCGWindowBounds as String] as? [String: Any],
              let frame = CGRect(dictionaryRepresentation: bounds as CFDictionary) else {
            state = .unavailable
            stateLabel?.stringValue = "\(target.appName) • \(VisualState.unavailable.rawValue)"
            overlay?.orderOut(nil)
            previewTask?.cancel(); previewTask = nil
            return
        }
        if frame != target.frame {
            self.target = WindowTarget(pid: target.pid, bundleID: target.bundleID, appName: target.appName,
                                       windowID: target.windowID, frame: frame,
                                       imageWidth: target.imageWidth, imageHeight: target.imageHeight,
                                       scale: target.scale, visible: true, capturedAt: target.capturedAt)
            overlay?.setFrame(appKitFrame(frame), display: true)
            // A resize changes the preview's pixel geometry as well as the
            // border. Restarting the tiny stream keeps the miniature's aspect
            // ratio and cursor transform tied to the current window.
            startPreviewStream()
        }
    }

    /// ScreenCaptureKit/CoreGraphics use a top-left global origin for window
    /// bounds, while AppKit panels use a bottom-left origin. Convert only for
    /// drawing; event coordinates remain in Quartz space.
    private func appKitFrame(_ quartz: CGRect) -> CGRect {
        guard let screen = NSScreen.screens.first(where: { $0.frame.intersects(quartz) }) else { return quartz }
        return CGRect(x: quartz.minX, y: screen.frame.maxY - quartz.maxY,
                      width: quartz.width, height: quartz.height)
    }

    private func startPreviewStream() {
        previewTask?.cancel()
        guard enabled, !suppressed, let target, state != .idleBlurred, state != .hidden else { return }
        let interval: UInt64 = (state == .observing ? 200_000_000 : 66_000_000)
        previewTask = Task { [weak self] in
            while !Task.isCancelled {
                do {
                    if let data = try await Capture.preview(target) {
                        await MainActor.run {
                            guard let self, let image = NSImage(data: data) else { return }
                            self.preview?.image = image
                        }
                    }
                } catch { break }
                try? await Task.sleep(nanoseconds: interval)
            }
        }
    }

    private func ensureOverlay() {
        guard overlay == nil else { return }
        let panel = NSPanel(contentRect: .zero, styleMask: [.borderless, .nonactivatingPanel],
                            backing: .buffered, defer: false)
        panel.isOpaque = false
        panel.backgroundColor = .clear
        panel.hasShadow = false
        panel.ignoresMouseEvents = true
        panel.hidesOnDeactivate = false
        panel.level = .floating
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .transient, .ignoresCycle]
        let view = OverlayView(frame: .zero)
        view.cursor = cursor
        view.controller = self
        panel.contentView = view
        overlay = panel
    }

    private func ensureMiniature() {
        guard miniature == nil else { return }
        let saved = UserDefaults.standard.string(forKey: "atenea.visual_feedback.miniature_frame")
        let defaultScreen = NSScreen.main?.visibleFrame ?? NSRect(x: 0, y: 0, width: 1440, height: 900)
        let frame = saved.flatMap { NSRectFromString($0) } ?? NSRect(
            x: defaultScreen.maxX - 376, y: defaultScreen.maxY - 256, width: 360, height: 240)
        let panel = NSPanel(contentRect: frame, styleMask: [.titled, .resizable, .nonactivatingPanel],
                            backing: .buffered, defer: false)
        panel.title = "Atenea"
        panel.minSize = NSSize(width: 280, height: 180)
        panel.maxSize = NSSize(width: 640, height: 420)
        panel.isFloatingPanel = true
        panel.level = .floating
        panel.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .transient, .ignoresCycle]
        let root = NSView(frame: panel.contentRect(forFrameRect: panel.frame))
        root.wantsLayer = true
        let panelColor = NSWorkspace.shared.accessibilityDisplayShouldReduceTransparency
            ? NSColor.windowBackgroundColor : NSColor.windowBackgroundColor.withAlphaComponent(0.94)
        root.layer?.backgroundColor = panelColor.cgColor
        let image = NSImageView(frame: NSRect(x: 8, y: 8, width: root.bounds.width - 16, height: root.bounds.height - 42))
        image.imageScaling = .scaleProportionallyUpOrDown
        image.autoresizingMask = [.width, .height]
        root.addSubview(image)
        preview = image
        let pipCursor = MiniatureCursorView(frame: image.frame)
        pipCursor.autoresizingMask = [.width, .height]
        pipCursor.isHidden = true
        root.addSubview(pipCursor)
        miniatureCursor = pipCursor
        let label = NSTextField(labelWithString: "Observing")
        label.frame = NSRect(x: 10, y: root.bounds.height - 30, width: root.bounds.width - 80, height: 20)
        label.autoresizingMask = [.width, .minYMargin]
        root.addSubview(label)
        stateLabel = label
        let close = NSButton(title: "×", target: self, action: #selector(closeMiniature))
        close.bezelStyle = .texturedRounded
        close.frame = NSRect(x: root.bounds.width - 32, y: root.bounds.height - 32, width: 24, height: 24)
        close.autoresizingMask = [.minXMargin, .minYMargin]
        root.addSubview(close)
        let resume = NSButton(title: "Resume", target: self, action: #selector(resumeFromPanel))
        resume.bezelStyle = .rounded
        resume.frame = NSRect(x: root.bounds.width - 94, y: 10, width: 78, height: 22)
        resume.autoresizingMask = [.minXMargin, .maxYMargin]
        resume.isHidden = true
        root.addSubview(resume)
        resumeButton = resume
        panel.contentView = root
        miniature = panel
        NotificationCenter.default.addObserver(self, selector: #selector(saveMiniatureFrame),
            name: NSWindow.didMoveNotification, object: panel)
        NotificationCenter.default.addObserver(self, selector: #selector(saveMiniatureFrame),
            name: NSWindow.didResizeNotification, object: panel)
    }

    @objc private func closeMiniature() { suppressByUser() }
    @objc private func resumeFromPanel() { resume() }
    @objc private func accessibilityChanged() {
        overlay?.contentView?.needsDisplay = true
        let color = NSWorkspace.shared.accessibilityDisplayShouldReduceTransparency
            ? NSColor.windowBackgroundColor : NSColor.windowBackgroundColor.withAlphaComponent(0.94)
        miniature?.contentView?.layer?.backgroundColor = color.cgColor
    }
    @objc private func sessionUnavailable() {
        hide()
        Task { await ActionGate.shared.interrupt() }
    }
    @objc private func saveMiniatureFrame(_ notification: Notification) {
        guard let window = notification.object as? NSWindow else { return }
        UserDefaults.standard.set(NSStringFromRect(window.frame),
                                  forKey: "atenea.visual_feedback.miniature_frame")
    }
}

@MainActor
private final class OverlayView: NSView {
    weak var controller: VisualFeedbackController?
    var cursor: CursorView?
    override func draw(_ dirtyRect: NSRect) {
        guard let context = NSGraphicsContext.current?.cgContext else { return }
        let bounds = self.bounds.insetBy(dx: 1.5, dy: 1.5)
        let colors = [NSColor(calibratedRed: 8/255, green: 127/255, blue: 158/255, alpha: 1).cgColor,
                      NSColor(calibratedRed: 109/255, green: 75/255, blue: 195/255, alpha: 1).cgColor] as CFArray
        let gradient = CGGradient(colorsSpace: CGColorSpaceCreateDeviceRGB(), colors: colors, locations: [0, 1])
        let path = CGPath(roundedRect: bounds, cornerWidth: 12, cornerHeight: 12, transform: nil)
        context.saveGState()
        context.setLineWidth(controller?.accessibilityReduceTransparency == true ? 4 : 3)
        context.addPath(path)
        context.replacePathWithStrokedPath()
        if controller?.accessibilityIncreaseContrast == true {
            context.setStrokeColor(NSColor(calibratedRed: 8/255, green: 127/255, blue: 158/255, alpha: 1).cgColor)
            context.drawPath(using: .stroke)
        } else if let gradient {
            context.clip()
            context.drawLinearGradient(gradient, start: .zero, end: CGPoint(x: bounds.maxX, y: bounds.maxY), options: [])
        }
        context.restoreGState()
        cursor?.draw(bounds: bounds)
        if let controller, controller.clickPulseProgress < 1 {
            cursor?.drawPulse(bounds: bounds, progress: controller.clickPulseProgress)
        }
        let label = "Atenea • \(controller?.healthState ?? "Observing")"
        let attributes: [NSAttributedString.Key: Any] = [
            .font: NSFont.systemFont(ofSize: 11, weight: .semibold),
            .foregroundColor: NSColor.labelColor
        ]
        NSString(string: label).draw(at: CGPoint(x: 10, y: bounds.height - 22), withAttributes: attributes)
    }
}

@MainActor
private final class CursorView {
    var position = CGPoint.zero
    var isHidden = true
    func draw(bounds: CGRect) {
        guard !isHidden, let context = NSGraphicsContext.current?.cgContext else { return }
        let p = CGPoint(x: position.x, y: bounds.height - position.y)
        let path = CGMutablePath()
        path.move(to: p); path.addLine(to: CGPoint(x: p.x + 1, y: p.y - 24))
        path.addLine(to: CGPoint(x: p.x + 7, y: p.y - 18))
        path.addLine(to: CGPoint(x: p.x + 13, y: p.y - 20))
        path.closeSubpath()
        context.saveGState(); context.setFillColor(NSColor.white.cgColor); context.setStrokeColor(NSColor.black.cgColor)
        context.setLineWidth(2); context.addPath(path); context.drawPath(using: .fillStroke); context.restoreGState()
        let attributes: [NSAttributedString.Key: Any] = [.font: NSFont.boldSystemFont(ofSize: 8), .foregroundColor: NSColor(calibratedRed: 8/255, green: 127/255, blue: 158/255, alpha: 1)]
        NSString(string: "A").draw(at: CGPoint(x: p.x + 4, y: p.y - 17), withAttributes: attributes)
    }

    func drawPulse(bounds: CGRect, progress: CGFloat) {
        guard !isHidden, let context = NSGraphicsContext.current?.cgContext else { return }
        let p = CGPoint(x: position.x, y: bounds.height - position.y)
        let radius = 7 + 24 * progress
        context.saveGState()
        context.setStrokeColor(NSColor(calibratedRed: 109/255, green: 75/255, blue: 195/255,
                                       alpha: 1 - progress).cgColor)
        context.setLineWidth(2)
        context.strokeEllipse(in: CGRect(x: p.x - radius, y: p.y - radius,
                                         width: radius * 2, height: radius * 2))
        context.restoreGState()
    }
}

@MainActor
private final class MiniatureCursorView: NSView {
    var position = CGPoint.zero
    override func draw(_ dirtyRect: NSRect) {
        guard !isHidden, let context = NSGraphicsContext.current?.cgContext else { return }
        let path = CGMutablePath()
        path.move(to: position)
        path.addLine(to: CGPoint(x: position.x + 1, y: position.y - 18))
        path.addLine(to: CGPoint(x: position.x + 6, y: position.y - 14))
        path.closeSubpath()
        context.setFillColor(NSColor.white.cgColor)
        context.setStrokeColor(NSColor.black.cgColor)
        context.setLineWidth(1.5)
        context.addPath(path)
        context.drawPath(using: .fillStroke)
        let attributes: [NSAttributedString.Key: Any] = [.font: NSFont.boldSystemFont(ofSize: 6), .foregroundColor: NSColor(calibratedRed: 8/255, green: 127/255, blue: 158/255, alpha: 1)]
        NSString(string: "A").draw(at: CGPoint(x: position.x + 3, y: position.y - 14), withAttributes: attributes)
    }
}
