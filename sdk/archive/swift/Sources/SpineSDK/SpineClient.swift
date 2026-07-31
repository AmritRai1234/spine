import Foundation

/// Native Swift Client SDK for Spine Backend Engine (iOS, macOS, watchOS, tvOS).
/// Supports Swift Concurrency (async/await), URLSession, and MainActor UI state bindings.
public final class SpineClient: @unchecked Sendable {
    public let baseURL: URL
    public let apiKey: String?
    
    private let session: URLSession
    private var listeners: [String: [( [String: Any] ) -> Void]] = [:]
    private var webSocketTask: URLSessionWebSocketTask?
    private let lock = NSLock()
    
    public init(baseURL: String = "http://localhost:8080", apiKey: String? = null) {
        guard let url = URL(string: baseURL.trimmingCharacters(in: CharacterSet(charactersIn: "/"))) else {
            fatalError("Invalid Spine base URL: \(baseURL)")
        }
        self.baseURL = url
        self.apiKey = apiKey
        self.session = URLSession(configuration: .default)
    }
    
    /// Emits an event asynchronously to Spine runtime engine.
    public func emit(event: String, payload: [String: Any] = [:]) async throws -> [String: Any] {
        let emitURL = baseURL.appendingPathComponent("emit")
        var request = URLRequest(url: emitURL)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        if let apiKey = apiKey {
            request.setValue(apiKey, forHTTPHeaderField: "X-API-Key")
        }
        
        let bodyDict: [String: Any] = ["event": event, "payload": payload]
        request.httpBody = try JSONSerialization.data(withJSONObject: bodyDict)
        
        let (data, response) = try await session.data(for: request)
        guard let httpResponse = response as? HTTPURLResponse, (200...299).contains(httpResponse.statusCode) else {
            throw URLError(.badServerResponse)
        }
        
        guard let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw URLError(.cannotParseResponse)
        }
        return json
    }
    
    /// Registers a state listener callback that dispatches directly on @MainActor / DispatchQueue.main.
    public func listenState(_ stateName: String, callback: @escaping @MainActor ([String: Any]) -> Void) {
        lock.lock()
        defer { lock.unlock() }
        if listeners[stateName] == nil {
            listeners[stateName] = []
        }
        listeners[stateName]?.append { payload in
            Task { @MainActor in
                callback(payload)
            }
        }
    }
    
    /// Connects to Spine real-time WebSocket state broadcasting hub.
    public func connectWebSocket() {
        let scheme = baseURL.scheme == "https" ? "wss" : "ws"
        guard var components = URLComponents(url: baseURL.appendingPathComponent("ws"), resolvingAgainstBaseURL: true) else { return }
        components.scheme = scheme
        if let apiKey = apiKey {
            components.queryItems = [URLQueryItem(name: "token", value: apiKey)]
        }
        
        guard let wsURL = components.url else { return }
        let task = session.webSocketTask(with: wsURL)
        self.webSocketTask = task
        task.resume()
        receiveWebSocketMessage()
    }
    
    private func receiveWebSocketMessage() {
        webSocketTask?.receive { [weak self] result in
            guard let self = self else { return }
            switch result {
            case .success(let message):
                switch message {
                case .string(let text):
                    self.handleIncomingStateMessage(text)
                case .data(let data):
                    if let text = String(data: data, encoding: .utf8) {
                        self.handleIncomingStateMessage(text)
                    }
                @unknown default:
                    break
                }
                self.receiveWebSocketMessage()
            case .failure(let error):
                print("[SpineSwift] WebSocket receive error: \(error)")
            }
        }
    }
    
    private func handleIncomingStateMessage(_ text: String) {
        guard let data = text.data(using: .utf8),
              let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let type = json["type"] as? String, type == "state",
              let stateName = json["state"] as? String,
              let payload = json["payload"] as? [String: Any] else {
            return
        }
        
        lock.lock()
        let callbacks = listeners[stateName] ?? []
        lock.unlock()
        
        for cb in callbacks {
            cb(payload)
        }
    }
}
