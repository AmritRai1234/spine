# Spine Swift SDK (iOS, macOS, watchOS, tvOS)

Native **Swift SDK** for Spine Backend Engine with Swift Concurrency (`async/await`), `URLSessionWebSocketTask`, and `@MainActor` SwiftUI / UIKit state bindings.

## Installation via Swift Package Manager (SwiftPM)

Add SpineSDK dependency in your `Package.swift` or Xcode Project:

```swift
dependencies: [
    .package(url: "https://github.com/AmritRai1234/spine.git", from: "2.4.0")
]
```

## Quick Start (SwiftUI / UIKit)

```swift
import SwiftUI
import SpineSDK

@MainActor
class DashboardViewModel: ObservableObject {
    @Published var leadStatus: String = "Waiting..."
    private let client = SpineClient(baseURL: "http://localhost:8080", apiKey: "your-key")
    
    init() {
        client.connectWebSocket()
        
        // Listen to state updates safely on @MainActor
        client.listenState("LEAD_STATUS") { [weak self] payload in
            if let status = payload["status"] as? String {
                self?.leadStatus = status
            }
        }
    }
    
    func submitLead(email: String) async {
        do {
            let res = try await client.emit(event: "SUBMIT_LEAD", payload: ["email": email])
            print("Emitted: \(res)")
        } catch {
            print("Emit error: \(error)")
        }
    }
}
```
