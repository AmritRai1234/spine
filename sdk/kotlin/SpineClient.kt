package dev.spine.sdk

import kotlinx.coroutines.*
import org.json.JSONObject
import java.net.URI
import java.net.http.HttpClient
import java.net.http.HttpRequest
import java.net.http.HttpResponse
import java.net.http.WebSocket
import java.util.concurrent.CompletionStage
import java.util.concurrent.ConcurrentHashMap

/**
 * Native Kotlin Client SDK for Spine Backend Engine.
 * Supports Coroutines, async event emission, HTTP queries, and safe main thread WebSocket state dispatching.
 */
class SpineClient(
    val baseUrl: String = "http://localhost:8080",
    val apiKey: String? = null
) {
    private val httpClient = HttpClient.newHttpClient()
    private val listeners = ConcurrentHashMap<String, MutableList<(JSONObject) -> Unit>>()
    private val scope = CoroutineScope(Dispatchers.IO + SupervisorJob())

    /**
     * Emits an event asynchronously to the Spine runtime engine.
     */
    suspend fun emit(event: String, payload: Map<String, Any> = emptyMap()): JSONObject = withContext(Dispatchers.IO) {
        val url = URI.create("${baseUrl.trimEnd('/')}/emit")
        val bodyJson = JSONObject().apply {
            put("event", event)
            put("payload", JSONObject(payload))
        }.toString()

        val reqBuilder = HttpRequest.newBuilder()
            .uri(url)
            .header("Content-Type", "application/json")
            .POST(HttpRequest.BodyPublishers.ofString(bodyJson))

        apiKey?.let { reqBuilder.header("X-API-Key", it) }

        val response = httpClient.send(reqBuilder.build(), HttpResponse.BodyHandlers.ofString())
        JSONObject(response.body())
    }

    /**
     * Registers a state listener callback.
     */
    fun listenState(stateName: String, callback: (JSONObject) -> Unit) {
        listeners.computeIfAbsent(stateName) { mutableListOf() }.add(callback)
    }

    /**
     * Connects to Spine real-time WebSocket state broadcasting hub.
     */
    fun connectWebSocket() {
        val scheme = if (baseUrl.startsWith("https")) "wss" else "ws"
        val host = baseUrl.substringAfter("://")
        var wsUrl = "$scheme://$host/ws"
        apiKey?.let { wsUrl += "?token=$it" }

        httpClient.newWebSocketBuilder().buildAsync(URI.create(wsUrl), object : WebSocket.Listener {
            override fun onText(webSocket: WebSocket?, data: CharSequence?, last: Boolean): CompletionStage<*>? {
                data?.let {
                    try {
                        val json = JSONObject(it.toString())
                        if (json.optString("type") == "state") {
                            val state = json.optString("state")
                            val payload = json.optJSONObject("payload") ?: JSONObject()
                            dispatchToMainThread(state, payload)
                        }
                    } catch (e: Exception) {
                        e.printStackTrace()
                    }
                }
                return super.onText(webSocket, data, last)
            }
        })
    }

    private fun dispatchToMainThread(state: String, payload: JSONObject) {
        listeners[state]?.forEach { cb ->
            scope.launch(Dispatchers.Main) {
                cb(payload)
            }
        }
    }

    fun close() {
        scope.cancel()
    }
}
