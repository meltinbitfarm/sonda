// swift-tools-version: 6.0
// Sonda Swift SDK (D17: same repository as the Go server, sources in sdk/).
// Apps add this package by the repository URL; SwiftPM ignores the Go code.

import PackageDescription

let package = Package(
    name: "Sonda",
    platforms: [
        // iOS 17 / macOS 14: AppTransaction.environment (TestFlight check), Storefront,
        // OSAllocatedUnfairLock and Swift Testing are all available; covers every
        // device that runs a current App Store build without forcing iOS 18.
        .iOS(.v17),
        .macOS(.v14),
    ],
    products: [
        .library(name: "Sonda", targets: ["Sonda"]),
    ],
    targets: [
        .target(
            name: "Sonda",
            path: "sdk/Sources/Sonda"
        ),
        .testTarget(
            name: "SondaTests",
            dependencies: ["Sonda"],
            path: "sdk/Tests/SondaTests"
        ),
    ],
    swiftLanguageModes: [.v6]
)
