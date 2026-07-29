# Spine Kotlin SDK (Android & JVM)

Native **Kotlin SDK** for Spine Backend Engine with Kotlin Coroutines, async HTTP, and safe Android / JVM UI thread main-dispatcher state marshaling.

## Installation

Add to your `build.gradle.kts`:

```kotlin
dependencies {
    implementation("dev.spine:spine-kotlin:2.4.0")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.0")
}
```

## Quick Start

```kotlin
import dev.spine.sdk.SpineClient

val client = SpineClient(baseUrl = "http://10.0.2.2:8080", apiKey = "your-api-key")

// Connect to real-time WebSocket state stream
client.connectWebSocket()

// Listen to state updates safely on Main thread
client.listenState("LEAD_STATUS") { payload ->
    println("Received lead status: ${payload.getString("status")}")
}

// Emit event from UI
CoroutineScope(Dispatchers.Main).launch {
    val result = client.emit("SUBMIT_LEAD", mapOf("email" to "user@android.dev"))
    println("Emit status: ${result.getString("status")}")
}
```
