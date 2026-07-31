// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "SpineSDK",
    platforms: [
        .iOS(.v15),
        .macOS(.v12),
        .tvOS(.v15),
        .watchOS(.v8)
    ],
    products: [
        .library(
            name: "SpineSDK",
            targets: ["SpineSDK"]
        ),
    ],
    targets: [
        .target(
            name: "SpineSDK",
            dependencies: []
        ),
        .testTarget(
            name: "SpineSDKTests",
            dependencies: ["SpineSDK"]
        ),
    ]
)
