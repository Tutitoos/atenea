// swift-tools-version:5.10
import PackageDescription

// The desktop helper is a separate artifact on purpose. Atenea is a Go binary
// and everything it needs from macOS -- the accessibility tree, synthetic
// input, ScreenCaptureKit -- lives behind Objective-C and Swift APIs that cgo
// reaches badly and that would put an unreachable, untestable slab of code
// inside the coverage profile of every platform. Keeping it out here means the
// Go side stays pure Go and testable against a double on all four CI legs.
// 5.10 and not higher: the macOS CI runner ships Swift 5.10, and a manifest it
// cannot parse fails before it reads a line of code -- which is how pinning the
// language mode to 6 broke a build that the mode was meant to protect.
//
// The code is written to satisfy Swift 6's concurrency rules anyway. Under 5.10
// most of them are warnings rather than errors, so the CI build is the only
// place that checks them for real: `swift build -Xswiftc -strict-concurrency=complete`
// is the local approximation, and it is an approximation.
let package = Package(
    name: "atenea-desktop-helper",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(name: "atenea-desktop-helper", path: "Sources/atenea-desktop-helper")
    ]
)
