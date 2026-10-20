import XCTest
import AppKit
@testable import atenea_desktop_helper

final class InputSafetyTests: XCTestCase {
    func testInactiveOrUnknownConsoleSessionIsRefused() {
        XCTAssertTrue(Permissions.sessionAvailable(onConsole: true, loggedIn: true, frontmostBundle: "test", hasScreens: true))
        for (console, login, bundle, screens) in [
            (true, true, "com.apple.loginwindow", true), (false, true, "test", true),
            (true, false, "test", true), (true, true, nil, true), (true, true, "test", false)
        ] {
            XCTAssertFalse(Permissions.sessionAvailable(onConsole: console, loggedIn: login, frontmostBundle: bundle, hasScreens: screens))
        }
        XCTAssertFalse(Permissions.sessionAvailable(onConsole: nil, loggedIn: nil, frontmostBundle: "test", hasScreens: true))
    }

    func testSecureSubroleAndUnknownSecurityAreRefused() {
        for (role, subrole, status, hasValue) in [
            ("AXTextField", "AXSecureTextField", AXError.success, true),
            ("AXSecureTextField", "", AXError.success, true),
            ("AXUnknown", "", AXError.success, true),
            ("AXTextField", nil, AXError.attributeUnsupported, false),
            ("AXTextArea", nil, AXError.cannotComplete, false),
            ("AXGroup", nil, AXError.success, true)
        ] {
            XCTAssertThrowsError(try Input.validateSecurity(role: role, subrole: subrole, status: status, hasSubroleValue: hasValue))
        }
        XCTAssertNoThrow(try Input.validateSecurity(role: "AXTextField", subrole: "", status: .success, hasSubroleValue: true))
        XCTAssertNoThrow(try Input.validateSecurity(role: "AXTextArea", subrole: nil, status: .attributeUnsupported, hasSubroleValue: false))
        XCTAssertNoThrow(try Input.validateSecurity(role: "AXWindow", subrole: nil, status: .attributeUnsupported, hasSubroleValue: false))
    }

    @MainActor
    func testMousePayloadIsWindowBoundAndSourcePrivate() throws {
        _ = NSApplication.shared
        let target = WindowTarget(pid: 42, bundleID: "test", appName: "test", windowID: 919191,
                                  frame: CGRect(x: -500, y: -200, width: 500, height: 400),
                                  imageWidth: 1000, imageHeight: 800, scale: CGSize(width: 2, height: 2),
                                  visible: true, capturedAt: Date())
        let session = try InputSession(target: target)
        XCTAssertNotEqual(session.source.sourceStateID, .combinedSessionState)
        XCTAssertNotEqual(session.source.sourceStateID, .hidSystemState)
        for type in [CGEventType.leftMouseDown, .leftMouseDragged, .leftMouseUp, .mouseMoved] {
            let event = try XCTUnwrap(session.mouse(type, at: CGPoint(x: -400, y: -100)))
            XCTAssertEqual(event.location, CGPoint(x: 100, y: 100))
            XCTAssertEqual(NSEvent(cgEvent: event)?.windowNumber, 919191)
        }
        XCTAssertEqual(InputSession.dragPoint(from: .zero, to: CGPoint(x: 180, y: 90), step: 1), CGPoint(x: 10, y: 5))
    }
}
