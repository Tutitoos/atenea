// swift-tools-version:6.0
import PackageDescription

// The desktop helper is a separate artifact on purpose. Atenea is a Go binary
// and everything it needs from macOS -- the accessibility tree, synthetic
// input, ScreenCaptureKit -- lives behind Objective-C and Swift APIs that cgo
// reaches badly and that would put an unreachable, untestable slab of code
// inside the coverage profile of every platform. Keeping it out here means the
// Go side stays pure Go and testable against a double on all four CI legs.
let package = Package(
    name: "atenea-desktop-helper",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(name: "atenea-desktop-helper", path: "Sources/atenea-desktop-helper")
    ],
    // Pinned rather than left to the toolchain, because leaving it to the
    // toolchain is how a local build and a CI build check different things:
    // under Swift 5 the concurrency rules are warnings and under Swift 6 they
    // are errors, so this compiled here and failed there. Same shape as the
    // lint job that only ran on ubuntu and never read the darwin files.
    swiftLanguageModes: [.v6]
)
