// swift-tools-version: 6.0
import PackageDescription

// No third-party dependencies. The app talks to punchcard over HTTP with
// URLSession, stores one token in the Keychain, and draws an NSMenu — none of
// that needs a package, and a menu bar app that pulls in a dependency tree is a
// menu bar app that breaks on someone else's release schedule.
let package = Package(
    name: "PunchcardBar",
    platforms: [.macOS(.v14)],
    targets: [
        .executableTarget(name: "PunchcardBar", path: "Sources/PunchcardBar"),
        .testTarget(
            name: "PunchcardBarTests",
            dependencies: ["PunchcardBar"],
            path: "Tests/PunchcardBarTests"
        ),
    ]
)
