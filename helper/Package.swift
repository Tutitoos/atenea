// swift-tools-version:5.9
import PackageDescription

// The desktop helper is a separate artifact on purpose. Atenea is a Go binary
// and everything it needs from macOS -- the accessibility tree, synthetic
// input, ScreenCaptureKit -- lives behind Objective-C and Swift APIs that cgo
// reaches badly and that would put an unreachable, untestable slab of code
// inside the coverage profile of every platform. Keeping it out here means the
// Go side stays pure Go and testable against a double on all four CI legs.
let package = Package(
    name: "atenea-desktop-helper",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(name: "atenea-desktop-helper", path: "Sources/atenea-desktop-helper")
    ]
)
